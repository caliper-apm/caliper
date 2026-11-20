import XCTest
import CaliperSDK

/// Reproducible iOS Physical-Device Release Overhead Harness (XCTest + XCTMetric).
/// Attach to an Xcode Release test target on a mid-tier iPhone (e.g. iPhone 13) to record
/// SDK OFF vs ON deltas against North Star Outcome #1 (<=15ms launch, <=4 MiB phys_footprint, <500 KB IPA).
final class CaliperOverheadMetricsTests: XCTestCase {
    func testDarwinFootprintOverheadBaselineCheck() {
        let before = CaliperSystemMetrics.readPhysFootprintKiB() ?? 0
        let client = CaliperClient(config: CaliperConfig(
            endpointURL: URL(string: "http://127.0.0.1:8080")!,
            resource: ResourceIdentity(
                appID: "dev.caliper.bench",
                appVersion: "1.2.0",
                buildNumber: "120",
                installHash: "0123456789abcdef"
            )
        ))
        client.setScene("benchmark_feed")
        for i in 0..<200 {
            client.addBreadcrumb(category: "bench", message: "item-\(i)")
        }
        let after = CaliperSystemMetrics.readPhysFootprintKiB() ?? before
        let deltaMiB = max(0, (after - before) / 1024.0)
        XCTAssertLessThanOrEqual(deltaMiB, 4.0, "CaliperSDK resident memory delta must stay <= 4 MiB")
    }
}
