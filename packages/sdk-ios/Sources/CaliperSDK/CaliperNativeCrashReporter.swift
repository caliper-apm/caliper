import Foundation
import CaliperCrashCore

/// Parsed representation of an async-signal-safe native crash tombstone written before process death.
public struct NativeCrashReport: Sendable {
    public let mechanism: String   // "mach" | "signal"
    public let exception: String   // e.g. "EXC_BAD_ACCESS" | "SIGSEGV"
    public let code: Int64
    public let faultAddress: String
    public let timestampMS: Int64
    public let scene: String
    public let frames: [StackFrame]
    public let binaryUUIDs: [String]
}

/// Real-time iOS/macOS C/Mach Exception + Unix Signal Crash Reporter.
///
/// Captures crashes immediately on-device without waiting for Apple's 24h MetricKit delay,
/// persists slide-subtracted relative hex frames (`0x...`) + Mach-O image UUIDs via async-signal-safe
/// POSIX I/O, and ingests them on next launch for automatic server-side Mach-O DWARF symbolication.
public final class CaliperNativeCrashReporter: @unchecked Sendable {
    private let directoryURL: URL

    public init(directoryURL: URL) {
        self.directoryURL = directoryURL
        try? FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)
    }

    /// Installs Mach exception + Unix signal handlers (with 64 KiB sigaltstack) and immediately
    /// ingests any pending crash tombstones from the previous run into `client`.
    /// Returns the number of crash reports discovered and enqueued.
    @discardableResult
    public func install(
        client: CaliperClient,
        enableMachHandler: Bool = true,
        scene: String = "unknown"
    ) -> Int {
        let ingested = ingestPendingReports(into: client)
        caliper_crash_set_scene(scene)
        _ = caliper_crash_install(directoryURL.path, enableMachHandler)
        return ingested
    }

    public func setScene(_ scene: String) {
        caliper_crash_set_scene(scene)
    }

    public func uninstall() {
        caliper_crash_uninstall()
    }

    /// Scans `directoryURL` for `.tombstone` files left by a prior crash, enqueues them into `client`
    /// with `bypassSampling: true`, and removes consumed tombstones.
    @discardableResult
    public func ingestPendingReports(into client: CaliperClient) -> Int {
        let files = pendingTombstoneFiles()
        var count = 0
        for fileURL in files {
            defer { try? FileManager.default.removeItem(at: fileURL) }
            guard let raw = try? String(contentsOf: fileURL, encoding: .utf8),
                  let report = Self.parseTombstone(raw) else {
                continue
            }
            let topAddr = report.frames.first?.address ?? report.faultAddress
            let topMod = report.frames.first?.module ?? "app"
            let fp = "ios-native-\(report.mechanism)-\(report.exception)-\(topMod)-\(topAddr)"
            let title = "Native \(report.mechanism.uppercased()) Crash: \(report.exception) (fault=\(report.faultAddress))"
            client.enqueue(
                kind: "crash",
                payload: .stability(StabilityPayload(
                    fingerprint: fp,
                    title: title,
                    fatal: true,
                    exceptionType: report.exception,
                    threadName: "crashed-thread",
                    frames: report.frames
                )),
                scene: report.scene,
                occurredAtMS: report.timestampMS,
                bypassSampling: true
            )
            count += 1
        }
        return count
    }

    public func pendingTombstoneFiles() -> [URL] {
        let items = (try? FileManager.default.contentsOfDirectory(at: directoryURL, includingPropertiesForKeys: nil)) ?? []
        return items
            .filter { $0.lastPathComponent.hasSuffix(".tombstone") }
            .sorted { $0.lastPathComponent < $1.lastPathComponent }
    }

    public static func parseTombstone(_ raw: String) -> NativeCrashReport? {
        var mechanism = "signal"
        var exception = ""
        var code: Int64 = 0
        var faultAddr = "0x0"
        var tsMS: Int64 = Int64(Date().timeIntervalSince1970 * 1000)
        var scene = "unknown"
        var frames: [StackFrame] = []
        var uuids = Set<String>()

        for line in raw.split(separator: "\n", omittingEmptySubsequences: true) {
            guard let eq = line.firstIndex(of: "=") else { continue }
            let key = String(line[..<eq])
            let val = String(line[line.index(after: eq)...])
            switch key {
            case "mechanism":
                mechanism = val
            case "exception":
                exception = val
            case "code":
                code = Int64(val) ?? 0
            case "fault_addr":
                faultAddr = val
            case "timestamp_ms":
                tsMS = Int64(val) ?? tsMS
            case "scene":
                scene = val.isEmpty ? "unknown" : val
            case "frame":
                // Format: <module>|<uuid>|<rel_hex>|<abs_hex>
                let parts = val.split(separator: "|", omittingEmptySubsequences: false).map(String.init)
                if parts.count >= 3 {
                    let mod = parts[0]
                    let uuid = parts[1]
                    let relHex = parts[2]
                    if !uuid.isEmpty { uuids.insert(uuid) }
                    frames.append(StackFrame(
                        module: mod,
                        symbol: relHex,
                        address: relHex,
                        inApp: !mod.hasPrefix("libsystem_") && !mod.hasPrefix("CoreFoundation") && !mod.hasPrefix("dyld")
                    ))
                }
            default:
                break
            }
        }
        guard !exception.isEmpty else { return nil }
        return NativeCrashReport(
            mechanism: mechanism,
            exception: exception,
            code: code,
            faultAddress: faultAddr,
            timestampMS: tsMS,
            scene: scene,
            frames: frames,
            binaryUUIDs: uuids.sorted()
        )
    }
}
