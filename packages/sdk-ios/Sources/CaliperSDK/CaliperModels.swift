import Foundation

public struct BatchEnvelope: Codable, Sendable {
    public let schemaVersion: Int
    public let sentAtMS: Int64
    public let resource: ResourceIdentity
    public let events: [TelemetryEvent]

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case sentAtMS = "sent_at_ms"
        case resource
        case events
    }

    public init(schemaVersion: Int = 1, sentAtMS: Int64, resource: ResourceIdentity, events: [TelemetryEvent]) {
        self.schemaVersion = schemaVersion
        self.sentAtMS = sentAtMS
        self.resource = resource
        self.events = events
    }
}

public struct ResourceIdentity: Codable, Sendable {
    public let appID: String
    public let platform: String
    public let env: String
    public let appVersion: String
    public let buildNumber: String
    public let gitSHA: String?
    public let symbolID: String?
    public let adapter: String
    public let sdkName: String
    public let sdkVersion: String
    public let installHash: String
    public let userHash: String?
    public let osVersion: String?
    public let deviceModel: String?

    enum CodingKeys: String, CodingKey {
        case appID = "app_id"
        case platform
        case env
        case appVersion = "app_version"
        case buildNumber = "build_number"
        case gitSHA = "git_sha"
        case symbolID = "symbol_id"
        case adapter
        case sdkName = "sdk_name"
        case sdkVersion = "sdk_version"
        case installHash = "install_hash"
        case userHash = "user_hash"
        case osVersion = "os_version"
        case deviceModel = "device_model"
    }

    public init(
        appID: String,
        platform: String = "ios",
        env: String = "prod",
        appVersion: String,
        buildNumber: String,
        gitSHA: String? = nil,
        symbolID: String? = nil,
        adapter: String = "native",
        sdkName: String = "caliper-ios",
        sdkVersion: String = "1.1.0",
        installHash: String,
        userHash: String? = nil,
        osVersion: String? = nil,
        deviceModel: String? = nil
    ) {
        self.appID = appID
        self.platform = platform
        self.env = env
        self.appVersion = appVersion
        self.buildNumber = buildNumber
        self.gitSHA = gitSHA
        self.symbolID = symbolID
        self.adapter = adapter
        self.sdkName = sdkName
        self.sdkVersion = sdkVersion
        self.installHash = installHash
        self.userHash = userHash
        self.osVersion = osVersion
        self.deviceModel = deviceModel
    }
}

public struct TelemetryEvent: Codable, Sendable {
    public let eventID: String
    public let kind: String
    public let occurredAtMS: Int64
    public let contractVersion: Int
    public let scene: String?
    public let windowID: String?
    public let policyID: String?
    public let sampleRatePPM: Int?
    public let payload: EventPayload

    enum CodingKeys: String, CodingKey {
        case eventID = "event_id"
        case kind
        case occurredAtMS = "occurred_at_ms"
        case contractVersion = "contract_version"
        case scene
        case windowID = "window_id"
        case policyID = "policy_id"
        case sampleRatePPM = "sample_rate_ppm"
        case payload
    }

    public init(
        eventID: String = UUID().uuidString.lowercased(),
        kind: String,
        occurredAtMS: Int64,
        contractVersion: Int = 1,
        scene: String? = nil,
        windowID: String? = nil,
        policyID: String? = nil,
        sampleRatePPM: Int? = nil,
        payload: EventPayload
    ) {
        self.eventID = eventID
        self.kind = kind
        self.occurredAtMS = occurredAtMS
        self.contractVersion = contractVersion
        self.scene = scene
        self.windowID = windowID
        self.policyID = policyID
        self.sampleRatePPM = sampleRatePPM
        self.payload = payload
    }
}

public enum EventPayload: Codable, Sendable {
    case startup(StartupPayload)
    case stability(StabilityPayload)
    case fluency(FluencyWindowPayload)
    case resource(ResourceWindowPayload)
    case jsLongTask(JSLongTaskPayload)
    case networkSummary(NetworkSummaryPayload)
    case networkTransport(NetworkTransportPayload)
    case customScene(CustomScenePayload)

