import Foundation
import CaliperCrashCore

/// Bounded FIFO offline disk queue for surviving iOS OOM kills and offline sessions.
public final class CaliperDiskQueue: @unchecked Sendable {
    private let dirURL: URL
    private let maxFiles: Int
    private let maxTotalBytes: Int
    private let lock = NSLock()
    private var seq: UInt64 = 0

    public init(directoryURL: URL, maxFiles: Int = 20, maxTotalBytes: Int = 2 * 1024 * 1024) {
        self.dirURL = directoryURL
        self.maxFiles = max(1, maxFiles)
        self.maxTotalBytes = max(4096, maxTotalBytes)
        try? FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)
    }

    @discardableResult
    public func enqueue(_ data: Data) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard data.count <= maxTotalBytes else { return false }
        try? FileManager.default.createDirectory(at: dirURL, withIntermediateDirectories: true)
        seq += 1
        let name = String(format: "%013lld-%06llu.batch.json", Int64(Date().timeIntervalSince1970 * 1000), seq)
        let target = dirURL.appendingPathComponent(name)
        do {
            try data.write(to: target, options: .atomic)
            evictLocked()
            return true
        } catch {
            return false
        }
    }

    public func count() -> Int {
        lock.lock()
        defer { lock.unlock() }
        return sortedFilesLocked().count
    }

    @discardableResult
    public func drainEach(_ sender: (Data) throws -> Void) -> Int {
        lock.lock()
        defer { lock.unlock() }
        var sent = 0
        for fileURL in sortedFilesLocked() {
            guard let data = try? Data(contentsOf: fileURL) else { continue }
            do {
                try sender(data)
                try? FileManager.default.removeItem(at: fileURL)
                sent += 1
            } catch {
                break
            }
        }
        return sent
    }

    private func sortedFilesLocked() -> [URL] {
        let items = (try? FileManager.default.contentsOfDirectory(at: dirURL, includingPropertiesForKeys: [.fileSizeKey])) ?? []
        return items
            .filter { $0.lastPathComponent.hasSuffix(".batch.json") }
            .sorted { $0.lastPathComponent < $1.lastPathComponent }
    }

    private func evictLocked() {
        var files = sortedFilesLocked()
        var total = files.reduce(0) { sum, url in
            let sz = (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
            return sum + sz
        }
        while files.count > maxFiles || total > maxTotalBytes {
            let oldest = files.removeFirst()
            let sz = (try? oldest.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
            total -= sz
            try? FileManager.default.removeItem(at: oldest)
        }
    }
}

/// PII-scrubbed diagnostic ring log buffer ready for audited remote pull tasks.
public final class CaliperDiagnosticRingLog: @unchecked Sendable {
    private let maxLines: Int
    private let lock = NSLock()
    private var lines: [String] = []

    public init(maxLines: Int = 500) {
        self.maxLines = max(10, maxLines)
    }

    public func append(level: String, tag: String, message: String) {
        let scrubbed = Self.scrubPII(message)
        let ts = Int64(Date().timeIntervalSince1970 * 1000)
        let line = "\(ts) [\(level)] \(tag): \(scrubbed)"
        lock.lock()
        if lines.count >= maxLines {
            lines.removeFirst()
        }
        lines.append(line)
        lock.unlock()
    }

    public func dumpText() -> String {
        lock.lock()
        defer { lock.unlock() }
        return lines.joined(separator: "\n")
    }

    public static func scrubPII(_ text: String) -> String {
        var out = text
        if let emailRx = try? NSRegularExpression(pattern: "[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}") {
            out = emailRx.stringByReplacingMatches(in: out, range: NSRange(out.startIndex..., in: out), withTemplate: "[REDACTED_EMAIL]")
        }
        if let bearerRx = try? NSRegularExpression(pattern: "(?i)(bearer\\s+)[A-Za-z0-9._-]+") {
            out = bearerRx.stringByReplacingMatches(in: out, range: NSRange(out.startIndex..., in: out), withTemplate: "$1[REDACTED_TOKEN]")
        }
        return out
    }
}

/// Background Main-Thread Hang / Watchdog monitor using a DispatchSemaphore ping onto DispatchQueue.main.
/// On hang timeout, suspends the actual main thread via Mach (`caliper_capture_main_thread_frames`),
/// reads its registers & vm_read_overwrite-protected FP stack, immediately resumes the main thread,
/// and reports slide-relative frames compatible with server-side Mach-O DWARF symbolication.
public final class CaliperMainThreadWatchdog: @unchecked Sendable {
    private let thresholdMS: Int
    private let pollIntervalMS: Int
    private let queue = DispatchQueue(label: "dev.caliper.apm.watchdog", qos: .utility)
    private let lock = NSLock()
    private var running = false
    private let onHang: (@Sendable (Int, [String]) -> Void)?
    private let onHangFrames: (@Sendable (Int, [StackFrame]) -> Void)?

    public init(
        thresholdMS: Int = 2000,
        pollIntervalMS: Int = 250,
        onHang: @escaping @Sendable (Int, [String]) -> Void
    ) {
        self.thresholdMS = max(50, thresholdMS)
        self.pollIntervalMS = max(20, pollIntervalMS)
        self.onHang = onHang
        self.onHangFrames = nil
    }

    public init(
        thresholdMS: Int = 2000,
        pollIntervalMS: Int = 250,
        onHangFrames: @escaping @Sendable (Int, [StackFrame]) -> Void
    ) {
        self.thresholdMS = max(50, thresholdMS)
        self.pollIntervalMS = max(20, pollIntervalMS)
        self.onHang = nil
        self.onHangFrames = onHangFrames
    }

    public func start(pingTarget: @escaping @Sendable (@escaping @Sendable () -> Void) -> Void = { ack in
        DispatchQueue.main.async { ack() }
    }) {
        // Ensure main thread Mach port & lock-free dyld image table are registered before monitoring.
        if Thread.isMainThread {
            caliper_register_main_thread()
        } else {
            DispatchQueue.main.async {
                caliper_register_main_thread()
            }
        }

        lock.lock()
        if running {
            lock.unlock()
            return
        }
        running = true
        lock.unlock()

        queue.async { [weak self] in
            guard let self else { return }
            while self.isRunning() {
                let sem = DispatchSemaphore(value: 0)
                let start = Date()
                pingTarget { sem.signal() }
                let waitResult = sem.wait(timeout: .now() + .milliseconds(self.thresholdMS))
                if waitResult == .timedOut && self.isRunning() {
                    let durMS = Int(Date().timeIntervalSince(start) * 1000)
                    let frames = Self.captureMainThreadFrames(maxFrames: 32)
                    if let onHangFrames = self.onHangFrames {
                        onHangFrames(durMS, frames)
                    } else if let onHang = self.onHang {
                        let formatted = frames.map { f -> String in
                            let mod = f.module ?? "unknown"
                            if let addr = f.address, f.symbol != addr {
                                return "\(mod) \(f.symbol) + \(addr)"
                            }
                            return "\(mod) \(f.symbol)"
                        }
                        onHang(durMS, formatted)
                    }
                }
                Thread.sleep(forTimeInterval: Double(self.pollIntervalMS) / 1000.0)
            }
        }
    }

    public func stop() {
        lock.lock()
        running = false
        lock.unlock()
    }

    /// Captures the hung main thread's actual call stack from a background thread using Mach
    /// `thread_suspend` + `thread_get_state` + `vm_read_overwrite` + `thread_resume`.
    /// Returns an empty array if called on the main thread itself (never self-deadlocks).
    public static func captureMainThreadFrames(maxFrames: Int = 32) -> [StackFrame] {
        let cap = max(1, min(maxFrames, 48))
        var raw = Array(repeating: caliper_captured_frame_t(), count: cap)
        let count = Int(caliper_capture_main_thread_frames(&raw, Int32(cap)))
        guard count > 0 else { return [] }
        return convertCapturedFrames(raw.prefix(count))
    }

    internal static func captureThreadFrames(machThreadPort: UInt32, maxFrames: Int = 32) -> [StackFrame] {
        let cap = max(1, min(maxFrames, 48))
        var raw = Array(repeating: caliper_captured_frame_t(), count: cap)
        let count = Int(caliper_capture_thread_frames_safe(machThreadPort, &raw, Int32(cap)))
        guard count > 0 else { return [] }
        return convertCapturedFrames(raw.prefix(count))
    }

    private static func convertCapturedFrames<S: Sequence>(_ items: S) -> [StackFrame] where S.Element == caliper_captured_frame_t {
        var out: [StackFrame] = []
        for item in items {
            let mod = cString(from: item.module)
            let sym = cString(from: item.symbol)
            let relHex = "0x" + String(item.relative_addr, radix: 16)
            out.append(StackFrame(
                module: mod.isEmpty ? "unknown" : mod,
                symbol: sym.isEmpty ? relHex : sym,
                address: relHex,
                inApp: CaliperModuleClassifier.isInAppModule(mod)
            ))
        }
        return out
    }

    private static func cString<T>(from tuple: T) -> String {
        withUnsafeBytes(of: tuple) { raw in
            let bytes = raw.prefix { $0 != 0 }
            return String(decoding: bytes, as: UTF8.self)
        }
    }

    private func isRunning() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return running
    }
}

