import Foundation

public struct CaliperConfig: Sendable {
    public let endpointURL: URL
    public let apiKey: String?
    public let resource: ResourceIdentity
    public let installSeed: String
    public let maxQueueSize: Int
    public let maxConsecutiveFailures: Int
    public let spoolDirectoryURL: URL?

    public init(
        endpointURL: URL,
        apiKey: String? = nil,
        resource: ResourceIdentity,
        installSeed: String = UUID().uuidString.lowercased(),
        maxQueueSize: Int = 200,
        maxConsecutiveFailures: Int = 5,
        spoolDirectoryURL: URL? = nil
    ) {
        self.endpointURL = endpointURL
        self.apiKey = apiKey
        self.resource = resource
        self.installSeed = installSeed
        self.maxQueueSize = max(1, min(maxQueueSize, 200))
        self.maxConsecutiveFailures = max(1, maxConsecutiveFailures)
        self.spoolDirectoryURL = spoolDirectoryURL
    }
}

/// Thread-safe, off-main-thread, fail-closed Native iOS APM client.
/// Zero React Native dependencies; optional RN adapters call public enqueue/scene/breadcrumb APIs.
public final class CaliperClient: @unchecked Sendable {
    private let config: CaliperConfig
    private let session: URLSession
    private let diskSpool: CaliperDiskQueue?
    private let worker = DispatchQueue(label: "dev.caliper.apm.worker", qos: .utility)

    private var currentScene: String = "startup"
    private var breadcrumbs = BreadcrumbRingBuffer(capacity: 32)
    private var queue: [TelemetryEvent] = []
    private var lease: PolicyLease?
    private var quota = SlidingQuota(windowMS: 86_400_000, cap: 50)
    private var consecutiveFailures = 0
    private var circuitTripped = false

    public init(config: CaliperConfig, session: URLSession = .shared) {
        self.config = config
        self.session = session
        self.diskSpool = config.spoolDirectoryURL.map { CaliperDiskQueue(directoryURL: $0) }
    }

    public func applyLease(_ nextLease: PolicyLease) {
        worker.sync {
            self.lease = nextLease
            self.quota = SlidingQuota(windowMS: 86_400_000, cap: nextLease.maxEvents24h)
        }
    }

    public func setScene(_ scene: String) {
        worker.async {
            self.currentScene = scene
            self.breadcrumbs.append(Breadcrumb(
                timestampMS: Int64(Date().timeIntervalSince1970 * 1000),
                category: "navigation",
                message: "scene:\(scene)"
            ))
        }
    }

    public func addBreadcrumb(category: String, message: String) {
        worker.async {
            self.breadcrumbs.append(Breadcrumb(
                timestampMS: Int64(Date().timeIntervalSince1970 * 1000),
                category: category,
                message: message
            ))
        }
    }

    /// Records a custom multi-stage business journey trace (e.g. article_open_to_readable).
    @discardableResult
    public func recordCustomJourney(
        journeyName: String,
        status: String = "success",
        durationMS: Double? = nil,
        stages: [SceneStage]? = nil,
        errorCode: String? = nil,
        scene: String? = nil
    ) -> Bool {
        let payload = CustomScenePayload(
            journeyName: journeyName,
            status: status,
            durationMS: durationMS,
            errorCode: errorCode,
            stages: stages?.isEmpty == true ? nil : stages
        )
        return enqueue(kind: "custom_scene", payload: .customScene(payload), scene: scene)
    }

    /// Enqueues an event if lease is active, quota permits, and circuit breaker is closed.
    @discardableResult
    public func enqueue(
        kind: String,
        payload: EventPayload,
        scene: String? = nil,
        windowID: String? = nil,
        occurredAtMS: Int64 = Int64(Date().timeIntervalSince1970 * 1000),
        bypassSampling: Bool = false
    ) -> Bool {
        worker.sync {
            guard !circuitTripped else { return false }
            var activePolicyID: String? = nil
            var activeRatePPM: Int? = nil

            if !bypassSampling {
                guard let lease, lease.isActive(nowMS: occurredAtMS, installSeed: config.installSeed) else {
                    return false
                }
                guard quota.tryConsume(timestampMS: occurredAtMS) else {
                    return false
                }
                activePolicyID = lease.policyID
                activeRatePPM = Int(lease.ratePPM)
            }

            if queue.count >= config.maxQueueSize {
                queue.removeFirst()
            }
            let event = TelemetryEvent(
                kind: kind,
                occurredAtMS: occurredAtMS,
                scene: scene ?? currentScene,
                windowID: windowID,
                policyID: activePolicyID,
                sampleRatePPM: activeRatePPM,
                payload: payload
            )
            queue.append(event)
            return true
        }
    }

