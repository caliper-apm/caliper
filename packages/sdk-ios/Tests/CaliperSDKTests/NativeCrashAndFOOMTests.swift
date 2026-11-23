import XCTest
@testable import CaliperSDK

final class NativeCrashAndFOOMTests: XCTestCase {
    private func makeTestClient() -> CaliperClient {
        CaliperClient(config: CaliperConfig(
            endpointURL: URL(string: "http://127.0.0.1:8080")!,
            resource: ResourceIdentity(
                appID: "dev.caliper.demo",
                appVersion: "1.7.0",
                buildNumber: "170",
                installHash: "0123456789abcdef"
            )
        ))
    }

    func testFOOMStateMachineAllNineBranches() {
        let base = FOOMSessionState(
            appVersion: "1.7.0",
            buildNumber: "170",
            osVersion: "18.1",
            bootTimeSec: 1_700_000_000,
            lifecycleState: "foreground",
            detectedCrash: false,
            debuggerAttached: false,
            scene: "media_gallery",
            lastMemoryFootprintKiB: 840 * 1024,
            updatedAtMS: 1_700_005_000_000
        )

        XCTAssertEqual(CaliperFOOMDetector.classify(previous: nil, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.1", currentBootTimeSec: 1_700_000_000, previousCrashDetected: false), .none)

        var clean = base; clean.lifecycleState = "terminated_clean"
        XCTAssertEqual(CaliperFOOMDetector.classify(previous: clean, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.1", currentBootTimeSec: 1_700_000_000, previousCrashDetected: false), .cleanExit)

        XCTAssertEqual(CaliperFOOMDetector.classify(previous: base, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.1", currentBootTimeSec: 1_700_000_000, previousCrashDetected: true), .detectedCrash)

        var dbg = base; dbg.debuggerAttached = true
        XCTAssertEqual(CaliperFOOMDetector.classify(previous: dbg, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.1", currentBootTimeSec: 1_700_000_000, previousCrashDetected: false), .debuggerSession)

        XCTAssertEqual(CaliperFOOMDetector.classify(previous: base, currentAppVersion: "1.7.1", currentBuildNumber: "171", currentOSVersion: "18.1", currentBootTimeSec: 1_700_000_000, previousCrashDetected: false), .appUpgraded)

        XCTAssertEqual(CaliperFOOMDetector.classify(previous: base, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.2", currentBootTimeSec: 1_700_000_000, previousCrashDetected: false), .osUpgraded)

        XCTAssertEqual(CaliperFOOMDetector.classify(previous: base, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.1", currentBootTimeSec: 1_700_003_600, previousCrashDetected: false), .deviceRebooted)

        var bg = base; bg.lifecycleState = "background"
        XCTAssertEqual(CaliperFOOMDetector.classify(previous: bg, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.1", currentBootTimeSec: 1_700_000_000, previousCrashDetected: false), .backgroundKill)

        // Genuine Foreground OOM!
        XCTAssertEqual(CaliperFOOMDetector.classify(previous: base, currentAppVersion: "1.7.0", currentBuildNumber: "170", currentOSVersion: "18.1", currentBootTimeSec: 1_700_000_002, previousCrashDetected: false), .foregroundOOM)
    }

    func testFOOMPersistedSessionEmitsAbnormalExitOnNextLaunch() {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-foom-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }

        let client1 = makeTestClient()
        let detector1 = CaliperFOOMDetector(directoryURL: dir)
        let first = detector1.startSession(
            client: client1, appVersion: "1.7.0", buildNumber: "170", osVersion: "18.1",
            previousCrashDetected: false, bootTimeSec: 1_700_000_000, debuggerAttached: false, initialScene: "media_gallery"
        )
        XCTAssertEqual(first, .none)
        detector1.updateMemoryFootprint(kib: 912 * 1024, scene: "video_editor")
        // Simulate abrupt foreground Jetsam OOM kill (no markCleanTerminate / no markBackground / no crash)

        let client2 = makeTestClient()
        let detector2 = CaliperFOOMDetector(directoryURL: dir)
        let second = detector2.startSession(
            client: client2, appVersion: "1.7.0", buildNumber: "170", osVersion: "18.1",
            previousCrashDetected: false, bootTimeSec: 1_700_000_000, debuggerAttached: false, initialScene: "startup"
        )
        XCTAssertEqual(second, .foregroundOOM)
        XCTAssertEqual(client2.pendingCount(), 1)
    }

    func testRealSubprocessMachAndSignalCrashCapture() throws {
        #if os(macOS)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-crash-e2e-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }

        // Locate CaliperCrashCore C source relative to this test file.
        let thisFile = URL(fileURLWithPath: #filePath)
        let pkgRoot = thisFile.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let cSrc = pkgRoot.appendingPathComponent("Sources/CaliperCrashCore/caliper_crash_core.c")
        let incDir = pkgRoot.appendingPathComponent("Sources/CaliperCrashCore/include")

        let helperSrc = dir.appendingPathComponent("crash_helper.c")
        let helperBin = dir.appendingPathComponent("crash_helper")
        let helperCode = """
        #include "caliper_crash_core.h"
        #include <stdio.h>
        __attribute__((noinline))
        static int trigger_real_null_deref(volatile int *ptr) {
            return *ptr + 42;
        }
        int main(int argc, char **argv) {
            if (argc < 2) return 2;
            caliper_crash_install(argv[1], true);
            caliper_crash_set_scene("video_decoder");
            volatile int *bad = (volatile int *)0x0;
            return trigger_real_null_deref(bad);
        }
        """
        try helperCode.write(to: helperSrc, atomically: true, encoding: .utf8)

        let cc = Process()
        cc.executableURL = URL(fileURLWithPath: "/usr/bin/clang")
        cc.arguments = ["-g", "-O1", "-I", incDir.path, "-o", helperBin.path, helperSrc.path, cSrc.path]
        try cc.run()
        cc.waitUntilExit()
        XCTAssertEqual(cc.terminationStatus, 0, "clang compile crash helper failed")

        // Spawn child process that deliberately triggers EXC_BAD_ACCESS / SIGSEGV!
        let child = Process()
        child.executableURL = helperBin
        child.arguments = [dir.path]
        try child.run()
        child.waitUntilExit()
        XCTAssertEqual(child.terminationReason, .uncaughtSignal, "child should terminate via real OS crash signal")

        // Parent reads & ingests the real tombstone!
        let reporter = CaliperNativeCrashReporter(directoryURL: dir)
        let files = reporter.pendingTombstoneFiles()
        XCTAssertEqual(files.count, 1, "expected exactly 1 tombstone written by real crash")
        let raw = try String(contentsOf: files[0], encoding: .utf8)
        let parsed = try XCTUnwrap(CaliperNativeCrashReporter.parseTombstone(raw))
        XCTAssertTrue(parsed.exception == "EXC_BAD_ACCESS" || parsed.exception == "SIGSEGV", "unexpected exception: \(parsed.exception)")
        XCTAssertEqual(parsed.scene, "video_decoder")
        XCTAssertFalse(parsed.frames.isEmpty, "expected captured native backtrace frames")
        XCTAssertFalse(parsed.binaryUUIDs.isEmpty, "expected Mach-O LC_UUID captured in tombstone")

        let client = makeTestClient()
        let count = reporter.ingestPendingReports(into: client)
        XCTAssertEqual(count, 1)
        XCTAssertEqual(client.pendingCount(), 1)
        XCTAssertTrue(reporter.pendingTombstoneFiles().isEmpty, "ingested tombstone must be deleted")
        #endif
    }

    func testCrashReporterInstallAndCleanUninstallLifecycle() {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-install-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }

        let client = makeTestClient()
        let reporter = CaliperNativeCrashReporter(directoryURL: dir)
        let ingested = reporter.install(client: client, enableMachHandler: true, scene: "home_feed")
        XCTAssertEqual(ingested, 0)
        reporter.setScene("article_detail")
        reporter.uninstall()
    }
}
