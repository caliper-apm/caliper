import Foundation
import CaliperSDK

/// Interactive Chaos Lab driver for the Atlas Reader iOS Demo Host.
/// Injects controlled faults across all 5 mobile APM pillars and flushes real telemetry to Caliper APM.
public final class AtlasChaosLabRunner: @unchecked Sendable {
    public let client: CaliperClient
    public let ringLog = CaliperDiagnosticRingLog(maxLines: 200)

    public init(endpointURL: URL, appVersion: String = "1.1.0", buildNumber: String = "110") {
        let cfg = CaliperConfig(
            endpointURL: endpointURL,
            resource: ResourceIdentity(
                appID: "dev.caliper.demo",
                platform: "ios",
                env: "prod",
                appVersion: appVersion,
                buildNumber: buildNumber,
                adapter: "native",
                installHash: "fedcba9876543210",
                userHash: "0000111122223333",
                osVersion: "18.1",
                deviceModel: "iPhone16,1"
            )
        )
        self.client = CaliperClient(config: cfg)
        let nowMS = Int64(Date().timeIntervalSince1970 * 1000)
        self.client.applyLease(PolicyLease(
            policyID: "demo_chaos_v1",
            ratePPM: 1_000_000,
            issuedAtMS: nowMS,
            leaseTTLMS: 3_600_000,
            maxEvents24h: 500,
            enabledCollectors: ["startup", "crash", "anr", "fluency", "resource", "network"]
        ))
    }

    public func triggerColdStartRegression() {
        client.enqueue(
            kind: "startup",
            payload: .startup(StartupPayload(
                totalDurationMS: 1840,
                nativeInitMS: 320,
                runtimeInitMS: 420,
                firstFrameMS: 1100,
                coldStart: true,
                startAnchor: "process_start",
                endAnchor: "first_content_frame",
                result: "success"
            )),
            scene: "startup"
        )
        ringLog.append(level: "WARN", tag: "ChaosLab", message: "injected 1840ms cold start regression")
    }

    public func triggerMainThreadJankBurst() {
        client.setScene("home_feed")
        var acc = FluencyWindowAccumulator()
        for i in 0..<120 {
            let dur = (i % 6 == 0) ? 54.0 : 12.0
            acc.recordFrame(durationMS: dur, targetMS: 16.67)
        }
        client.enqueue(
            kind: "fluency_window",
            payload: .fluency(FluencyWindowPayload(
                algorithm: "ios_display_link",
                windowDurationMS: 10_000,
                frameCount: acc.frameCount,
                jankFrameCount: acc.jankFrameCount,
                severeJankFrameCount: acc.severeJankFrameCount,
                refreshRateHz: 60,
                frameDurationBuckets: acc.frameDurationBuckets,
                stallStack: ["0x10004220", "0x10008010"]
            )),
            scene: "home_feed"
        )
        ringLog.append(level: "WARN", tag: "ChaosLab", message: "injected home_feed jank burst")
    }

    public func captureLiveResourceSnapshot() {
        client.setScene("media_gallery")
        _ = client.captureResourceWindow(windowDurationMS: 10_000, cpuPct: 28.4, sampleCount: 6)
    }
}
