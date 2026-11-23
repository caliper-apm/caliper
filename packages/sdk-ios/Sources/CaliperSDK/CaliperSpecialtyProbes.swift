import Foundation
#if canImport(UIKit) && !os(watchOS)
import UIKit
#endif
#if canImport(WebKit)
import WebKit
#endif

// ===== 1. Thermal & Battery State Probe =====

public struct ThermalBatterySnapshot: Sendable, Equatable {
    public let thermalLevel: String    // nominal | fair | serious | critical
    public let batteryLevelPct: Int    // 0..100 or -1 if unavailable
    public let isCharging: Bool
    public let memoryFootprintKiB: Double
    public let scene: String

    public init(
        thermalLevel: String,
        batteryLevelPct: Int,
        isCharging: Bool,
        memoryFootprintKiB: Double,
        scene: String = "unknown"
    ) {
        self.thermalLevel = thermalLevel
        self.batteryLevelPct = batteryLevelPct
        self.isCharging = isCharging
        self.memoryFootprintKiB = memoryFootprintKiB
        self.scene = scene
    }
}

public enum CaliperThermalBatteryProbe {
    public static func thermalLevelString(_ state: ProcessInfo.ThermalState) -> String {
        switch state {
        case .nominal:  return "nominal"
        case .fair:     return "fair"
        case .serious:  return "serious"
        case .critical: return "critical"
        @unknown default: return "nominal"
        }
    }

    public static func isSevere(_ snap: ThermalBatterySnapshot) -> Bool {
        snap.thermalLevel == "serious" || snap.thermalLevel == "critical"
    }

    public static func captureSnapshot(scene: String = "unknown") -> ThermalBatterySnapshot {
        let level = thermalLevelString(ProcessInfo.processInfo.thermalState)
        var pct = -1
        var charging = false
        #if canImport(UIKit) && os(iOS)
        let dev = UIDevice.current
        if dev.isBatteryMonitoringEnabled && dev.batteryLevel >= 0 {
            pct = Int(dev.batteryLevel * 100.0)
            charging = (dev.batteryState == .charging || dev.batteryState == .full)
        }
        #endif
        return ThermalBatterySnapshot(
            thermalLevel: level,
            batteryLevelPct: pct,
            isCharging: charging,
            memoryFootprintKiB: CaliperSystemMetrics.physicalFootprintKiB() ?? 0,
            scene: scene
        )
    }

    @discardableResult
    public static func record(client: CaliperClient, snapshot: ThermalBatterySnapshot) -> Bool {
        let severe = isSevere(snapshot)
        let errCode: String? = switch snapshot.thermalLevel {
        case "critical": "THERMAL_CRITICAL"
        case "serious":  "THERMAL_SERIOUS"
        default: nil
        }
        var stages: [SceneStage] = []
        if snapshot.batteryLevelPct >= 0 {
            stages.append(SceneStage(name: "battery_pct", durationMS: Double(snapshot.batteryLevelPct)))
        }
        if snapshot.memoryFootprintKiB > 0 {
            stages.append(SceneStage(name: "footprint_mib", durationMS: snapshot.memoryFootprintKiB / 1024.0))
        }
        let severityScore: Double = switch snapshot.thermalLevel {
        case "critical": 3.0
        case "serious":  2.0
        case "fair":     1.0
        default:         0.0
        }
        return client.recordCustomJourney(
            journeyName: "device_thermal_state",
            status: severe ? "failure" : "success",
            durationMS: severityScore,
            stages: stages.isEmpty ? nil : stages,
            errorCode: errCode,
            scene: snapshot.scene
        )
    }
}

// ===== 2. Bounded, Zero-PII Sandbox Disk Storage Auditor =====

public struct DiskBucketStat: Sendable, Equatable {
    public let bucket: String
    public let totalBytes: Int64
    public let fileCount: Int
}

public struct DiskLargeFile: Sendable, Equatable {
    public let sanitizedPath: String
    public let sizeBytes: Int64
}

public struct DiskAuditReport: Sendable, Equatable {
    public let totalBytes: Int64
    public let visitedFiles: Int
    public let truncated: Bool
    public let buckets: [DiskBucketStat]
    public let largeFiles: [DiskLargeFile]
}

public enum CaliperDiskAuditProbe {
    public static func sanitizePath(_ relativePath: String) -> String {
        var out = relativePath.replacingOccurrences(of: "\\", with: "/")
        let rules: [(String, String)] = [
            ("[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}", "<email>"),
            ("[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}", "<uuid>"),
            ("[0-9a-fA-F]{16,}", "<hash>"),
            ("(?<![0-9])\\d{6,}(?![0-9])", "<id>"),
        ]
        for (pattern, repl) in rules {
            if let rx = try? NSRegularExpression(pattern: pattern) {
                let range = NSRange(out.startIndex..., in: out)
                out = rx.stringByReplacingMatches(in: out, range: range, withTemplate: repl)
            }
        }
        return out
    }