/// Parsed frames and Mach-O binary UUIDs extracted from Apple's `MXCallStackTree.jsonRepresentation()`.
public struct ParsedMetricKitCallStack: Sendable, Equatable {
    public let frames: [StackFrame]
    public let binaryUUIDs: [String]
    public let threadAttributed: Bool

    public init(frames: [StackFrame], binaryUUIDs: [String], threadAttributed: Bool) {
        self.frames = frames
        self.binaryUUIDs = binaryUUIDs
        self.threadAttributed = threadAttributed
    }
}

/// Cross-platform pure-Swift parser for Apple MetricKit `MXCallStackTree.jsonRepresentation()`.
///
/// Accepts both direct `MXCallStackTree` JSON (`{"callStackPerThread": ..., "callStacks": [...]}`)
/// and wrapped diagnostic JSON (`{"callStackTree": {"callStacks": [...]}}`).
/// Walks the attributed (or hottest sampled) call stack from root to leaf and reverses into
/// Caliper's canonical leaf-first order with `offsetIntoBinaryTextSegment` formatted as `0x...`
/// for direct server-side Mach-O `.dSYM` DWARF symbolication.
public enum CaliperMetricKitCallStackParser {
    private struct WrappedTree: Decodable {
        let callStackTree: TreePayload?
        let callStacks: [ThreadStack]?
    }

