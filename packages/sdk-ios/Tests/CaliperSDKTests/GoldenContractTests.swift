import XCTest
@testable import CaliperSDK

private struct GoldenFixture: Decodable {
    struct SamplingCase: Decodable {
        let install_seed: String
        let policy_id: String
        let expected_hash_u32: UInt32
        let expected_bucket_ppm: UInt32
        let admitted_at_100000_ppm: Bool?
        let admitted_at_132696_ppm: Bool?
        let admitted_at_132697_ppm: Bool?
        let admitted_at_500000_ppm: Bool?
        let admitted_at_717152_ppm: Bool?
        let admitted_at_30000_ppm: Bool?
        let admitted_at_600000_ppm: Bool?
        let admitted_at_436213_ppm: Bool?
        let admitted_at_436214_ppm: Bool?
    }
    struct FluencyCase: Decodable {
        let name: String
        let frame_count: Int64
        let jank_frame_count: Int64
        let expected_jank_ratio: Double?
        let expected_severity: String
    }
    struct QuotaCase: Decodable {
        let name: String
        let window_ms: Int64
        let cap: Int
        let timestamps_ms: [Int64]
        let expected_admitted: [Bool]
    }
    let deterministic_sampling_fnv1a32: [SamplingCase]
    let duration_histogram_bounds_ms: [Double]
    let fluency_window_cases: [FluencyCase]
    let sliding_quota_24h_cases: [QuotaCase]
}

final class GoldenContractTests: XCTestCase {
    private func loadFixture() throws -> GoldenFixture {
        let testFile = URL(fileURLWithPath: #filePath)
        // packages/sdk-ios/Tests/CaliperSDKTests/GoldenContractTests.swift -> packages/sdk-core/fixtures/contract-vectors.json
        let fixtureURL = testFile
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("sdk-core/fixtures/contract-vectors.json")
        let data = try Data(contentsOf: fixtureURL)
        return try JSONDecoder().decode(GoldenFixture.self, from: data)
    }

    func testGoldenContractParity() throws {
        let fixture = try loadFixture()
        XCTAssertEqual(CaliperContract.durationBoundsMS, fixture.duration_histogram_bounds_ms)

        for tc in fixture.deterministic_sampling_fnv1a32 {
            XCTAssertEqual(CaliperContract.fnv1a32("\(tc.install_seed)|\(tc.policy_id)"), tc.expected_hash_u32)
            XCTAssertEqual(CaliperContract.samplingBucketPPM(installSeed: tc.install_seed, policyID: tc.policy_id), tc.expected_bucket_ppm)
            let checks: [(UInt32, Bool?)] = [
                (100_000, tc.admitted_at_100000_ppm),
                (132_696, tc.admitted_at_132696_ppm),
                (132_697, tc.admitted_at_132697_ppm),
                (500_000, tc.admitted_at_500000_ppm),
                (717_152, tc.admitted_at_717152_ppm),
                (30_000, tc.admitted_at_30000_ppm),
                (600_000, tc.admitted_at_600000_ppm),
                (436_213, tc.admitted_at_436213_ppm),
                (436_214, tc.admitted_at_436214_ppm),
            ]
            for (rate, want) in checks {
                if let want {
                    XCTAssertEqual(CaliperContract.isAdmitted(installSeed: tc.install_seed, policyID: tc.policy_id, ratePPM: rate), want)
                }
            }
        }

        for tc in fixture.fluency_window_cases {
            let got = FluencyWindowAccumulator.classify(frameCount: tc.frame_count, jankFrameCount: tc.jank_frame_count)
            XCTAssertEqual(got.severity, tc.expected_severity, tc.name)
            XCTAssertEqual(got.jankRatio, tc.expected_jank_ratio, tc.name)
        }

        for tc in fixture.sliding_quota_24h_cases {
            var q = SlidingQuota(windowMS: tc.window_ms, cap: tc.cap)
            for (i, ts) in tc.timestamps_ms.enumerated() {
                XCTAssertEqual(q.tryConsume(timestampMS: ts), tc.expected_admitted[i], "\(tc.name)[\(i)]")
            }
        }
    }

    func testDarwinSystemMetricsAndFailClosedLease() throws {
        XCTAssertNotNil(CaliperSystemMetrics.processCPUMilliseconds())
        let memKiB = try XCTUnwrap(CaliperSystemMetrics.physicalFootprintKiB())
        XCTAssertGreaterThan(memKiB, 0)

        let res = ResourceIdentity(
            appID: "dev.caliper.demo",
            appVersion: "1.0.0",
            buildNumber: "1",
            installHash: "0123456789abcdef"
        )
        let client = CaliperClient(config: CaliperConfig(
            endpointURL: URL(string: "http://127.0.0.1:18080")!,
            resource: res,
            installSeed: "00000000-0000-4000-8000-000000000001"
        ))

        // Without lease: must fail closed and drop sampled events.
        XCTAssertFalse(client.captureResourceWindow(windowDurationMS: 30_000, cpuPct: 12.0))
        XCTAssertEqual(client.pendingCount(), 0)

        // With active admitted lease (bucket=132696, rate=200000): must admit up to cap=2.
        let now: Int64 = 1_763_540_000_000
        client.applyLease(PolicyLease(
            policyID: "fluency_v1",
            ratePPM: 200_000,
            issuedAtMS: now,
            leaseTTLMS: 60_000,
            maxEvents24h: 2,
            enabledCollectors: ["resources"]
        ))
        XCTAssertTrue(client.enqueue(kind: "resource_window", payload: .resource(ResourceWindowPayload(windowDurationMS: 30_000, cpuPct: 10, memoryPeakKiB: memKiB, sampleCount: 1)), occurredAtMS: now + 1000))
        XCTAssertTrue(client.enqueue(kind: "resource_window", payload: .resource(ResourceWindowPayload(windowDurationMS: 30_000, cpuPct: 11, memoryPeakKiB: memKiB, sampleCount: 1)), occurredAtMS: now + 2000))
        // 3rd exceeds cap=2 -> dropped
        XCTAssertFalse(client.enqueue(kind: "resource_window", payload: .resource(ResourceWindowPayload(windowDurationMS: 30_000, cpuPct: 12, memoryPeakKiB: memKiB, sampleCount: 1)), occurredAtMS: now + 3000))
        // Expired lease -> dropped
        XCTAssertFalse(client.enqueue(kind: "resource_window", payload: .resource(ResourceWindowPayload(windowDurationMS: 30_000, cpuPct: 12, memoryPeakKiB: memKiB, sampleCount: 1)), occurredAtMS: now + 120_000))
        XCTAssertEqual(client.pendingCount(), 2)
    }

    func testAbnormalExitMarkerLifecycle() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: dir) }
        let marker = CaliperAbnormalExitMarker(directory: dir)
        XCTAssertFalse(marker.checkPreviousAbnormalExitAndMarkActive())
        // Simulate ungraceful crash / OOM (no markCleanExit called):
        XCTAssertTrue(marker.checkPreviousAbnormalExitAndMarkActive())
        // Clean exit clears marker:
        marker.markCleanExit()
        XCTAssertFalse(marker.checkPreviousAbnormalExitAndMarkActive())
    }
}
