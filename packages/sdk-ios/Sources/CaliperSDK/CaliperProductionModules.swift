import Foundation

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
public final class CaliperMainThreadWatchdog: @unchecked Sendable {
    private let thresholdMS: Int
    private let pollIntervalMS: Int
    private let queue = DispatchQueue(label: "dev.caliper.apm.watchdog", qos: .utility)
    private let lock = NSLock()
    private var running = false
    private let onHang: @Sendable (Int, [String]) -> Void

    public init(
        thresholdMS: Int = 2000,
        pollIntervalMS: Int = 250,
        onHang: @escaping @Sendable (Int, [String]) -> Void
    ) {
        self.thresholdMS = max(50, thresholdMS)
        self.pollIntervalMS = max(20, pollIntervalMS)
        self.onHang = onHang
    }

    public func start(pingTarget: @escaping @Sendable (@escaping @Sendable () -> Void) -> Void = { ack in
        DispatchQueue.main.async { ack() }
    }) {
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
                    let symbols = Thread.callStackSymbols.prefix(32).map { String($0) }
                    self.onHang(durMS, symbols)
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

    private func isRunning() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return running
    }
}

#if canImport(MetricKit) && os(iOS)
import MetricKit

/// Subscribes to Apple MetricKit (iOS 14+) diagnostic payloads (crashes, hangs, disk writes)
/// and forwards them into CaliperClient without private APIs or unsafe swizzling.
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
                    let title = "MetricKit Crash (signal=\(crash.signal?.intValue ?? 0) code=\(crash.exceptionCode?.intValue ?? 0))"
                    let fp = "mx-crash-\(crash.signal?.intValue ?? 0)-\(crash.exceptionType?.intValue ?? 0)"
                    client.enqueue(
                        kind: "crash",
                        payload: .stability(StabilityPayload(
                            fingerprint: fp,
                            title: title,
                            fatal: true,
                            exceptionType: "MetricKitCrash",
                            frames: []
                        )),
                        bypassSampling: true
                    )
                }
            }
            if let hangs = payload.hangDiagnostics {
                for hang in hangs {
                    let durMS = Int(hang.hangDuration.converted(to: .milliseconds).value)
                    client.enqueue(
                        kind: "anr",
                        payload: .stability(StabilityPayload(
                            fingerprint: "mx-hang-\(durMS / 500 * 500)",
                            title: "MetricKit Main-Thread Hang (\(durMS)ms)",
                            fatal: false,
                            exceptionType: "Hang",
                            frames: []
                        )),
                        bypassSampling: true
                    )
                }
            }
        }
    }
}
#endif
