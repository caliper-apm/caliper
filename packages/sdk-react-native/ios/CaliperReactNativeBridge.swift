import Foundation
import CaliperSDK

/// React Native Objective-C / Swift TurboModule bridge matching TypeScript `CaliperNativeBridgeSpec`.
/// Forwards JS LongTask windows, logical API denominator summaries, route scenes, custom journeys,
/// first-frame readiness, and uncaught JS errors directly into Native `CaliperClient`.
@objc(CaliperReactNativeBridge)
public final class CaliperReactNativeBridge: NSObject {
    private let client: CaliperClient?

    @objc public static func requiresMainQueueSetup() -> Bool {
        false
    }

    public init(client: CaliperClient? = nil) {
        self.client = client
        super.init()
    }

    private func activeClient() -> CaliperClient? {
        client ?? CaliperIOSAgent.client()
    }

    @objc(setScene:)
    public func setScene(_ scene: String) {
        CaliperIOSAgent.setScene(scene)
        client?.setScene(scene)
    }

    @objc(addBreadcrumb:message:)
    public func addBreadcrumb(_ category: String, message: String) {
        activeClient()?.addBreadcrumb(category: category, message: message)
    }

    @objc(notifyFirstFrameReady:)
    public func notifyFirstFrameReady(_ jsEntryToFirstFrameMs: Double) {
        guard jsEntryToFirstFrameMs.isFinite, jsEntryToFirstFrameMs >= 0 else { return }
        CaliperIOSAgent.markFirstContentFrame(runtimeInitMS: jsEntryToFirstFrameMs)
    }

    @objc(enqueueEvent:scene:windowId:payloadJson:)
    public func enqueueEvent(
        _ kind: String,
        scene: String,
        windowId: String?,
        payloadJson: String
    ) {
        guard let target = activeClient(), let data = payloadJson.data(using: .utf8) else { return }
        let decoder = JSONDecoder()
        let effectiveScene = scene.isEmpty ? nil : scene
        let bypass = (kind == "crash" || kind == "anr" || kind == "abnormal_exit")

        switch kind {
        case "js_longtask_window":
            if let p = try? decoder.decode(JSLongTaskPayload.self, from: data) {
                target.enqueue(kind: kind, payload: .jsLongTask(p), scene: effectiveScene, windowID: windowId)
            }
        case "network_summary":
            if let p = try? decoder.decode(NetworkSummaryPayload.self, from: data) {
                target.enqueue(kind: kind, payload: .networkSummary(p), scene: effectiveScene, windowID: windowId)
            }
        case "crash", "anr", "abnormal_exit":
            if let p = try? decoder.decode(StabilityPayload.self, from: data) {
                target.enqueue(kind: kind, payload: .stability(p), scene: effectiveScene, windowID: windowId, bypassSampling: bypass)
                Task { _ = try? await target.flush() }
            }
        case "custom_scene":
            if let p = try? decoder.decode(CustomScenePayload.self, from: data) {
                target.enqueue(kind: kind, payload: .customScene(p), scene: effectiveScene, windowID: windowId)
            }
        default:
            break
        }
    }

    @objc public func recordJSLongTaskWindow(
        windowDurationMS: Int64,
        longTaskCount: Int64,
        totalBlockingMS: Double,
        maxTaskMS: Double,
        scene: String?
    ) {
        activeClient()?.enqueue(
            kind: "js_longtask_window",
            payload: .jsLongTask(JSLongTaskPayload(
                windowDurationMS: windowDurationMS,
                longTaskCount: longTaskCount,
                totalBlockingMS: totalBlockingMS,
                maxTaskMS: maxTaskMS
            )),
            scene: scene
        )
    }

    @objc public func flush() {
        guard let target = activeClient() else { return }
        Task { _ = try? await target.flush() }
    }
}
