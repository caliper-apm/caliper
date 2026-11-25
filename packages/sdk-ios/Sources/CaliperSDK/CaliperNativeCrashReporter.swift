import Foundation
import CaliperCrashCore

/// Shared classifier distinguishing application frames from Apple OS/runtime frameworks.
public enum CaliperModuleClassifier {
    private static let systemPrefixes: [String] = [
        "libsystem_", "libobjc", "libdispatch", "libswift", "libdyld", "libc++",
        "libxpc", "libCore", "libAudio", "dyld",
        "CoreFoundation", "Foundation", "UIKit", "SwiftUI", "QuartzCore",
        "GraphicsServices", "CFNetwork", "FrontBoardServices", "RunningBoardServices",
        "AttributeGraph", "CoreGraphics", "CoreAnimation", "AppKit", "HIToolbox",
        "Security", "Metal", "WebKit", "JavaScriptCore", "MetricKit", "XCTest",
    ]

    public static func isInAppModule(_ module: String) -> Bool {
        let trimmed = module.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty || trimmed == "unknown" {
            return false
        }
        for prefix in systemPrefixes {
            if trimmed.hasPrefix(prefix) {
                return false
            }
        }
        return true
    }
}

/// Parsed representation of an async-signal-safe native crash tombstone written before process death.
public struct NativeCrashReport: Sendable {
    public let mechanism: String   // "mach" | "signal" | "nsexception"
    public let exception: String   // e.g. "EXC_BAD_ACCESS" | "SIGSEGV" | "NSRangeException"
    public let reason: String?
    public let code: Int64
    public let faultAddress: String
    public let timestampMS: Int64
    public let scene: String
    public let frames: [StackFrame]
    public let binaryUUIDs: [String]

    public init(
        mechanism: String,
        exception: String,
        reason: String? = nil,
        code: Int64,
        faultAddress: String,
        timestampMS: Int64,
        scene: String,
        frames: [StackFrame],
        binaryUUIDs: [String]
    ) {
        self.mechanism = mechanism
        self.exception = exception
        self.reason = reason
        self.code = code
        self.faultAddress = faultAddress
        self.timestampMS = timestampMS
        self.scene = scene
        self.frames = frames
        self.binaryUUIDs = binaryUUIDs
    }
}

private let caliperNSExceptionLock = NSLock()
private nonisolated(unsafe) var caliperPreviousNSExceptionHandler: (@convention(c) (NSException) -> Void)?
private nonisolated(unsafe) var caliperNSExceptionHandlerInstalled = false

private func caliperUncaughtNSExceptionHandler(_ exception: NSException) {
    let pcs = exception.callStackReturnAddresses.map { UInt(truncating: $0) }
    _ = CaliperNativeCrashReporter.writeNSExceptionTombstone(
        name: exception.name.rawValue,
        reason: exception.reason,
        returnAddresses: pcs
    )

    caliperNSExceptionLock.lock()
    let prev = caliperPreviousNSExceptionHandler
    caliperNSExceptionLock.unlock()
    prev?(exception)
}

/// Real-time iOS/macOS C/Mach Exception + Unix Signal + Objective-C NSException Crash Reporter.
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

    /// Installs Mach exception + Unix signal handlers (with 64 KiB sigaltstack and vm_read_overwrite
    /// stack-walk protection), chains `NSSetUncaughtExceptionHandler`, and immediately ingests any
    /// pending crash tombstones from the previous run into `client`.
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
        Self.installNSExceptionHandlerIfNeeded()
        return ingested
    }

    public func setScene(_ scene: String) {
        caliper_crash_set_scene(scene)
    }

    public func uninstall() {
        Self.uninstallNSExceptionHandlerIfNeeded()
        caliper_crash_uninstall()
    }

    /// Writes a deduplicated `nsexception` tombstone directly through `CaliperCrashCore`.
    /// Returns `0` if written, `1` if a tombstone was already written for this process, or `<0` on error.
    @discardableResult
    public static func writeNSExceptionTombstone(
        name: String,
        reason: String?,
        returnAddresses: [UInt]
    ) -> Int32 {
        return name.withCString { namePtr in
            let writeWithReason: (UnsafePointer<CChar>?) -> Int32 = { reasonPtr in
                if returnAddresses.isEmpty {
                    return caliper_crash_write_exception_tombstone(namePtr, reasonPtr, nil, 0)
                }
                return returnAddresses.withUnsafeBufferPointer { buf in
                    caliper_crash_write_exception_tombstone(namePtr, reasonPtr, buf.baseAddress, Int32(buf.count))
                }
            }
            if let reason, !reason.isEmpty {
                return reason.withCString { writeWithReason($0) }
            }
            return writeWithReason(nil)
        }
    }

    private static func installNSExceptionHandlerIfNeeded() {
        caliperNSExceptionLock.lock()
        defer { caliperNSExceptionLock.unlock() }
        guard !caliperNSExceptionHandlerInstalled else { return }
        caliperPreviousNSExceptionHandler = NSGetUncaughtExceptionHandler()
        NSSetUncaughtExceptionHandler(caliperUncaughtNSExceptionHandler)
        caliperNSExceptionHandlerInstalled = true
    }

    private static func uninstallNSExceptionHandlerIfNeeded() {
        caliperNSExceptionLock.lock()
        defer { caliperNSExceptionLock.unlock() }
        guard caliperNSExceptionHandlerInstalled else { return }
        NSSetUncaughtExceptionHandler(caliperPreviousNSExceptionHandler)
        caliperPreviousNSExceptionHandler = nil
        caliperNSExceptionHandlerInstalled = false
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
            let topFrame = report.frames.first(where: { $0.inApp == true }) ?? report.frames.first
            let topAddr = topFrame?.address ?? report.faultAddress
            let topMod = topFrame?.module ?? "app"
            let fp = "ios-native-\(report.mechanism)-\(report.exception)-\(topMod)-\(topAddr)"
            let title: String
            if let reason = report.reason, !reason.isEmpty {
                title = String("\(report.exception): \(reason)".prefix(240))
            } else {
                title = "Native \(report.mechanism.uppercased()) Crash: \(report.exception) (fault=\(report.faultAddress))"
            }
            client.enqueue(
                kind: "crash",
                payload: .stability(StabilityPayload(
                    fingerprint: fp,
                    title: title,
                    fatal: true,
                    exceptionType: report.exception,
                    threadName: report.mechanism == "nsexception" ? "main" : "crashed-thread",
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
        var reason: String? = nil
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
            case "reason":
                if !val.isEmpty { reason = val }
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
                        inApp: CaliperModuleClassifier.isInAppModule(mod)
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
            reason: reason,
            code: code,
            faultAddress: faultAddr,
            timestampMS: tsMS,
            scene: scene,
            frames: frames,
            binaryUUIDs: uuids.sorted()
        )
    }
}