    private struct TreePayload: Decodable {
        let callStacks: [ThreadStack]?
    }

    private struct ThreadStack: Decodable {
        let threadAttributed: Bool?
        let callStackRootFrames: [RawFrame]?
    }

    private struct RawFrame: Decodable {
        let binaryName: String?
        let binaryUUID: String?
        let address: UInt64?
        let offsetIntoBinaryTextSegment: UInt64?
        let sampleCount: Int?
        let subFrames: [RawFrame]?
    }

    public static func parse(jsonData: Data, maxFrames: Int = 48) -> ParsedMetricKitCallStack {
        guard !jsonData.isEmpty,
              let root = try? JSONDecoder().decode(WrappedTree.self, from: jsonData) else {
            return ParsedMetricKitCallStack(frames: [], binaryUUIDs: [], threadAttributed: false)
        }

        let stacks = root.callStacks ?? root.callStackTree?.callStacks ?? []
        guard !stacks.isEmpty else {
            return ParsedMetricKitCallStack(frames: [], binaryUUIDs: [], threadAttributed: false)
        }

        // Prefer the crashing/attributed thread; fall back to the first stack containing frames.
        let chosen = stacks.first(where: { $0.threadAttributed == true && !($0.callStackRootFrames?.isEmpty ?? true) })
            ?? stacks.first(where: { !($0.callStackRootFrames?.isEmpty ?? true) })
        guard let chosen, let roots = chosen.callStackRootFrames, !roots.isEmpty else {
            return ParsedMetricKitCallStack(frames: [], binaryUUIDs: [], threadAttributed: false)
        }

        let limit = max(1, min(maxFrames, 64))
        var rootToLeaf: [RawFrame] = []
        var current: RawFrame? = pickHottest(roots)
        while let node = current, rootToLeaf.count < limit {
            rootToLeaf.append(node)
            if let children = node.subFrames, !children.isEmpty {
                current = pickHottest(children)
            } else {
                current = nil
            }
        }

        // Reverse to leaf-first order (crashing / hanging frame at index 0).
        var frames: [StackFrame] = []
        var uuids = Set<String>()
        for raw in rootToLeaf.reversed() {
            let mod = (raw.binaryName?.isEmpty == false) ? raw.binaryName! : "unknown"
            if let uuid = raw.binaryUUID, !uuid.isEmpty {
                uuids.insert(uuid)
            }
            let relOrAbs = raw.offsetIntoBinaryTextSegment ?? raw.address
            let addrHex = relOrAbs.map { "0x" + String($0, radix: 16) } ?? "0x0"
            frames.append(StackFrame(
                module: mod,
                symbol: addrHex,
                address: addrHex,
                inApp: CaliperModuleClassifier.isInAppModule(mod)
            ))
        }

        return ParsedMetricKitCallStack(
            frames: frames,
            binaryUUIDs: uuids.sorted(),
            threadAttributed: chosen.threadAttributed == true
        )
    }

