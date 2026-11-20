import Foundation
import CaliperSDK

/// Minimal Pure-iOS Integration Example (15 lines, ZERO React Native dependencies).
public enum PureIOSBootstrap {
    public static func start(endpointURL: URL = URL(string: "http://127.0.0.1:8080")!) -> CaliperClient {
        let client = CaliperClient(config: CaliperConfig(
            endpointURL: endpointURL,
            resource: ResourceIdentity(
                appID: "dev.caliper.pureios",
                platform: "ios",
                env: "prod",
                appVersion: "1.0.0",
                buildNumber: "100",
                installHash: "8877665544332211"
            )
        ))
        let nowMS = Int64(Date().timeIntervalSince1970 * 1000)
        client.applyLease(PolicyLease(
            policyID: "default_v1",
            ratePPM: 1_000_000,
            issuedAtMS: nowMS,
            leaseTTLMS: 86_400_000,
            maxEvents24h: 200,
            enabledCollectors: ["startup", "crash", "fluency", "resource", "network"]
        ))
        client.setScene("home_feed")
        _ = client.captureResourceWindow(windowDurationMS: 30_000, cpuPct: 12.5, sampleCount: 6)
        return client
    }
}
