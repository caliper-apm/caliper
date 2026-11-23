import Foundation
#if canImport(UIKit)
import UIKit
#endif

/// Extracts DNS / TCP / TLS / TTFB / Body timings from public URLSessionTaskTransactionMetrics.
public enum CaliperTransportExtractor {
    public static func extract(
        operation: String,
        method: String,
        statusCode: Int?,
        result: String,
        tx: URLSessionTaskTransactionMetrics,
        traceContext: CaliperTraceContext? = nil
    ) -> NetworkTransportPayload? {
        guard let fetchStart = tx.fetchStartDate, let responseEnd = tx.responseEndDate else {
            return nil
        }
        let totalMS = max(0, responseEnd.timeIntervalSince(fetchStart) * 1000.0)
        let dnsMS = intervalMS(tx.domainLookupStartDate, tx.domainLookupEndDate)
        let tcpMS = intervalMS(tx.connectStartDate, tx.secureConnectionStartDate ?? tx.connectEndDate)
        let tlsMS = intervalMS(tx.secureConnectionStartDate, tx.secureConnectionEndDate)
        let ttfbMS = intervalMS(tx.requestStartDate, tx.responseStartDate)
        let bodyMS = intervalMS(tx.responseStartDate, tx.responseEndDate)

        return NetworkTransportPayload(
            operation: operation,
            method: method.uppercased(),
            statusCode: statusCode,
            result: result,
            dnsMS: dnsMS,
            tcpMS: tcpMS,
            tlsMS: tlsMS,
            ttfbMS: ttfbMS,
            bodyMS: bodyMS,
            totalMS: totalMS,
            connectionReused: tx.isReusedConnection,
            protocolName: tx.networkProtocolName,
            traceID: traceContext?.traceID,
            spanID: traceContext?.spanID
        )
    }

    private static func intervalMS(_ start: Date?, _ end: Date?) -> Double? {
        guard let start, let end else { return nil }
        return max(0, end.timeIntervalSince(start) * 1000.0)
    }
}

/// Persists clean-exit marker to detect suspected OOM / abnormal terminations on next launch.
public final class CaliperAbnormalExitMarker {
    private let markerURL: URL

    public init(directory: URL) {
        self.markerURL = directory.appendingPathComponent("caliper_session_active.flag")
    }

    /// Returns true if previous foreground session terminated without a clean exit mark.
    public func checkPreviousAbnormalExitAndMarkActive() -> Bool {
        let fm = FileManager.default
        let abnormal = fm.fileExists(atPath: markerURL.path)
        try? fm.createDirectory(at: markerURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? Data("1".utf8).write(to: markerURL, options: .atomic)
        return abnormal
    }

    public func markCleanExit() {
        try? FileManager.default.removeItem(at: markerURL)
    }
}

#if canImport(UIKit)
/// Native CADisplayLink fluency sampler (zero private GPU APIs).
public final class CaliperDisplayLinkSampler: NSObject {
    private var displayLink: CADisplayLink?
    private var lastTimestamp: CFTimeInterval?
    private let onFrame: (Double, Double) -> Void

    public init(onFrame: @escaping (Double, Double) -> Void) {
        self.onFrame = onFrame
        super.init()
    }

    public func start() {
        guard displayLink == nil else { return }
        let link = CADisplayLink(target: self, selector: #selector(step(_:)))
        link.add(to: .main, forMode: .common)
        displayLink = link
    }

    public func stop() {
        displayLink?.invalidate()
        displayLink = nil
        lastTimestamp = nil
    }

    @objc private func step(_ link: CADisplayLink) {
        defer { lastTimestamp = link.timestamp }
        guard let prev = lastTimestamp else { return }
        let durationMS = max(0, (link.timestamp - prev) * 1000.0)
        let targetMS = max(1.0, (link.targetTimestamp - link.timestamp) * 1000.0)
        onFrame(durationMS, targetMS)
    }
}
#endif
