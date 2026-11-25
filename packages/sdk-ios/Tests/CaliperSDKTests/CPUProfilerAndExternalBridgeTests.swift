import XCTest
@testable import CaliperSDK

@_silgen_name("caliper_test_cpu_profile_hot_marker")
@inline(never)
private func caliperTestCPUProfileHotMarker(durationSec: TimeInterval) {
    Thread.sleep(forTimeInterval: durationSec)
}

final class CPUProfilerAndExternalBridgeTests: XCTestCase {
    private func makeActiveClient() -> CaliperClient {
        let c = CaliperClient(config: CaliperConfig(
            endpointURL: URL(string: "http://127.0.0.1:8080")!,
            resource: ResourceIdentity(
                appID: "dev.caliper.demo",
                appVersion: "1.10.0",
                buildNumber: "1100",
                installHash: "0123456789abcdef"
            )
        ))
        c.applyLease(PolicyLease(
            policyID: "pol-p1-bridge",
            ratePPM: 1_000_000,
            issuedAtMS: Int64(Date().timeIntervalSince1970 * 1000) - 1000,
            leaseTTLMS: 3_600_000,
            maxEvents24h: 200,
            enabledCollectors: ["startup", "crash", "anr", "fluency", "resources", "network", "custom_scene", "cpu_profile", "heap_snapshot"]
        ))
        return c
    }

    func testIOSCPUProfilerRefusesSelfSuspendOnMainThreadAndSamplesMainThreadOffMain() {
        let profiler = CaliperIOSCPUProfiler()
        if Thread.isMainThread {
            XCTAssertNil(profiler.sampleMainThreadBlocking(durationMS: 60, intervalMS: 10))
        }

        let client = makeActiveClient()
        let exp = expectation(description: "async mach cpu profile")
        var captured: CPUProfilePayload?
        profiler.sampleMainThreadAsync(
            client: client,
            scene: "video_feed",
            durationMS: 180,
            intervalMS: 15,
            bypassSampling: true
        ) { payload in
            captured = payload
            exp.fulfill()
        }

        // Keep main thread parked inside an identifiable hot frame while background profiler samples via Mach.
        caliperTestCPUProfileHotMarker(durationSec: 0.24)
        wait(for: [exp], timeout: 3.0)

        guard let profile = captured else {
            XCTFail("expected non-nil CPUProfilePayload from Mach profiler")
            return
        }
        XCTAssertEqual(profile.runtime, "native")
        XCTAssertEqual(profile.threadName, "main")
        XCTAssertGreaterThan(profile.totalSamples, 0)
        XCTAssertFalse(profile.stacks.isEmpty)
        let allFrames = profile.stacks.flatMap(\.frames)
        XCTAssertTrue(
            allFrames.contains(where: { $0.contains("caliper_test_cpu_profile_hot_marker") }),
            "expected main-thread hot marker in folded flamegraph stacks, got: \(allFrames)"
        )
        XCTAssertEqual(client.pendingCount(), 1)
    }

    func testStrippedInAppFramesFormatAsBareHexForServerDWARFResolveLines() {
        let frames = [
            StackFrame(module: "AtlasReader", symbol: "0x42bc", address: "0x42bc", inApp: true),
            StackFrame(module: "UIKitCore", symbol: "-[UIApplication sendEvent:]", address: "0x1100", inApp: false),
        ]
        let formatted = CaliperIOSCPUProfiler.formatRootToLeafFrames(frames)
        // Reversed root-to-leaf: UIKitCore first, stripped in-app leaf bare hex last.
        XCTAssertEqual(formatted, ["UIKitCore::-[UIApplication sendEvent:]", "0x42bc"])
    }

    func testExternalProbeBridgeRoutesCrashHangHeapAndDiagnosticIntoCaliperPipeline() {
        let client = makeActiveClient()

        // 1. KSCrash / Matrix-iOS WCCrashBlockMonitor crash
        XCTAssertTrue(CaliperExternalProbeBridge.ingestExternalCrash(
            client: client,
            source: "matrix-ios",
            exceptionType: "EXC_BAD_ACCESS",
            title: "KSCrash EXC_BAD_ACCESS in ImageDecoder",
            frames: [
                StackFrame(module: "AtlasReader", symbol: "0x8a20", address: "0x8a20", inApp: true),
                StackFrame(module: "CoreFoundation", symbol: "0x1200", address: "0x1200", inApp: false),
            ],
            scene: "media_gallery"
        ))

        // 2. Matrix-iOS WCBlockMonitorMgr / btrace lag
        XCTAssertTrue(CaliperExternalProbeBridge.ingestExternalHangOrLag(
            client: client,
            source: "btrace",
            durationMS: 1420,
            frames: [
                StackFrame(module: "AtlasReader", symbol: "0x9100", address: "0x9100", inApp: true),
            ],
            traceReference: "perfetto://trace-20261125-001",
            scene: "home_feed"
        ))

        // 3. Matrix-iOS WCMemoryStat / custom malloc dominators -> heap_snapshot
        XCTAssertTrue(CaliperExternalProbeBridge.ingestExternalHeapSnapshot(
            client: client,
            triggerReason: "oom_warning",
            memoryKind: "ios_phys_footprint_kib",
            totalAllocatedKiB: 420_000,
            totalRetainedKiB: 318_000,
            memoryLimitKiB: 1_400_000,
            dominators: [
                HeapDominator(className: "UIImageCacheEntry", instanceCount: 412, retainedKiB: 96_400, retainerPath: ["FeedCache", "ImageStore"]),
                HeapDominator(className: "CVPixelBuffer", instanceCount: 18, retainedKiB: 142_000),
            ],
            scene: "media_gallery"
        ))

        // Empty/invalid dominators must be rejected honestly (Zero-Lie)
        XCTAssertFalse(CaliperExternalProbeBridge.ingestExternalHeapSnapshot(
            client: client,
            totalAllocatedKiB: 100,
            totalRetainedKiB: 50,
            dominators: []
        ))

        // 4. External specialty diagnostic journey (e.g. btrace checkpoint / Matrix FPS)
        XCTAssertTrue(CaliperExternalProbeBridge.ingestExternalDiagnosticJourney(
            client: client,
            source: "matrix_ios",
            diagnosticName: "main_thread_io",
            passed: false,
            durationMS: 185.0,
            stages: [SceneStage(name: "sqlite_wal_sync", durationMS: 142.0)],
            errorCode: "MAIN_THREAD_IO_VIOLATION",
            scene: "article_detail"
        ))

        XCTAssertEqual(client.pendingCount(), 4)
    }
}