    public func encode(to encoder: Encoder) throws {
        switch self {
        case .startup(let v): try v.encode(to: encoder)
        case .stability(let v): try v.encode(to: encoder)
        case .fluency(let v): try v.encode(to: encoder)
        case .resource(let v): try v.encode(to: encoder)
        case .jsLongTask(let v): try v.encode(to: encoder)
        case .networkSummary(let v): try v.encode(to: encoder)
        case .networkTransport(let v): try v.encode(to: encoder)
        case .customScene(let v): try v.encode(to: encoder)
        }
    }

    public init(from decoder: Decoder) throws {
        if let v = try? StartupPayload(from: decoder) { self = .startup(v); return }
        if let v = try? FluencyWindowPayload(from: decoder) { self = .fluency(v); return }
        if let v = try? ResourceWindowPayload(from: decoder) { self = .resource(v); return }
        if let v = try? NetworkTransportPayload(from: decoder) { self = .networkTransport(v); return }
        if let v = try? NetworkSummaryPayload(from: decoder) { self = .networkSummary(v); return }
        if let v = try? JSLongTaskPayload(from: decoder) { self = .jsLongTask(v); return }
        if let v = try? StabilityPayload(from: decoder) { self = .stability(v); return }
        self = .customScene(try CustomScenePayload(from: decoder))
    }
}

public struct StartupPayload: Codable, Sendable {
    public let totalDurationMS: Double
    public let nativeInitMS: Double?
    public let runtimeInitMS: Double?
    public let firstFrameMS: Double?
    public let coldStart: Bool
    public let startAnchor: String
    public let endAnchor: String
    public let result: String

    enum CodingKeys: String, CodingKey {
        case totalDurationMS = "total_duration_ms"
        case nativeInitMS = "native_init_ms"
        case runtimeInitMS = "runtime_init_ms"
        case firstFrameMS = "first_frame_ms"
        case coldStart = "cold_start"
        case startAnchor = "start_anchor"
        case endAnchor = "end_anchor"
        case result
    }

    public init(totalDurationMS: Double, nativeInitMS: Double? = nil, runtimeInitMS: Double? = nil, firstFrameMS: Double? = nil, coldStart: Bool = true, startAnchor: String = "process_main", endAnchor: String = "first_content_frame", result: String = "success") {
        self.totalDurationMS = totalDurationMS
        self.nativeInitMS = nativeInitMS
        self.runtimeInitMS = runtimeInitMS
        self.firstFrameMS = firstFrameMS
        self.coldStart = coldStart
        self.startAnchor = startAnchor
        self.endAnchor = endAnchor
        self.result = result
    }
}

public struct StackFrame: Codable, Sendable {
    public let module: String?
    public let symbol: String
    public let file: String?
    public let line: Int?
    public let address: String?
    public let inApp: Bool?

    enum CodingKeys: String, CodingKey {
        case module, symbol, file, line, address
        case inApp = "in_app"
    }

    public init(module: String? = nil, symbol: String, file: String? = nil, line: Int? = nil, address: String? = nil, inApp: Bool? = nil) {
        self.module = module
        self.symbol = symbol
        self.file = file
        self.line = line
        self.address = address
        self.inApp = inApp
    }
}

public struct Breadcrumb: Codable, Sendable {
    public let timestampMS: Int64
    public let category: String
    public let message: String

    enum CodingKeys: String, CodingKey {
        case timestampMS = "timestamp_ms"
        case category, message
    }

    public init(timestampMS: Int64, category: String, message: String) {
        self.timestampMS = timestampMS
        self.category = category
        self.message = message
    }
}

public struct StabilityPayload: Codable, Sendable {
    public let fingerprint: String
    public let title: String
    public let fatal: Bool
    public let exceptionType: String?
    public let threadName: String?
    public let exitReason: String?
    public let frames: [StackFrame]
    public let breadcrumbs: [Breadcrumb]?

    enum CodingKeys: String, CodingKey {
        case fingerprint, title, fatal, frames, breadcrumbs
        case exceptionType = "exception_type"
        case threadName = "thread_name"
        case exitReason = "exit_reason"
    }

