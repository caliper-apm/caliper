import Foundation
import CaliperCrashCore

/// Classification of the previous app session by the Zero-Lie Heuristic FOOM State Machine.
public enum FOOMTerminationClass: String, Sendable, Equatable {
    case none             // First launch (no prior state file)
    case cleanExit        // applicationWillTerminate ran cleanly
    case detectedCrash    // Crash tombstone or uncaught exception already attributed
    case debuggerSession  // Attached to Xcode / LLDB (P_TRACED)
    case appUpgraded      // app_version or build_number changed between launches
    case osUpgraded       // iOS/macOS version changed between launches
    case deviceRebooted   // kern.boottime changed (reboot / battery drain)
    case backgroundKill   // App was backgrounded (normal Jetsam eviction, NOT FOOM)
    case foregroundOOM    // Unexplained foreground termination -> Foreground OOM!
}

public struct FOOMSessionState: Codable, Sendable, Equatable {
    public var appVersion: String
    public var buildNumber: String
    public var osVersion: String
    public var bootTimeSec: Int64
    public var lifecycleState: String // "foreground" | "background" | "terminated_clean"
    public var detectedCrash: Bool
    public var debuggerAttached: Bool
    public var scene: String
    public var lastMemoryFootprintKiB: Double
    public var updatedAtMS: Int64

    public init(
        appVersion: String,
        buildNumber: String,
        osVersion: String,
        bootTimeSec: Int64,
        lifecycleState: String,
        detectedCrash: Bool,
        debuggerAttached: Bool,
        scene: String,
        lastMemoryFootprintKiB: Double,
        updatedAtMS: Int64
    ) {
        self.appVersion = appVersion
        self.buildNumber = buildNumber
        self.osVersion = osVersion
        self.bootTimeSec = bootTimeSec
        self.lifecycleState = lifecycleState
        self.detectedCrash = detectedCrash
        self.debuggerAttached = debuggerAttached
        self.scene = scene
        self.lastMemoryFootprintKiB = lastMemoryFootprintKiB
        self.updatedAtMS = updatedAtMS
    }
}

/// Zero-Lie Heuristic Foreground Out-Of-Memory (FOOM) Detector for iOS/macOS.
///
/// Follows the strict exclusion state machine used in industry-grade mobile APMs:
/// only classifies a prior termination as `FOOM_HEURISTIC` when clean exit, detected crash,
/// debugger attachment, app/OS upgrade, device reboot, and background Jetsam are ALL ruled out.
public final class CaliperFOOMDetector: @unchecked Sendable {
    private let stateFileURL: URL
    private let lock = NSLock()
    private var currentState: FOOMSessionState?