    public static func auditDirectories(
        roots: [(String, URL)],
        maxDepth: Int = 4,
        maxFiles: Int = 2000,
        largeFileThresholdBytes: Int64 = 20 * 1024 * 1024
    ) -> DiskAuditReport {
        let fm = FileManager.default
        var totalBytes: Int64 = 0
        var visited = 0
        var truncated = false
        var buckets: [DiskBucketStat] = []
        var largeFiles: [DiskLargeFile] = []

        for (bucketName, rootURL) in roots {
            var bucketBytes: Int64 = 0
            var bucketCount = 0

            func walk(_ dirURL: URL, depth: Int) {
                if depth > maxDepth || visited >= maxFiles {
                    if visited >= maxFiles { truncated = true }
                    return
                }
                guard let items = try? fm.contentsOfDirectory(
                    at: dirURL,
                    includingPropertiesForKeys: [.isDirectoryKey, .fileSizeKey],
                    options: [.skipsPackageDescendants]
                ) else { return }

                for item in items {
                    if visited >= maxFiles {
                        truncated = true
                        return
                    }
                    let rv = try? item.resourceValues(forKeys: [.isDirectoryKey, .fileSizeKey])
                    if rv?.isDirectory == true {
                        walk(item, depth: depth + 1)
                    } else {
                        visited += 1
                        let sz = Int64(rv?.fileSize ?? 0)
                        bucketBytes += max(0, sz)
                        bucketCount += 1
                        if sz >= largeFileThresholdBytes && largeFiles.count < 10 {
                            let rel = item.path.replacingOccurrences(of: rootURL.path, with: "")
                                .trimmingCharacters(in: CharacterSet(charactersIn: "/"))
                            largeFiles.append(DiskLargeFile(
                                sanitizedPath: "\(bucketName)/\(sanitizePath(rel))",
                                sizeBytes: sz
                            ))
                        }
                    }
                }
            }

            walk(rootURL, depth: 0)
            totalBytes += bucketBytes
            buckets.append(DiskBucketStat(bucket: bucketName, totalBytes: bucketBytes, fileCount: bucketCount))
        }

        return DiskAuditReport(
            totalBytes: totalBytes,
            visitedFiles: visited,
            truncated: truncated,
            buckets: buckets,
            largeFiles: largeFiles.sorted { $0.sizeBytes > $1.sizeBytes }
        )
    }

    @discardableResult
    public static func recordAudit(
        client: CaliperClient,
        report: DiskAuditReport,
        quotaBudgetMiB: Double = 500.0
    ) -> Bool {
        let totalMiB = Double(report.totalBytes) / (1024.0 * 1024.0)
        let exceeded = totalMiB > quotaBudgetMiB
        let stages = report.buckets.map { b in
            SceneStage(name: "\(b.bucket)_mb", durationMS: Double(b.totalBytes) / (1024.0 * 1024.0))
        }
        return client.recordCustomJourney(
            journeyName: "disk_storage_audit",
            status: exceeded ? "failure" : "success",
            durationMS: totalMiB,
            stages: stages,
            errorCode: exceeded ? "DISK_QUOTA_EXCEEDED" : nil,
            scene: "disk_audit"
        )
    }
}

// ===== 3. WebView / WKWebView Performance & Blank-Screen Probe =====

public enum CaliperWebViewProbe {
    /// Lightweight (~1.3 KB) vanilla JS probe collecting NavigationTiming, FCP, JS errors, and blank-screen status.
    public static let injectedJavaScript: String = """
    (function(){if(window.__caliperWV)return;window.__caliperWV=1;function emit(p){try{var s=JSON.stringify(p);if(window.webkit&&window.webkit.messageHandlers&&window.webkit.messageHandlers.caliperWV){window.webkit.messageHandlers.caliperWV.postMessage(s);}else if(window.CaliperWV&&window.CaliperWV.postMessage){window.CaliperWV.postMessage(s);}}catch(_){}}var fcp=0;try{new PerformanceObserver(function(l){l.getEntries().forEach(function(e){if(e.name==='first-contentful-paint')fcp=e.startTime;});}).observe({type:'paint',buffered:true});}catch(_){}window.addEventListener('error',function(e){emit({type:'js_error',url:location.origin+location.pathname,message:String(e.message||'Script error')});});window.addEventListener('unhandledrejection',function(e){emit({type:'js_error',url:location.origin+location.pathname,message:String(e.reason||'Unhandled rejection')});});function reportNav(){try{var n=performance.getEntriesByType('navigation')[0];var txt=(document.body&&document.body.innerText||'').trim();var nodes=document.body?document.body.querySelectorAll('*').length:0;var blank=(txt.length===0&&nodes<4);emit({type:'nav',url:location.origin+location.pathname,dns_ms:n?Math.max(0,n.domainLookupEnd-n.domainLookupStart):0,tcp_ms:n?Math.max(0,n.connectEnd-n.connectStart):0,ttfb_ms:n?Math.max(0,n.responseStart-n.requestStart):0,dom_ms:n?Math.max(0,n.domInteractive-n.startTime):0,load_ms:n?Math.max(0,n.loadEventEnd-n.startTime):0,fcp_ms:Math.max(0,fcp),blank:blank});}catch(_){}}if(document.readyState==='complete'){setTimeout(reportNav,50);}else{window.addEventListener('load',function(){setTimeout(reportNav,50);});}})();
    """