    public init(fingerprint: String, title: String, fatal: Bool, exceptionType: String? = nil, threadName: String? = nil, exitReason: String? = nil, frames: [StackFrame], breadcrumbs: [Breadcrumb]? = nil) {
        self.fingerprint = fingerprint
        self.title = title
        self.fatal = fatal
        self.exceptionType = exceptionType
        self.threadName = threadName
        self.exitReason = exitReason
        self.frames = frames
        self.breadcrumbs = breadcrumbs
    }
}

public struct FluencyWindowPayload: Codable, Sendable {
    public let algorithm: String
    public let windowDurationMS: Int64
    public let frameCount: Int64
    public let jankFrameCount: Int64
    public let severeJankFrameCount: Int64?
    public let refreshRateHz: Double?
    public let frameDurationBuckets: [Int64]?
    public let stallStack: [String]?

    enum CodingKeys: String, CodingKey {
        case algorithm
        case windowDurationMS = "window_duration_ms"
        case frameCount = "frame_count"
        case jankFrameCount = "jank_frame_count"
        case severeJankFrameCount = "severe_jank_frame_count"
        case refreshRateHz = "refresh_rate_hz"
        case frameDurationBuckets = "frame_duration_buckets"
        case stallStack = "stall_stack"
    }

    public init(algorithm: String = "ios_display_link", windowDurationMS: Int64, frameCount: Int64, jankFrameCount: Int64, severeJankFrameCount: Int64? = nil, refreshRateHz: Double? = nil, frameDurationBuckets: [Int64]? = nil, stallStack: [String]? = nil) {
        self.algorithm = algorithm
        self.windowDurationMS = windowDurationMS
        self.frameCount = frameCount
        self.jankFrameCount = jankFrameCount
        self.severeJankFrameCount = severeJankFrameCount
        self.refreshRateHz = refreshRateHz
        self.frameDurationBuckets = frameDurationBuckets
        self.stallStack = stallStack
    }
}

public struct ResourceWindowPayload: Codable, Sendable {
    public let windowDurationMS: Int64
    public let cpuPct: Double
    public let memoryKind: String
    public let memoryPeakKiB: Double
    public let memoryAvgKiB: Double?
    public let sampleCount: Int

    enum CodingKeys: String, CodingKey {
        case windowDurationMS = "window_duration_ms"
        case cpuPct = "cpu_pct"
        case memoryKind = "memory_kind"
        case memoryPeakKiB = "memory_peak_kib"
        case memoryAvgKiB = "memory_avg_kib"
        case sampleCount = "sample_count"
    }

    public init(windowDurationMS: Int64, cpuPct: Double, memoryKind: String = "ios_phys_footprint_kib", memoryPeakKiB: Double, memoryAvgKiB: Double? = nil, sampleCount: Int) {
        self.windowDurationMS = windowDurationMS
        self.cpuPct = cpuPct
        self.memoryKind = memoryKind
        self.memoryPeakKiB = memoryPeakKiB
        self.memoryAvgKiB = memoryAvgKiB
        self.sampleCount = sampleCount
    }
}

public struct JSLongTaskPayload: Codable, Sendable {
    public let windowDurationMS: Int64
    public let longTaskCount: Int64
    public let totalBlockingMS: Double
    public let maxTaskMS: Double
    public let droppedEntries: Int64?

    enum CodingKeys: String, CodingKey {
        case windowDurationMS = "window_duration_ms"
        case longTaskCount = "longtask_count"
        case totalBlockingMS = "total_blocking_ms"
        case maxTaskMS = "max_task_ms"
        case droppedEntries = "dropped_entries"
    }

    public init(windowDurationMS: Int64, longTaskCount: Int64, totalBlockingMS: Double, maxTaskMS: Double, droppedEntries: Int64? = nil) {
        self.windowDurationMS = windowDurationMS
        self.longTaskCount = longTaskCount
        self.totalBlockingMS = totalBlockingMS
        self.maxTaskMS = maxTaskMS
        self.droppedEntries = droppedEntries
    }
}

