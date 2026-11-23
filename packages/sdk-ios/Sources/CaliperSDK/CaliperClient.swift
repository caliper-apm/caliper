import Foundation

public struct CaliperConfig: Sendable {
    public let endpointURL: URL
    public let apiKey: String?
    public let resource: ResourceIdentity
    public let installSeed: String
    public let maxQueueSize: Int
    public let maxConsecutiveFailures: Int

    public init(
        endpointURL: URL,
        apiKey: String? = nil,
        resource: ResourceIdentity,
        installSeed: String = UUID().uuidString.lowercased(),
        maxQueueSize: Int = 200,
        maxConsecutiveFailures: Int = 5
    ) {
        self.endpointURL = endpointURL
        self.apiKey = apiKey
        self.resource = resource
        self.installSeed = installSeed
        self.maxQueueSize = max(1, min(maxQueueSize, 200))
        self.maxConsecutiveFailures = max(1, maxConsecutiveFailures)
    }
}

/// Thread-safe, off-main-thread, fail-closed Native iOS APM client.
/// Zero React Native dependencies; optional RN adapters call public enqueue/scene/breadcrumb APIs.
public final class CaliperClient: @unchecked Sendable {
    private let config: CaliperConfig
    private let session: URLSession
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

    public func currentBreadcrumbs() -> [Breadcrumb] {
        worker.sync { breadcrumbs.snapshot() }
    }

    /// Flushes queued events to POST /v1/ingest off the main thread.
    public func flush() async throws -> Int {
        let eventsToSend: [TelemetryEvent] = worker.sync {
            guard !circuitTripped, !queue.isEmpty else { return [] }
            let batch = queue
            queue.removeAll(keepingCapacity: true)
            return batch
        }
        guard !eventsToSend.isEmpty else { return 0 }

        let envelope = BatchEnvelope(
            sentAtMS: Int64(Date().timeIntervalSince1970 * 1000),
            resource: config.resource,
            events: eventsToSend
        )
        let body = try JSONEncoder().encode(envelope)
        var req = URLRequest(url: config.endpointURL.appendingPathComponent("v1/ingest"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let key = config.apiKey, !key.isEmpty {
            req.setValue(key, forHTTPHeaderField: "X-Caliper-Key")
        }
        req.httpBody = body

        do {
            let (_, response) = try await session.data(for: req)
            guard let http = response as? HTTPURLResponse, (200...299).contains(http.statusCode) else {
                recordTransportFailure()
                throw URLError(.badServerResponse)
            }
            worker.sync { consecutiveFailures = 0 }
            return eventsToSend.count
        } catch {
            recordTransportFailure()
            throw error
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
