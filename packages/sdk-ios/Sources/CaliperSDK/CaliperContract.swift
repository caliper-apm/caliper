import Foundation

/// Zero-Lie cross-platform contract primitives (byte-for-byte parity with contract-vectors.json).
public enum CaliperContract {
    public static let maxSamplingRatePPM: UInt32 = 1_000_000
    public static let histogramBucketCount = 19

    public static let durationBoundsMS: [Double] = [
        16, 33, 50, 100, 250, 500, 750, 1000, 1500, 2000,
        3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000,
    ]

    public static let memoryBoundsMiB: [Double] = [
        64, 96, 128, 160, 192, 224, 256, 320, 384, 448,
        512, 640, 768, 896, 1024, 1536, 2048, 4096,
    ]

    /// Unsigned 32-bit FNV-1a over ASCII bytes.
    public static func fnv1a32(_ input: String) -> UInt32 {
        var hash: UInt32 = 2_166_136_261
        for byte in input.utf8 {
            hash ^= UInt32(byte)
            hash = hash &* 16_777_619
        }
        return hash
    }

    public static func samplingBucketPPM(installSeed: String, policyID: String) -> UInt32 {
        fnv1a32("\(installSeed)|\(policyID)") % maxSamplingRatePPM
    }

    public static func isAdmitted(installSeed: String, policyID: String, ratePPM: UInt32) -> Bool {
        if ratePPM == 0 { return false }
        if ratePPM >= maxSamplingRatePPM { return true }
        return samplingBucketPPM(installSeed: installSeed, policyID: policyID) < ratePPM
    }

    public static func durationBucketIndex(_ ms: Double) -> Int {
        bucketIndex(bounds: durationBoundsMS, value: ms)
    }

    public static func memoryBucketIndex(_ mib: Double) -> Int {
        bucketIndex(bounds: memoryBoundsMiB, value: mib)
    }

    private static func bucketIndex(bounds: [Double], value: Double) -> Int {
        guard value.isFinite, value >= 0 else { return bounds.count }
        for (idx, upper) in bounds.enumerated() where value <= upper {
            return idx
        }
        return bounds.count
    }

    public static func validateStartupStages(
        totalMS: Double,
        nativeInitMS: Double? = nil,
        runtimeInitMS: Double? = nil,
        firstFrameMS: Double? = nil
    ) -> Bool {
        guard totalMS.isFinite, totalMS >= 0 else { return false }
        var sum = 0.0
        for stage in [nativeInitMS, runtimeInitMS, firstFrameMS].compactMap({ $0 }) {
            guard stage.isFinite, stage >= 0 else { return false }
            sum += stage
        }
        return sum <= totalMS + 1e-6
    }
}

public struct FluencyAssessment: Equatable, Sendable {
    public let jankRatio: Double?
    public let severity: String

    public init(jankRatio: Double?, severity: String) {
        self.jankRatio = jankRatio
        self.severity = severity
    }
}

/// Accumulates frame intervals inside a bounded window; frameCount == 0 always yields unknown.
public struct FluencyWindowAccumulator: Sendable {
    public private(set) var frameCount: Int64 = 0
    public private(set) var jankFrameCount: Int64 = 0
    public private(set) var severeJankFrameCount: Int64 = 0
    public private(set) var frameDurationBuckets: [Int64] = Array(repeating: 0, count: CaliperContract.histogramBucketCount)

    public init() {}

    public mutating func recordFrame(durationMS: Double, targetMS: Double) {
        guard durationMS.isFinite, durationMS >= 0, targetMS.isFinite, targetMS > 0 else { return }
        frameCount += 1
        let idx = CaliperContract.durationBucketIndex(durationMS)
        frameDurationBuckets[idx] += 1
        if durationMS > targetMS * 1.5 {
            jankFrameCount += 1
        }
        if durationMS > targetMS * 3.0 {
            severeJankFrameCount += 1
        }
    }

    public func classify() -> FluencyAssessment {
        Self.classify(frameCount: frameCount, jankFrameCount: jankFrameCount)
    }

    public static func classify(frameCount: Int64, jankFrameCount: Int64) -> FluencyAssessment {
        guard frameCount > 0, jankFrameCount >= 0, jankFrameCount <= frameCount else {
            return FluencyAssessment(jankRatio: nil, severity: "unknown")
        }
        let ratio = Double(jankFrameCount) / Double(frameCount)
        let severity: String
        if ratio >= 0.10 {
            severity = "critical"
        } else if ratio >= 0.03 {
            severity = "degraded"
        } else {
            severity = "healthy"
        }
        return FluencyAssessment(jankRatio: ratio, severity: severity)
    }
}

/// Enforces rolling 24h (or arbitrary window) event cap per installation.
public struct SlidingQuota: Sendable {
    public let windowMS: Int64
    public let cap: Int
    private var timestamps: [Int64] = []

    public init(windowMS: Int64, cap: Int) {
        self.windowMS = windowMS
        self.cap = cap
    }

    public mutating func tryConsume(timestampMS: Int64) -> Bool {
        guard cap > 0, windowMS > 0 else { return false }
        let cutoff = timestampMS - windowMS
        timestamps.removeAll { $0 <= cutoff }
        guard timestamps.count < cap else { return false }
        timestamps.append(timestampMS)
        return true
    }
}

/// Fail-closed remote sampling policy lease.
public struct PolicyLease: Equatable, Sendable {
    public let policyID: String
    public let ratePPM: UInt32
    public let issuedAtMS: Int64
    public let leaseTTLMS: Int64
    public let maxEvents24h: Int
    public let enabledCollectors: Set<String>

    public init(
        policyID: String,
        ratePPM: UInt32,
        issuedAtMS: Int64,
        leaseTTLMS: Int64,
        maxEvents24h: Int,
        enabledCollectors: Set<String>
    ) {
        self.policyID = policyID
        self.ratePPM = ratePPM
        self.issuedAtMS = issuedAtMS
        self.leaseTTLMS = leaseTTLMS
        self.maxEvents24h = maxEvents24h
        self.enabledCollectors = enabledCollectors
    }

    public func isActive(nowMS: Int64, installSeed: String) -> Bool {
        guard leaseTTLMS > 0, nowMS >= issuedAtMS, nowMS - issuedAtMS < leaseTTLMS else {
            return false
        }
        return CaliperContract.isAdmitted(installSeed: installSeed, policyID: policyID, ratePPM: ratePPM)
    }
}

/// Aggregates logical API calls inside a single uninterrupted window with strict bucket conservation.
public struct NetworkSummaryAccumulator: Sendable {
    public private(set) var callCount: Int64 = 0
    public private(set) var failureCounts = NetworkFailureCounts()
    public private(set) var durationBuckets: [Int64] = Array(repeating: 0, count: CaliperContract.histogramBucketCount)
    public private(set) var durationSumMS: Double = 0

    public init() {}

    public mutating func recordCall(durationMS: Double, failureDomain: String? = nil) {
        guard durationMS.isFinite, durationMS >= 0, durationMS <= 600_000 else { return }
        callCount += 1
        durationSumMS += durationMS
        durationBuckets[CaliperContract.durationBucketIndex(durationMS)] += 1
        switch failureDomain {
        case "http": failureCounts.http += 1
        case "business": failureCounts.business += 1
        case "parse": failureCounts.parse += 1
        case "network": failureCounts.network += 1
        case "timeout": failureCounts.timeout += 1
        default: break
        }
    }
}
