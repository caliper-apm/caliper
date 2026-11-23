import Foundation
import ObjectiveC.runtime
#if canImport(UIKit) && os(iOS)
import UIKit
#endif
#if canImport(SwiftUI)
import SwiftUI
#endif

/// Configuration switches for 1-line `CaliperIOSAgent.install(...)` auto-instrumentation.
public struct CaliperIOSAgentOptions: Sendable {
    public var enableNativeCrash: Bool
    public var enableFOOMDetector: Bool
    public var enableWatchdog: Bool
    public var enableMetricKit: Bool
    public var autoInstrumentViewControllers: Bool
    public var enableThermalMonitor: Bool
    public var enableDailyDiskAudit: Bool
    public var diskQuotaBudgetMiB: Double
    public var watchdogThresholdMS: Int
    public var watchdogPollIntervalMS: Int
    public var stateDirectoryURL: URL?

    public init(
        enableNativeCrash: Bool = true,
        enableFOOMDetector: Bool = true,
        enableWatchdog: Bool = true,
        enableMetricKit: Bool = true,
        autoInstrumentViewControllers: Bool = true,
        enableThermalMonitor: Bool = true,
        enableDailyDiskAudit: Bool = true,
        diskQuotaBudgetMiB: Double = 500.0,
        watchdogThresholdMS: Int = 3000,
        watchdogPollIntervalMS: Int = 1000,
        stateDirectoryURL: URL? = nil
    ) {
        self.enableNativeCrash = enableNativeCrash
        self.enableFOOMDetector = enableFOOMDetector
        self.enableWatchdog = enableWatchdog
        self.enableMetricKit = enableMetricKit
        self.autoInstrumentViewControllers = autoInstrumentViewControllers
        self.enableThermalMonitor = enableThermalMonitor
        self.enableDailyDiskAudit = enableDailyDiskAudit
        self.diskQuotaBudgetMiB = diskQuotaBudgetMiB
        self.watchdogThresholdMS = watchdogThresholdMS
        self.watchdogPollIntervalMS = watchdogPollIntervalMS
        self.stateDirectoryURL = stateDirectoryURL
    }
}

/// Drop-in 1-line Pure-Native iOS/macOS APM Agent (Zero React Native deps, zero private APIs).
///
/// Automatically wires:
/// - Real-time C/Mach Exception + Unix Signal crash reporter (`CaliperNativeCrashReporter`)
/// - 9-branch Heuristic Foreground OOM state machine (`CaliperFOOMDetector`)
/// - Off-main-thread Watchdog & MetricKit (iOS 14+)
/// - Zero-swizzle lifecycle & thermal state observers + safe opt-in `UIViewController` scene tracking
/// - Low-frequency bounded sandbox disk audit (`CaliperDiskAuditProbe`)
public final class CaliperIOSAgent: @unchecked Sendable {
    public static let shared = CaliperIOSAgent()

    private let lock = NSLock()
    private var installed = false
    private var _client: CaliperClient?
    private var _crashReporter: CaliperNativeCrashReporter?
    private var _foomDetector: CaliperFOOMDetector?
    private var _watchdog: CaliperMainThreadWatchdog?
    private var _ringLog = CaliperDiagnosticRingLog(maxLines: 500)
    private var observers: [NSObjectProtocol] = []
    private var _lastFOOMClass: FOOMTerminationClass = .none
    #if canImport(MetricKit) && os(iOS)
    private var _metricKitBox: AnyObject?
    #endif

    private init() {}

    @discardableResult
    public static func install(
        config: CaliperConfig,
        options: CaliperIOSAgentOptions = CaliperIOSAgentOptions()
    ) -> CaliperClient {
        shared.installInternal(config: config, options: options)
    }

    public static func client() -> CaliperClient? {
        shared.lock.lock()
        defer { shared.lock.unlock() }
        return shared._client
    }

    public static func lastFOOMClassification() -> FOOMTerminationClass {
        shared.lock.lock()
        defer { shared.lock.unlock() }
        return shared._lastFOOMClass
    }

    public static func setScene(_ scene: String) {
        shared.lock.lock()
        let c = shared._client
        let cr = shared._crashReporter
        let foom = shared._foomDetector
        shared.lock.unlock()

        c?.setScene(scene)
        cr?.setScene(scene)
        foom?.markForeground(scene: scene)
    }

    public static func log(level: String, tag: String, message: String) {
        shared._ringLog.append(level: level, tag: tag, message: message)
    }

    /// Cleanly tears down handlers and observers (primarily for unit tests).
    public static func resetForTesting() {
        shared.lock.lock()
        for obs in shared.observers {
            NotificationCenter.default.removeObserver(obs)
        }
        shared.observers.removeAll()
        shared._watchdog?.stop()
        shared._crashReporter?.uninstall()
        shared._watchdog = nil
        shared._crashReporter = nil
        shared._foomDetector = nil
        shared._client = nil
        shared.installed = false
        shared._lastFOOMClass = .none
        shared.lock.unlock()
    }