    /// Captures an immediate process resource reading using public Darwin APIs.
    @discardableResult
    public func captureResourceWindow(windowDurationMS: Int64, cpuPct: Double, sampleCount: Int = 1, bypassSampling: Bool = false) -> Bool {
        guard let memKiB = CaliperSystemMetrics.physicalFootprintKiB() else { return false }
        let payload = ResourceWindowPayload(
            windowDurationMS: windowDurationMS,
            cpuPct: max(0, cpuPct),
            memoryKind: "ios_phys_footprint_kib",
            memoryPeakKiB: memKiB,
            memoryAvgKiB: memKiB,
            sampleCount: max(1, sampleCount)
        )
        return enqueue(kind: "resource_window", payload: .resource(payload), bypassSampling: bypassSampling)
    }

    public func pendingCount() -> Int {
        worker.sync { queue.count }
    }

    public func spooledBatchCount() -> Int {
        diskSpool?.count() ?? 0
    }

    public func currentBreadcrumbs() -> [Breadcrumb] {
        worker.sync { breadcrumbs.snapshot() }
    }

    /// Flushes queued events (plus any previously spooled offline batches) to POST /v1/ingest.
    /// On network failure, persists the encoded batch to `diskSpool` so crashes/FOOMs survive offline boots.
    public func flush() async throws -> Int {
        var sentCount = 0

        // 1. Snapshot current in-memory queue up front so no crash/FOOM is lost if spool drain fails offline.
        let eventsToSend: [TelemetryEvent] = worker.sync {
            guard !circuitTripped, !queue.isEmpty else { return [] }
            let batch = queue
            queue.removeAll(keepingCapacity: true)
            return batch
        }
        let currentBody: Data? = if !eventsToSend.isEmpty {
            try JSONEncoder().encode(BatchEnvelope(
                sentAtMS: Int64(Date().timeIntervalSince1970 * 1000),
                resource: config.resource,
                events: eventsToSend
            ))
        } else {
            nil
        }

        // 2. Drain any previously spooled offline batches first.
        if let spool = diskSpool {
            var spooledBatches: [Data] = []
            _ = spool.drainEach { data in
                spooledBatches.append(data)
            }
            for (idx, body) in spooledBatches.enumerated() {
                do {
                    try await postJSONBody(body)
                    sentCount += 1
                } catch {
                    for rem in spooledBatches[idx...] {
                        spool.enqueue(rem)
                    }
                    if let cur = currentBody {
                        spool.enqueue(cur)
                    }
                    recordTransportFailure()
                    throw error
                }
            }
        }

        guard let body = currentBody else { return sentCount }
        do {
            try await postJSONBody(body)
            worker.sync { consecutiveFailures = 0 }
            return sentCount + eventsToSend.count
        } catch {
            diskSpool?.enqueue(body)
            recordTransportFailure()
            throw error
        }
    }

    private func postJSONBody(_ body: Data) async throws {
        var req = URLRequest(url: config.endpointURL.appendingPathComponent("v1/ingest"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let key = config.apiKey, !key.isEmpty {
            req.setValue(key, forHTTPHeaderField: "X-Caliper-Key")
        }
        req.httpBody = body
        let (_, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse, (200...299).contains(http.statusCode) else {
            throw URLError(.badServerResponse)
        }
    }

    private func recordTransportFailure() {
        worker.sync {
            consecutiveFailures += 1
            if consecutiveFailures >= config.maxConsecutiveFailures {
                circuitTripped = true
            }
        }
    }
}