    public init(directoryURL: URL) {
        try? FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)
        self.stateFileURL = directoryURL.appendingPathComponent("caliper_foom_state.json")
    }

    /// Evaluates the prior session's persisted state, emits an `abnormal_exit` (`FOOM_HEURISTIC`)
    /// event if and only if all 8 non-FOOM exclusions are false, and initializes the new session marker.
    @discardableResult
    public func startSession(
        client: CaliperClient,
        appVersion: String,
        buildNumber: String,
        osVersion: String,
        previousCrashDetected: Bool,
        bootTimeSec: Int64 = caliper_boot_time_sec(),
        debuggerAttached: Bool = caliper_is_debugger_attached(),
        initialScene: String = "startup"
    ) -> FOOMTerminationClass {
        lock.lock()
        defer { lock.unlock() }

        let classification = Self.classify(
            previous: loadStateLocked(),
            currentAppVersion: appVersion,
            currentBuildNumber: buildNumber,
            currentOSVersion: osVersion,
            currentBootTimeSec: bootTimeSec,
            previousCrashDetected: previousCrashDetected
        )

        if classification == .foregroundOOM, let prev = loadStateLocked() {
            let memMiB = String(format: "%.1f", prev.lastMemoryFootprintKiB / 1024.0)
            client.enqueue(
                kind: "abnormal_exit",
                payload: .stability(StabilityPayload(
                    fingerprint: "ios-foom-\(prev.scene)",
                    title: "Foreground OOM (FOOM_HEURISTIC, last_footprint=\(memMiB) MiB, scene=\(prev.scene))",
                    fatal: true,
                    exitReason: "FOOM_HEURISTIC",
                    processImportance: "foreground",
                    frames: []
                )),
                scene: prev.scene,
                occurredAtMS: prev.updatedAtMS,
                bypassSampling: true
            )
        }

        let next = FOOMSessionState(
            appVersion: appVersion,
            buildNumber: buildNumber,
            osVersion: osVersion,
            bootTimeSec: bootTimeSec,
            lifecycleState: "foreground",
            detectedCrash: false,
            debuggerAttached: debuggerAttached,
            scene: initialScene,
            lastMemoryFootprintKiB: CaliperSystemMetrics.physicalFootprintKiB() ?? 0,
            updatedAtMS: Int64(Date().timeIntervalSince1970 * 1000)
        )
        currentState = next
        saveStateLocked(next)
        return classification
    }

    public func markForeground(scene: String? = nil) {
        mutateState { st in
            st.lifecycleState = "foreground"
            if let s = scene, !s.isEmpty { st.scene = s }
            if let fp = CaliperSystemMetrics.physicalFootprintKiB() {
                st.lastMemoryFootprintKiB = fp
            }
        }
    }

    public func markBackground() {
        mutateState { st in
            st.lifecycleState = "background"
            if let fp = CaliperSystemMetrics.physicalFootprintKiB() {
                st.lastMemoryFootprintKiB = fp
            }
        }
    }

    public func markCleanTerminate() {
        mutateState { st in
            st.lifecycleState = "terminated_clean"
        }
    }

    public func markCrashDetected() {
        mutateState { st in
            st.detectedCrash = true
        }
    }

    public func updateMemoryFootprint(kib: Double, scene: String? = nil) {
        mutateState { st in
            st.lastMemoryFootprintKiB = max(0, kib)
            if let s = scene, !s.isEmpty { st.scene = s }
        }
    }

    /// Pure deterministic FOOM classifier (exposed for exhaustive unit testing).
    public static func classify(
        previous: FOOMSessionState?,
        currentAppVersion: String,
        currentBuildNumber: String,
        currentOSVersion: String,
        currentBootTimeSec: Int64,
        previousCrashDetected: Bool
    ) -> FOOMTerminationClass {
        guard let prev = previous else { return .none }
        if prev.lifecycleState == "terminated_clean" { return .cleanExit }
        if prev.detectedCrash || previousCrashDetected { return .detectedCrash }
        if prev.debuggerAttached { return .debuggerSession }
        if prev.appVersion != currentAppVersion || prev.buildNumber != currentBuildNumber { return .appUpgraded }
        if prev.osVersion != currentOSVersion { return .osUpgraded }
        if prev.bootTimeSec > 0 && currentBootTimeSec > 0 && abs(prev.bootTimeSec - currentBootTimeSec) > 5 {
            return .deviceRebooted
        }
        if prev.lifecycleState == "background" { return .backgroundKill }
        return .foregroundOOM
    }

    private func mutateState(_ block: (inout FOOMSessionState) -> Void) {
        lock.lock()
        defer { lock.unlock() }
        guard var st = currentState else { return }
        block(&st)
        st.updatedAtMS = Int64(Date().timeIntervalSince1970 * 1000)
        currentState = st
        saveStateLocked(st)
    }

    private func loadStateLocked() -> FOOMSessionState? {
        guard let data = try? Data(contentsOf: stateFileURL) else { return nil }
        return try? JSONDecoder().decode(FOOMSessionState.self, from: data)
    }

    private func saveStateLocked(_ st: FOOMSessionState) {
        if let data = try? JSONEncoder().encode(st) {
            try? data.write(to: stateFileURL, options: .atomic)
        }
    }
}