    private func installInternal(config: CaliperConfig, options: CaliperIOSAgentOptions) -> CaliperClient {
        lock.lock()
        if installed, let existing = _client {
            lock.unlock()
            return existing
        }

        let stateDir = options.stateDirectoryURL ?? FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("caliper-apm-state")
        try? FileManager.default.createDirectory(at: stateDir, withIntermediateDirectories: true)

        let effectiveConfig = CaliperConfig(
            endpointURL: config.endpointURL,
            apiKey: config.apiKey,
            resource: config.resource,
            installSeed: config.installSeed,
            maxQueueSize: config.maxQueueSize,
            maxConsecutiveFailures: config.maxConsecutiveFailures,
            spoolDirectoryURL: config.spoolDirectoryURL ?? stateDir.appendingPathComponent("spool")
        )
        let client = CaliperClient(config: effectiveConfig)
        _client = client

        // 1. Native C/Mach + Signal Crash Reporter (always runs before FOOM check!)
        var priorCrashCount = 0
        if options.enableNativeCrash {
            let reporter = CaliperNativeCrashReporter(directoryURL: stateDir.appendingPathComponent("crashes"))
            priorCrashCount = reporter.install(client: client, enableMachHandler: true, scene: "startup")
            _crashReporter = reporter
        }

        // 2. Heuristic FOOM State Machine
        if options.enableFOOMDetector {
            let foom = CaliperFOOMDetector(directoryURL: stateDir)
            _lastFOOMClass = foom.startSession(
                client: client,
                appVersion: config.resource.appVersion,
                buildNumber: config.resource.buildNumber,
                osVersion: config.resource.osVersion ?? "",
                previousCrashDetected: priorCrashCount > 0,
                initialScene: "startup"
            )
            _foomDetector = foom
        }

        // 3. Watchdog
        if options.enableWatchdog {
            let wd = CaliperMainThreadWatchdog(
                thresholdMS: options.watchdogThresholdMS,
                pollIntervalMS: options.watchdogPollIntervalMS
            ) { durMS, stack in
                let frames = stack.prefix(24).map { StackFrame(symbol: $0, inApp: true) }
                client.enqueue(
                    kind: "anr",
                    payload: .stability(StabilityPayload(
                        fingerprint: "ios-watchdog-\(durMS / 500 * 500)",
                        title: "Main Thread Hang (\(durMS)ms)",
                        fatal: false,
                        exceptionType: "WatchdogHang",
                        threadName: "main",
                        frames: frames
                    )),
                    bypassSampling: true
                )
            }
            wd.start()
            _watchdog = wd
        }

        // 4. MetricKit (iOS 14+)
        #if canImport(MetricKit) && os(iOS)
        if options.enableMetricKit, #available(iOS 14.0, *) {
            let mk = CaliperMetricKitBridge(client: client)
            mk.start()
            _metricKitBox = mk
        }
        #endif

        // 5. Thermal State Observer (zero swizzle)
        if options.enableThermalMonitor {
            let thermalObs = NotificationCenter.default.addObserver(
                forName: ProcessInfo.thermalStateDidChangeNotification,
                object: nil,
                queue: nil
            ) { _ in
                let snap = CaliperThermalBatteryProbe.captureSnapshot()
                if CaliperThermalBatteryProbe.isSevere(snap) {
                    CaliperThermalBatteryProbe.record(client: client, snapshot: snap)
                }
            }
            observers.append(thermalObs)
        }

        // 6. iOS Lifecycle Notifications + Safe Opt-In UIViewController Swizzle
        #if canImport(UIKit) && os(iOS)
        let fgObs = NotificationCenter.default.addObserver(
            forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main
        ) { [weak self] _ in
            self?._foomDetector?.markForeground()
        }
        let bgObs = NotificationCenter.default.addObserver(
            forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main
        ) { [weak self] _ in
            self?._foomDetector?.markBackground()
            Task { try? await client.flush() }
        }
        let termObs = NotificationCenter.default.addObserver(
            forName: UIApplication.willTerminateNotification, object: nil, queue: .main
        ) { [weak self] _ in
            self?._foomDetector?.markCleanTerminate()
        }
        observers.append(contentsOf: [fgObs, bgObs, termObs])

        if options.autoInstrumentViewControllers {
            UIViewController.caliperInstallAutoSceneTracking()
        }
        #endif

        // 7. Bounded Daily Disk Audit (background utility queue, at most once per 24h)
        if options.enableDailyDiskAudit {
            let markerURL = stateDir.appendingPathComponent("last_disk_audit.txt")
            let quotaMiB = options.diskQuotaBudgetMiB
            DispatchQueue.global(qos: .utility).async {
                let nowSec = Date().timeIntervalSince1970
                if let prev = try? String(contentsOf: markerURL, encoding: .utf8),
                   let prevSec = Double(prev.trimmingCharacters(in: .whitespacesAndNewlines)),
                   nowSec - prevSec < 86_400 {
                    return
                }
                let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first
                let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first
                let tmp = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
                var roots: [(String, URL)] = [("tmp", tmp)]
                if let caches { roots.append(("cache", caches)) }
                if let docs { roots.append(("documents", docs)) }
                let report = CaliperDiskAuditProbe.auditDirectories(roots: roots)
                CaliperDiskAuditProbe.recordAudit(client: client, report: report, quotaBudgetMiB: quotaMiB)
                try? String(nowSec).write(to: markerURL, atomically: true, encoding: .utf8)
            }
        }

