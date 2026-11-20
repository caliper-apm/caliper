import Foundation
import CaliperSDK

/// React Native Objective-C / Swift TurboModule bridge forwarding JS LongTask windows,
/// logical API denominator summaries, route scenes, and JS errors into Native CaliperClient.
@objc(CaliperReactNativeBridge)
public final class CaliperReactNativeBridge: NSObject {
    private let client: CaliperClient

    public init(client: CaliperClient) {
        self.client = client
        super.init()
    }

    @objc public func setScene(_ scene: String) {
        client.setScene(scene)
    }

    @objc public func addBreadcrumb(_ category: String, message: String) {
        client.addBreadcrumb(category: category, message: message)
    }

    @objc public func recordJSLongTaskWindow(
        windowDurationMS: Int64,
        longTaskCount: Int64,
        totalBlockingMS: Double,
        maxTaskMS: Double,
        scene: String?
    ) {
        client.enqueue(
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
        Task { _ = try? await client.flush() }
    }
}