    private static func pickHottest(_ candidates: [RawFrame]) -> RawFrame? {
        guard var best = candidates.first else { return nil }
        var bestSamples = best.sampleCount ?? 0
        for item in candidates.dropFirst() {
            let sc = item.sampleCount ?? 0
            if sc > bestSamples {
                best = item
                bestSamples = sc
            }
        }
        return best
    }
}

#if canImport(MetricKit) && os(iOS)
import MetricKit

/// Subscribes to Apple MetricKit (iOS 14+) diagnostic payloads (crashes, hangs)
/// and forwards parsed `callStackTree` frames into CaliperClient without private APIs.
@available(iOS 14.0, *)
public final class CaliperMetricKitBridge: NSObject, MXMetricManagerSubscriber {
    private let client: CaliperClient

    public init(client: CaliperClient) {
        self.client = client
        super.init()
    }

    public func start() {
        MXMetricManager.shared.add(self)
    }

    public func stop() {
        MXMetricManager.shared.remove(self)
    }

    public func didReceive(_ payloads: [MXDiagnosticPayload]) {
        for payload in payloads {
            if let crashes = payload.crashDiagnostics {
                for crash in crashes {
                    let parsed = CaliperMetricKitCallStackParser.parse(
                        jsonData: crash.callStackTree.jsonRepresentation()
                    )
                    let top = parsed.frames.first(where: { $0.inApp == true }) ?? parsed.frames.first
                    let sigNum = crash.signal?.intValue ?? 0
                    let excType = crash.exceptionType?.intValue ?? 0
                    let excCode = crash.exceptionCode?.intValue ?? 0
                    let locSuffix = top.map { "-\($0.module ?? "app")-\($0.address ?? $0.symbol)" } ?? ""
                    let fp = "mx-crash-\(sigNum)-\(excType)\(locSuffix)"
                    let title = "MetricKit Crash (signal=\(sigNum) code=\(excCode))"
                    client.enqueue(
                        kind: "crash",
                        payload: .stability(StabilityPayload(
                            fingerprint: fp,
                            title: title,
                            fatal: true,
                            exceptionType: "MetricKitCrash",
                            threadName: parsed.threadAttributed ? "attributed-thread" : "unknown",
                            frames: parsed.frames
                        )),
                        bypassSampling: true
                    )
                }
            }
            if let hangs = payload.hangDiagnostics {
                for hang in hangs {
                    let durMS = Int(hang.hangDuration.converted(to: .milliseconds).value)
                    let parsed = CaliperMetricKitCallStackParser.parse(
                        jsonData: hang.callStackTree.jsonRepresentation()
                    )
                    let top = parsed.frames.first(where: { $0.inApp == true }) ?? parsed.frames.first
                    let fp: String
                    if let top {
                        fp = "mx-hang-\(top.module ?? "app")-\(top.address ?? top.symbol)"
                    } else {
                        fp = "mx-hang-\(durMS / 500 * 500)"
                    }
                    client.enqueue(
                        kind: "anr",
                        payload: .stability(StabilityPayload(
                            fingerprint: fp,
                            title: "MetricKit Main-Thread Hang (\(durMS)ms)",
                            fatal: false,
                            exceptionType: "Hang",
                            threadName: "main",
                            frames: parsed.frames
                        )),
                        bypassSampling: true
                    )
                }
            }
        }
    }
}
#endif