        installed = true
        lock.unlock()
        _ringLog.append(level: "INFO", tag: "CaliperIOSAgent", message: "installed sdk=\(config.resource.sdkVersion)")
        return client
    }
}

/// Safe forwarding `URLSessionTaskDelegate` proxy preserving host SSL pinning / auth delegate selectors
/// while capturing `URLSessionTaskMetrics` transport waterfalls automatically.
public final class CaliperAutoURLSessionDelegate: NSObject, URLSessionTaskDelegate, URLSessionDataDelegate {
    private let client: CaliperClient
    private weak var originalDelegate: URLSessionDelegate?

    public init(client: CaliperClient, forwardingTo original: URLSessionDelegate? = nil) {
        self.client = client
        self.originalDelegate = original
        super.init()
    }

    public override func responds(to aSelector: Selector!) -> Bool {
        if super.responds(to: aSelector) { return true }
        return originalDelegate?.responds(to: aSelector) ?? false
    }

    public override func forwardingTarget(for aSelector: Selector!) -> Any? {
        if let orig = originalDelegate, orig.responds(to: aSelector) {
            return orig
        }
        return super.forwardingTarget(for: aSelector)
    }

    public func urlSession(_ session: URLSession, task: URLSessionTask, didFinishCollecting metrics: URLSessionTaskMetrics) {
        if let tx = metrics.transactionMetrics.last {
            let op = CaliperWebViewProbe.normalizeURL(task.originalRequest?.url?.path ?? "/")
            let method = task.originalRequest?.httpMethod ?? "GET"
            let code = (task.response as? HTTPURLResponse)?.statusCode ?? 0
            let result = task.error != nil ? "failed" : (code >= 500 || code == 0 ? "failed" : "success")
            let dns = deltaMS(tx.domainLookupStartDate, tx.domainLookupEndDate)
            let tcp = deltaMS(tx.connectStartDate, tx.connectEndDate)
            let tls = deltaMS(tx.secureConnectionStartDate, tx.secureConnectionEndDate)
            let ttfb = deltaMS(tx.requestStartDate, tx.responseStartDate)
            let body = deltaMS(tx.responseStartDate, tx.responseEndDate)
            let total = metrics.taskInterval.duration * 1000.0
            client.enqueue(
                kind: "network_stage",
                payload: .networkTransport(NetworkTransportPayload(
                    operation: op,
                    method: method,
                    statusCode: code,
                    result: result,
                    dnsMS: dns,
                    tcpMS: tcp,
                    tlsMS: tls,
                    ttfbMS: ttfb,
                    bodyMS: body,
                    totalMS: max(0, total),
                    connectionReused: tx.isReusedConnection,
                    protocolName: tx.networkProtocolName
                ))
            )
        }
        if let taskDel = originalDelegate as? URLSessionTaskDelegate {
            taskDel.urlSession?(session, task: task, didFinishCollecting: metrics)
        }
    }

    private func deltaMS(_ start: Date?, _ end: Date?) -> Double? {
        guard let s = start, let e = end else { return nil }
        return max(0, e.timeIntervalSince(s) * 1000.0)
    }
}

#if canImport(UIKit) && os(iOS)
extension UIViewController {
    private static let swizzleOnce: Void = {
        let originalSel = #selector(UIViewController.viewDidAppear(_:))
        let swizzledSel = #selector(UIViewController.caliper_viewDidAppear(_:))
        guard let origMethod = class_getInstanceMethod(UIViewController.self, originalSel),
              let swizMethod = class_getInstanceMethod(UIViewController.self, swizzledSel) else {
            return
        }
        method_exchangeImplementations(origMethod, swizMethod)
    }()

    static func caliperInstallAutoSceneTracking() {
        _ = swizzleOnce
    }

    @objc private func caliper_viewDidAppear(_ animated: Bool) {
        caliper_viewDidAppear(animated)
        let clsName = String(describing: type(of: self))
        // Skip Apple system container VCs (UINavigationController, _UI*, etc.)
        guard !clsName.hasPrefix("UI"), !clsName.hasPrefix("_UI"), !clsName.hasPrefix("SF") else {
            return
        }
        CaliperIOSAgent.setScene(clsName)
    }
}
#endif

#if canImport(SwiftUI)
@available(iOS 15.0, macOS 13.0, *)
public extension View {
    /// Lightweight SwiftUI scene modifier syncing active screen into CaliperClient, CrashReporter, and FOOM detector.
    func caliperScene(_ name: String) -> some View {
        self.onAppear {
            CaliperIOSAgent.setScene(name)
        }
    }
}
#endif
