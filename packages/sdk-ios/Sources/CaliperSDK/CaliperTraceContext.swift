import Foundation
#if canImport(Security)
import Security
#endif

/// W3C Trace Context Level 1 (`traceparent`) generator, parser, and allowlist propagator.
///
/// Format: `00-<32-lower-hex-trace-id>-<16-lower-hex-span-id>-<2-hex-flags>`
public struct CaliperTraceContext: Equatable, Sendable {
    public static let headerName = "traceparent"
    private static let zeroTraceID = "00000000000000000000000000000000"
    private static let zeroSpanID = "0000000000000000"

    public let traceID: String
    public let spanID: String
    public let sampled: Bool

    public init(traceID: String, spanID: String, sampled: Bool = true) {
        self.traceID = traceID
        self.spanID = spanID
        self.sampled = sampled
    }

    public var traceparentHeader: String {
        "00-\(traceID)-\(spanID)-\(sampled ? "01" : "00")"
    }

    public static func newRoot(sampled: Bool = true) -> CaliperTraceContext {
        var traceID = ""
        repeat {
            traceID = randomHex(byteCount: 16)
        } while traceID == zeroTraceID

        var spanID = ""
        repeat {
            spanID = randomHex(byteCount: 8)
        } while spanID == zeroSpanID

        return CaliperTraceContext(traceID: traceID, spanID: spanID, sampled: sampled)
    }

    public static func parse(traceparent raw: String?) -> CaliperTraceContext? {
        guard let trimmed = raw?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased(),
              !trimmed.isEmpty else {
            return nil
        }
        let parts = trimmed.split(separator: "-", omittingEmptySubsequences: false)
        guard parts.count >= 4 else { return nil }
        let version = String(parts[0])
        let traceID = String(parts[1])
        let spanID = String(parts[2])
        let flags = String(parts[3])

        guard version.count == 2, isLowerHex(version), version != "ff" else { return nil }
        guard traceID.count == 32, isLowerHex(traceID), traceID != zeroTraceID else { return nil }
        guard spanID.count == 16, isLowerHex(spanID), spanID != zeroSpanID else { return nil }
        guard flags.count == 2, isLowerHex(flags), let flagVal = UInt8(flags, radix: 16) else { return nil }
        return CaliperTraceContext(traceID: traceID, spanID: spanID, sampled: (flagVal & 0x01) != 0)
    }

    /// Evaluates whether W3C `traceparent` should be injected for `url`, enforcing explicit host
    /// allowlists (`*`, exact host, or `.domain.com` suffix) and preventing self-tracing loops.
    public static func shouldPropagate(
        url: URL?,
        targets: [String],
        collectorEndpointURL: URL? = nil
    ) -> Bool {
        guard !targets.isEmpty, let rawHost = url?.host?.lowercased() else {
            return false
        }
        let host = rawHost.hasSuffix(".") ? String(rawHost.dropLast()) : rawHost
        guard !host.isEmpty else { return false }

        if let collectorHost = collectorEndpointURL?.host?.lowercased(),
           collectorHost == host,
           !targets.contains(where: { $0.trimmingCharacters(in: .whitespaces).lowercased() == host }) {
            return false
        }

        for rawRule in targets {
            let rule = rawRule.trimmingCharacters(in: .whitespaces).lowercased()
            if rule.isEmpty { continue }
            if rule == "*" { return true }
            if rule.hasPrefix(".") {
                let bare = String(rule.dropFirst())
                if host == bare || host.hasSuffix(rule) { return true }
            } else if host == rule {
                return true
            }
        }
        return false
    }

    /// Injects a W3C `traceparent` header into a copy of `request` when its host matches `targets`.
    /// Existing valid `traceparent` headers are adopted without overwriting.
    public static func inject(
        into request: URLRequest,
        targets: [String],
        collectorEndpointURL: URL? = nil,
        sampled: Bool = true
    ) -> (URLRequest, CaliperTraceContext?) {
        guard shouldPropagate(url: request.url, targets: targets, collectorEndpointURL: collectorEndpointURL) else {
            return (request, nil)
        }
        if let existing = parse(traceparent: request.value(forHTTPHeaderField: headerName)) {
            return (request, existing)
        }
        let ctx = newRoot(sampled: sampled)
        var copy = request
        copy.setValue(ctx.traceparentHeader, forHTTPHeaderField: headerName)
        return (copy, ctx)
    }

    private static func randomHex(byteCount: Int) -> String {
        var bytes = [UInt8](repeating: 0, count: byteCount)
        var generated = false
        #if canImport(Security)
        if SecRandomCopyBytes(kSecRandomDefault, byteCount, &bytes) == errSecSuccess {
            generated = true
        }
        #endif
        if !generated {
            var rng = SystemRandomNumberGenerator()
            for i in 0..<byteCount {
                bytes[i] = UInt8.random(in: 0...255, using: &rng)
            }
        }
        let hexChars = Array("0123456789abcdef".utf8)
        var out = [UInt8]()
        out.reserveCapacity(byteCount * 2)
        for b in bytes {
            out.append(hexChars[Int(b >> 4)])
            out.append(hexChars[Int(b & 0x0F)])
        }
        return String(decoding: out, as: UTF8.self)
    }

    private static func isLowerHex(_ s: String) -> Bool {
        for b in s.utf8 {
            let ok = (b >= 48 && b <= 57) || (b >= 97 && b <= 102)
            if !ok { return false }
        }
        return true
    }
}

public extension URLRequest {
    /// Returns a copy of this `URLRequest` with W3C `traceparent` injected if the URL host matches
    /// either `targets` or the installed `CaliperIOSAgent`'s configured `tracePropagationTargets`.
    func withCaliperTraceparent(targets: [String]? = nil, sampled: Bool = true) -> URLRequest {
        let cfg = CaliperIOSAgent.client()?.configuration
        let effectiveTargets = targets ?? cfg?.tracePropagationTargets ?? []
        return CaliperTraceContext.inject(
            into: self,
            targets: effectiveTargets,
            collectorEndpointURL: cfg?.endpointURL,
            sampled: sampled
        ).0
    }
}
