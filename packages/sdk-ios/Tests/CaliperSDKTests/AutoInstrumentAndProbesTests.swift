import XCTest
@testable import CaliperSDK

final class AutoInstrumentAndProbesTests: XCTestCase {
    private func makeActiveClient() -> CaliperClient {
        let c = CaliperClient(config: CaliperConfig(
            endpointURL: URL(string: "http://127.0.0.1:8080")!,
            resource: ResourceIdentity(
                appID: "dev.caliper.demo",
                appVersion: "1.8.0",
                buildNumber: "180",
                installHash: "0123456789abcdef"
            )
        ))
        c.applyLease(PolicyLease(
            policyID: "pol-p2",
            ratePPM: 1_000_000,
            issuedAtMS: Int64(Date().timeIntervalSince1970 * 1000) - 1000,
            leaseTTLMS: 3_600_000,
            maxEvents24h: 100,
            enabledCollectors: ["startup", "crash", "anr", "fluency", "resources", "network", "custom_scene"]
        ))
        return c
    }

    func testThermalBatteryProbeDetectsSevereStates() {
        let client = makeActiveClient()
        let ok = ThermalBatterySnapshot(thermalLevel: "nominal", batteryLevelPct: 84, isCharging: false, memoryFootprintKiB: 96 * 1024, scene: "home_feed")
        let hot = ThermalBatterySnapshot(thermalLevel: "critical", batteryLevelPct: 41, isCharging: true, memoryFootprintKiB: 210 * 1024, scene: "video_editor")

        XCTAssertFalse(CaliperThermalBatteryProbe.isSevere(ok))
        XCTAssertTrue(CaliperThermalBatteryProbe.isSevere(hot))
        XCTAssertTrue(CaliperThermalBatteryProbe.record(client: client, snapshot: ok))
        XCTAssertTrue(CaliperThermalBatteryProbe.record(client: client, snapshot: hot))
        XCTAssertEqual(client.pendingCount(), 2)
    }

    func testDiskAuditProbeSanitizesPIIAndEnforcesBounds() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-ios-disk-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: root) }

        let cache = root.appendingPathComponent("cache")
        let sub = cache.appendingPathComponent("user_987654321/550e8400-e29b-41d4-a716-446655440000")
        try FileManager.default.createDirectory(at: sub, withIntermediateDirectories: true)
        try Data(repeating: 0x41, count: 4096).write(to: sub.appendingPathComponent("alice@example.com_0123456789abcdef9999.bin"))

        let sanitized = CaliperDiskAuditProbe.sanitizePath("user_987654321/550e8400-e29b-41d4-a716-446655440000/alice@example.com_0123456789abcdef9999.bin")
        XCTAssertFalse(sanitized.contains("987654321"))
        XCTAssertFalse(sanitized.contains("550e8400"))
        XCTAssertFalse(sanitized.contains("alice@example.com"))
        XCTAssertFalse(sanitized.contains("0123456789abcdef"))

        let report = CaliperDiskAuditProbe.auditDirectories(
            roots: [("cache", cache)],
            largeFileThresholdBytes: 1024
        )
        XCTAssertEqual(report.totalBytes, 4096)
        XCTAssertEqual(report.visitedFiles, 1)
        XCTAssertEqual(report.largeFiles.count, 1)
        XCTAssertFalse(report.largeFiles[0].sanitizedPath.contains("alice@example.com"))

        let client = makeActiveClient()
        XCTAssertTrue(CaliperDiskAuditProbe.recordAudit(client: client, report: report, quotaBudgetMiB: 0.001))
        XCTAssertEqual(client.pendingCount(), 1)
    }

    func testWebViewProbeStripsURLPIIAndIngestsNavBlankAndJSError() {
        let client = makeActiveClient()
        XCTAssertEqual(
            CaliperWebViewProbe.normalizeURL("https://m.example.com/campaign/fall?token=secret&uid=12#banner"),
            "https://m.example.com/campaign/fall"
        )

        let navOk = #"{"type":"nav","url":"https://m.example.com/campaign/fall?token=1","dns_ms":10,"tcp_ms":15,"ttfb_ms":85,"dom_ms":210,"load_ms":390,"fcp_ms":160,"blank":false}"#
        let navBlank = #"{"type":"nav","url":"https://m.example.com/broken","dns_ms":5,"tcp_ms":9,"ttfb_ms":40,"dom_ms":60,"load_ms":70,"fcp_ms":0,"blank":true}"#
        let jsErr = #"{"type":"js_error","url":"https://m.example.com/campaign/fall?auth=xyz","message":"ReferenceError: hydrate is not defined"}"#

        XCTAssertTrue(CaliperWebViewProbe.ingestMessageJSON(navOk, client: client))
        XCTAssertTrue(CaliperWebViewProbe.ingestMessageJSON(navBlank, client: client))
        XCTAssertTrue(CaliperWebViewProbe.ingestMessageJSON(jsErr, client: client))
        XCTAssertEqual(client.pendingCount(), 3)
    }

    func testOneLineCaliperIOSAgentInstallAndReset() {
        let stateDir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-agent-\(UUID().uuidString)")
        defer {
            CaliperIOSAgent.resetForTesting()
            try? FileManager.default.removeItem(at: stateDir)
        }

        let cfg = CaliperConfig(
            endpointURL: URL(string: "http://127.0.0.1:8080")!,
            resource: ResourceIdentity(
                appID: "dev.caliper.demo",
                appVersion: "1.8.0",
                buildNumber: "180",
                installHash: "0123456789abcdef"
            )
        )
        let client = CaliperIOSAgent.install(
            config: cfg,
            options: CaliperIOSAgentOptions(
                enableNativeCrash: true,
                enableFOOMDetector: true,
                enableWatchdog: false,
                enableMetricKit: false,
                enableThermalMonitor: true,
                enableDailyDiskAudit: false,
                stateDirectoryURL: stateDir
            )
        )
        XCTAssertNotNil(CaliperIOSAgent.client())
        XCTAssertEqual(CaliperIOSAgent.lastFOOMClassification(), .none)
        CaliperIOSAgent.setScene("home_feed")
        CaliperIOSAgent.log(level: "INFO", tag: "Test", message: "one-line agent active")
        XCTAssertEqual(client.pendingCount(), 0)
    }
}
