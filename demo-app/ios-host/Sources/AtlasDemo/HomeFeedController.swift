import Foundation
import CaliperSDK

/// Pure Native iOS Feed Controller for Atlas Reader Demo App.
/// Demonstrates zero-RN-dependency integration of CaliperSDK + Chaos Lab triggers.
public final class HomeFeedController {
    private let client: CaliperClient
    private var leakedBuffers: [Data] = []
    private var fluency = FluencyWindowAccumulator()

    public init(client: CaliperClient) {
        self.client = client
    }

    public func onEnterFeed() {
        client.setScene("home_feed")
        client.addBreadcrumb(category: "ui", message: "entered native iOS HomeFeed")
        fluency = FluencyWindowAccumulator()
    }

    /// Chaos Lab #2: Injects degraded CADisplayLink frames + raw instruction address stall stack.
    public func injectScrollJankStall(stallMS: Double = 95.0) {
        client.addBreadcrumb(category: "chaos", message: "injectScrollJankStall(\(Int(stallMS))ms)")
        for i in 0..<60 {
            let dur = (i % 5 == 0) ? stallMS : 15.2
            fluency.recordFrame(durationMS: dur, targetMS: 16.67)
        }
        let payload = FluencyWindowPayload(
            algorithm: "ios_display_link",
            windowDurationMS: 30_000,
            frameCount: fluency.frameCount,
            jankFrameCount: fluency.jankFrameCount,
            severeJankFrameCount: fluency.severeJankFrameCount,
            refreshRateHz: 60,
            frameDurationBuckets: fluency.frameDurationBuckets,
            stallStack: ["0x10004228", "0x10008010"]
        )
        client.enqueue(kind: "fluency_window", payload: .fluency(payload), scene: "home_feed", bypassSampling: true)
    }

    /// Chaos Lab #4: Retains 4 MiB buffers to trigger measurable phys_footprint growth.
    public func injectMemoryRetentionLeak(megabytes: Int = 4) {
        let clamped = max(1, min(megabytes, 32))
        leakedBuffers.append(Data(repeating: 0xAB, count: clamped * 1024 * 1024))
        _ = client.captureResourceWindow(windowDurationMS: 30_000, cpuPct: 24.5, sampleCount: 6, bypassSampling: true)
    }

    /// Chaos Lab #5: Emits raw hex-address iOS crash resolvable via uploaded dSYM symbol map.
    public func emitSimulatedNativeCrash() {
        let payload = StabilityPayload(
            fingerprint: "fp-ios-image-decoder-exc",
            title: "EXC_BAD_ACCESS at 0x10004228",
            fatal: true,
            exceptionType: "EXC_BAD_ACCESS",
            threadName: "com.apple.main-thread",
            frames: [
                StackFrame(module: "AtlasDemo", symbol: "0x10004228", address: "0x10004228", inApp: true),
                StackFrame(module: "AtlasDemo", symbol: "0x10008010", address: "0x10008010", inApp: true),
            ],
            breadcrumbs: client.currentBreadcrumbs()
        )
        client.enqueue(kind: "crash", payload: .stability(payload), scene: "home_feed", bypassSampling: true)
    }
}