    public static func normalizeURL(_ rawURL: String) -> String {
        let stripped = rawURL.split(separator: "?").first.map(String.init) ?? rawURL
        let noFrag = stripped.split(separator: "#").first.map(String.init) ?? stripped
        let trimmed = noFrag.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? "webview://unknown" : String(trimmed.prefix(180))
    }

    @discardableResult
    public static func ingestMessageJSON(_ rawJSON: String, client: CaliperClient) -> Bool {
        guard let data = rawJSON.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = obj["type"] as? String else {
            return false
        }
        let normURL = normalizeURL(obj["url"] as? String ?? "")
        if type == "js_error" {
            let msg = String(((obj["message"] as? String) ?? "WebView JS Error").prefix(200))
            let fp = "webview-jserr-\(normURL)-\(msg.prefix(32))"
            return client.enqueue(
                kind: "crash",
                payload: .stability(StabilityPayload(
                    fingerprint: fp,
                    title: "WebView JS Error: \(msg)",
                    fatal: false,
                    exceptionType: "WebViewJSError",
                    threadName: "webview-js",
                    frames: [StackFrame(module: "webview", symbol: normURL, inApp: true)]
                )),
                scene: normURL,
                bypassSampling: true
            )
        }
        if type == "nav" {
            let dns = max(0, (obj["dns_ms"] as? NSNumber)?.doubleValue ?? 0)
            let tcp = max(0, (obj["tcp_ms"] as? NSNumber)?.doubleValue ?? 0)
            let ttfb = max(0, (obj["ttfb_ms"] as? NSNumber)?.doubleValue ?? 0)
            let dom = max(0, (obj["dom_ms"] as? NSNumber)?.doubleValue ?? 0)
            let load = max(0, (obj["load_ms"] as? NSNumber)?.doubleValue ?? 0)
            let fcp = max(0, (obj["fcp_ms"] as? NSNumber)?.doubleValue ?? 0)
            let blank = (obj["blank"] as? Bool) ?? false
            let total = max(load, max(dom, max(fcp, ttfb)))
            return client.recordCustomJourney(
                journeyName: "webview_page_load",
                status: blank ? "failure" : "success",
                durationMS: total,
                stages: [
                    SceneStage(name: "dns", durationMS: dns),
                    SceneStage(name: "tcp", durationMS: tcp),
                    SceneStage(name: "ttfb", durationMS: ttfb),
                    SceneStage(name: "dom_interactive", durationMS: dom),
                    SceneStage(name: "fcp", durationMS: fcp),
                    SceneStage(name: "load_complete", durationMS: load),
                ],
                errorCode: blank ? "WEBVIEW_BLANK_SCREEN" : nil,
                scene: normURL
            )
        }
        return false
    }
}

#if canImport(WebKit)
/// Zero-PII WKWebView script message bridge injecting `CaliperWebViewProbe` automatically.
public final class CaliperWKWebViewBridge: NSObject, WKScriptMessageHandler {
    private let client: CaliperClient

    public init(client: CaliperClient) {
        self.client = client
        super.init()
    }

    @MainActor
    @discardableResult
    public static func attach(to controller: WKUserContentController, client: CaliperClient) -> CaliperWKWebViewBridge {
        let bridge = CaliperWKWebViewBridge(client: client)
        controller.add(bridge, name: "caliperWV")
        let script = WKUserScript(
            source: CaliperWebViewProbe.injectedJavaScript,
            injectionTime: .atDocumentEnd,
            forMainFrameOnly: true
        )
        controller.addUserScript(script)
        return bridge
    }

    public func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        if let raw = message.body as? String {
            CaliperWebViewProbe.ingestMessageJSON(raw, client: client)
        }
    }
}
#endif