public struct NetworkFailureCounts: Codable, Sendable {
    public var http: Int64 = 0
    public var business: Int64 = 0
    public var parse: Int64 = 0
    public var network: Int64 = 0
    public var timeout: Int64 = 0

    public init(http: Int64 = 0, business: Int64 = 0, parse: Int64 = 0, network: Int64 = 0, timeout: Int64 = 0) {
        self.http = http
        self.business = business
        self.parse = parse
        self.network = network
        self.timeout = timeout
    }
}

public struct NetworkSummaryPayload: Codable, Sendable {
    public let windowDurationMS: Int64
    public let callCount: Int64
    public let failureCounts: NetworkFailureCounts
    public let durationBuckets: [Int64]
    public let durationSumMS: Double

    enum CodingKeys: String, CodingKey {
        case windowDurationMS = "window_duration_ms"
        case callCount = "call_count"
        case failureCounts = "failure_counts"
        case durationBuckets = "duration_buckets"
        case durationSumMS = "duration_sum_ms"
    }

    public init(windowDurationMS: Int64, callCount: Int64, failureCounts: NetworkFailureCounts, durationBuckets: [Int64], durationSumMS: Double) {
        self.windowDurationMS = windowDurationMS
        self.callCount = callCount
        self.failureCounts = failureCounts
        self.durationBuckets = durationBuckets
        self.durationSumMS = durationSumMS
    }
}

public struct NetworkTransportPayload: Codable, Sendable {
    public let operation: String
    public let method: String
    public let statusCode: Int?
    public let result: String
    public let dnsMS: Double?
    public let tcpMS: Double?
    public let tlsMS: Double?
    public let ttfbMS: Double?
    public let bodyMS: Double?
    public let totalMS: Double
    public let connectionReused: Bool?
    public let protocolName: String?

    enum CodingKeys: String, CodingKey {
        case operation, method, result
        case statusCode = "status_code"
        case dnsMS = "dns_ms"
        case tcpMS = "tcp_ms"
        case tlsMS = "tls_ms"
        case ttfbMS = "ttfb_ms"
        case bodyMS = "body_ms"
        case totalMS = "total_ms"
        case connectionReused = "connection_reused"
        case protocolName = "protocol"
    }

    public init(operation: String, method: String, statusCode: Int? = nil, result: String, dnsMS: Double? = nil, tcpMS: Double? = nil, tlsMS: Double? = nil, ttfbMS: Double? = nil, bodyMS: Double? = nil, totalMS: Double, connectionReused: Bool? = nil, protocolName: String? = nil) {
        self.operation = operation
        self.method = method
        self.statusCode = statusCode
        self.result = result
        self.dnsMS = dnsMS
        self.tcpMS = tcpMS
        self.tlsMS = tlsMS
        self.ttfbMS = ttfbMS
        self.bodyMS = bodyMS
        self.totalMS = totalMS
        self.connectionReused = connectionReused
        self.protocolName = protocolName
    }
}

public struct CustomScenePayload: Codable, Sendable {
    public let journeyName: String
    public let status: String
    public let durationMS: Double?
    public let errorCode: String?

    enum CodingKeys: String, CodingKey {
        case journeyName = "journey_name"
        case status
        case durationMS = "duration_ms"
        case errorCode = "error_code"
    }

    public init(journeyName: String, status: String, durationMS: Double? = nil, errorCode: String? = nil) {
        self.journeyName = journeyName
        self.status = status
        self.durationMS = durationMS
        self.errorCode = errorCode
    }
}

/// Bounded ring buffer for pre-fault breadcrumbs (never unbounded memory).
public struct BreadcrumbRingBuffer: Sendable {
    private let capacity: Int
    private var items: [Breadcrumb] = []

    public init(capacity: Int = 32) {
        self.capacity = max(1, capacity)
    }

    public mutating func append(_ crumb: Breadcrumb) {
        if items.count >= capacity {
            items.removeFirst(items.count - capacity + 1)
        }
        items.append(crumb)
    }

    public func snapshot() -> [Breadcrumb] {
        items
    }
}
