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

        let prevHandler = NSGetUncaughtExceptionHandler()
        let client = makeTestClient()
        let reporter = CaliperNativeCrashReporter(directoryURL: dir)
        let ingested = reporter.install(client: client, enableMachHandler: true, scene: "home_feed")
        XCTAssertEqual(ingested, 0)
        XCTAssertNotNil(NSGetUncaughtExceptionHandler())
        reporter.setScene("article_detail")
        reporter.uninstall()
        // Previous NSUncaughtExceptionHandler pointer must be cleanly restored.
        XCTAssertEqual(
            unsafeBitCast(NSGetUncaughtExceptionHandler(), to: UInt.self),
            unsafeBitCast(prevHandler, to: UInt.self)
        )
    }

    func testNSExceptionTombstoneWriteDedupAndParse() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-nsexception-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }

        let client = makeTestClient()
        let reporter = CaliperNativeCrashReporter(directoryURL: dir)
        _ = reporter.install(client: client, enableMachHandler: false, scene: "checkout_confirm")
        defer { reporter.uninstall() }

        let addrs = Thread.callStackReturnAddresses.map { UInt(truncating: $0) }
        let first = CaliperNativeCrashReporter.writeNSExceptionTombstone(
            name: "NSRangeException",
            reason: "index 5 beyond bounds [0 .. 2]\nline2",
            returnAddresses: addrs
        )
        XCTAssertEqual(first, 0, "first NSException tombstone write should succeed")

        // Second write (e.g., trailing abort()/SIGABRT) must be deduplicated cleanly.
        let second = CaliperNativeCrashReporter.writeNSExceptionTombstone(
            name: "SIGABRT",
            reason: "duplicate",
            returnAddresses: addrs
        )
        XCTAssertEqual(second, 1, "subsequent crash write in same process must deduplicate")

        let files = reporter.pendingTombstoneFiles()
        XCTAssertEqual(files.count, 1)
        let raw = try String(contentsOf: files[0], encoding: .utf8)
        let parsed = try XCTUnwrap(CaliperNativeCrashReporter.parseTombstone(raw))
        XCTAssertEqual(parsed.mechanism, "nsexception")
        XCTAssertEqual(parsed.exception, "NSRangeException")
        XCTAssertEqual(parsed.reason, "index 5 beyond bounds [0 .. 2] line2")
        XCTAssertEqual(parsed.scene, "checkout_confirm")
        XCTAssertFalse(parsed.frames.isEmpty)

        XCTAssertEqual(reporter.ingestPendingReports(into: client), 1)
        XCTAssertEqual(client.pendingCount(), 1)
    }

    func testRealSubprocessCorruptedFramePointerSafeUnwind() throws {
        #if os(macOS)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-corrupt-fp-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }

        let thisFile = URL(fileURLWithPath: #filePath)
        let pkgRoot = thisFile.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let cSrc = pkgRoot.appendingPathComponent("Sources/CaliperCrashCore/caliper_crash_core.c")
        let incDir = pkgRoot.appendingPathComponent("Sources/CaliperCrashCore/include")

        let helperSrc = dir.appendingPathComponent("corrupt_fp_helper.c")
        let helperBin = dir.appendingPathComponent("corrupt_fp_helper")
        let helperCode = """
        #include "caliper_crash_core.h"
        __attribute__((noinline))
        static void trigger_corrupted_fp_crash(void) {
        #if defined(__arm64__) || defined(__aarch64__)
            __asm__ volatile(
                "mov x29, #0x1008\\n"
                "mov x0, #0\\n"
                "ldr x1, [x0]\\n"
                : : : "x0", "x1", "memory"
            );
        #elif defined(__x86_64__)
            __asm__ volatile(
                "movq $0x1008, %%rbp\\n"
                "xorq %%rax, %%rax\\n"
                "movq (%%rax), %%rbx\\n"
                : : : "rax", "rbx", "memory"
            );
        #else
            volatile int *bad = (volatile int *)0x0;
            *bad = 1;
        #endif
        }
        int main(int argc, char **argv) {
            if (argc < 2) return 2;
            caliper_crash_install(argv[1], true);
            caliper_crash_set_scene("corrupt_stack_scene");
            trigger_corrupted_fp_crash();
            return 0;
        }
        """
        try helperCode.write(to: helperSrc, atomically: true, encoding: .utf8)

        let cc = Process()
        cc.executableURL = URL(fileURLWithPath: "/usr/bin/clang")
        cc.arguments = ["-g", "-O1", "-I", incDir.path, "-o", helperBin.path, helperSrc.path, cSrc.path]
        try cc.run()
        cc.waitUntilExit()
        XCTAssertEqual(cc.terminationStatus, 0, "clang compile corrupt_fp_helper failed")

        let child = Process()
        child.executableURL = helperBin
        child.arguments = [dir.path]
        try child.run()
        child.waitUntilExit()
        XCTAssertEqual(child.terminationReason, .uncaughtSignal)

        // vm_read_overwrite must prevent a secondary crash in mach_exception_loop and write 1 valid tombstone!
        let reporter = CaliperNativeCrashReporter(directoryURL: dir)
        let files = reporter.pendingTombstoneFiles()
        XCTAssertEqual(files.count, 1, "expected 1 tombstone even when FP points to unmapped 0x1008")
        let raw = try String(contentsOf: files[0], encoding: .utf8)
        let parsed = try XCTUnwrap(CaliperNativeCrashReporter.parseTombstone(raw))
        XCTAssertEqual(parsed.scene, "corrupt_stack_scene")
        XCTAssertFalse(parsed.frames.isEmpty, "expected at least crashing PC frame to survive corrupt FP")
        XCTAssertFalse(parsed.binaryUUIDs.isEmpty)
        #endif
    }

    func testRealSubprocessNSExceptionCaptureAndSignalDedup() throws {
        #if os(macOS)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-objc-exc-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }

        let thisFile = URL(fileURLWithPath: #filePath)
        let pkgRoot = thisFile.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let cSrc = pkgRoot.appendingPathComponent("Sources/CaliperCrashCore/caliper_crash_core.c")
        let incDir = pkgRoot.appendingPathComponent("Sources/CaliperCrashCore/include")

        let helperSrc = dir.appendingPathComponent("objc_exc_helper.m")
        let helperBin = dir.appendingPathComponent("objc_exc_helper")
        let helperCode = """
        #import <Foundation/Foundation.h>
        #include "caliper_crash_core.h"

        static void test_ns_exc_handler(NSException *exc) {
            NSArray<NSNumber *> *addrs = [exc callStackReturnAddresses];
            uintptr_t pcs[48];
            int n = (int)MIN((NSUInteger)48, [addrs count]);
            for (int i = 0; i < n; i++) {
                pcs[i] = (uintptr_t)[[addrs objectAtIndex:i] unsignedLongLongValue];
            }
            caliper_crash_write_exception_tombstone(
                [[exc name] UTF8String],
                [[exc reason] UTF8String],
                pcs,
                n
            );
        }

        int main(int argc, char **argv) {
            @autoreleasepool {
                if (argc < 2) return 2;
                caliper_crash_install(argv[1], true);
                caliper_crash_set_scene("feed_detail");
                NSSetUncaughtExceptionHandler(&test_ns_exc_handler);
                [NSException raise:NSRangeException format:@"index 99 beyond bounds [0 .. 3]"];
            }
            return 0;
        }
        """
        try helperCode.write(to: helperSrc, atomically: true, encoding: .utf8)

        let cc = Process()
        cc.executableURL = URL(fileURLWithPath: "/usr/bin/clang")
        cc.arguments = ["-fobjc-arc", "-g", "-O1", "-framework", "Foundation", "-I", incDir.path, "-o", helperBin.path, helperSrc.path, cSrc.path]
        try cc.run()
        cc.waitUntilExit()
        XCTAssertEqual(cc.terminationStatus, 0, "clang compile objc_exc_helper failed")

        let child = Process()
        child.executableURL = helperBin
        child.arguments = [dir.path]
        try child.run()
        child.waitUntilExit()
        XCTAssertEqual(child.terminationReason, .uncaughtSignal)

        let reporter = CaliperNativeCrashReporter(directoryURL: dir)
        let files = reporter.pendingTombstoneFiles()
        XCTAssertEqual(files.count, 1, "expected exactly 1 tombstone (NSException deduplicated against trailing SIGABRT)")
        let raw = try String(contentsOf: files[0], encoding: .utf8)
        let parsed = try XCTUnwrap(CaliperNativeCrashReporter.parseTombstone(raw))
        XCTAssertEqual(parsed.mechanism, "nsexception")
        XCTAssertEqual(parsed.exception, "NSRangeException")
        XCTAssertEqual(parsed.reason, "index 99 beyond bounds [0 .. 3]")
        XCTAssertEqual(parsed.scene, "feed_detail")
        XCTAssertFalse(parsed.frames.isEmpty)
        XCTAssertFalse(parsed.binaryUUIDs.isEmpty)
        #endif
    }
}
