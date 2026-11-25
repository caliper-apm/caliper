import XCTest
@testable import CaliperSDK

@_silgen_name("caliper_test_main_thread_stall_marker")
@inline(never)
private func caliperTestMainThreadStallMarker(durationSec: TimeInterval) {
    Thread.sleep(forTimeInterval: durationSec)
}

final class ProductionModulesTests: XCTestCase {
    func testDiskQueuePersistsEvictsAndDrainsInFIFOOrder() {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-ios-spool-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }

        let q = CaliperDiskQueue(directoryURL: dir, maxFiles: 3, maxTotalBytes: 64 * 1024)
        XCTAssertTrue(q.enqueue(Data("{\"n\":1}".utf8)))
        XCTAssertTrue(q.enqueue(Data("{\"n\":2}".utf8)))
        XCTAssertTrue(q.enqueue(Data("{\"n\":3}".utf8)))
        XCTAssertTrue(q.enqueue(Data("{\"n\":4}".utf8)))

        XCTAssertEqual(q.count(), 3)
        var drained: [String] = []
        let sent = q.drainEach { data in
            drained.append(String(decoding: data, as: UTF8.self))
        }
        XCTAssertEqual(sent, 3)
        XCTAssertEqual(q.count(), 0)
        XCTAssertEqual(drained.first, "{\"n\":2}")
        XCTAssertEqual(drained.last, "{\"n\":4}")
    }

    func testDiagnosticRingLogScrubsPII() {
        let log = CaliperDiagnosticRingLog(maxLines: 10)
        log.append(level: "INFO", tag: "Net", message: "user bob@example.com sent Bearer tok_abc123")
        let dump = log.dumpText()
        XCTAssertFalse(dump.contains("bob@example.com"))
        XCTAssertFalse(dump.contains("tok_abc123"))
        XCTAssertTrue(dump.contains("[REDACTED_EMAIL]"))
        XCTAssertTrue(dump.contains("[REDACTED_TOKEN]"))
    }

    func testMainThreadWatchdogFiresOnStall() {
        final class OnceFlag: @unchecked Sendable {
            private let lock = NSLock()
            private var fired = false
            func trySet() -> Bool {
                lock.lock()
                defer { lock.unlock() }
                if fired { return false }
                fired = true
                return true
            }
        }
        let exp = expectation(description: "watchdog hang callback")
        let once = OnceFlag()
        let wd = CaliperMainThreadWatchdog(thresholdMS: 60, pollIntervalMS: 20) { durMS, frames in
            if once.trySet() {
                XCTAssertGreaterThanOrEqual(durMS, 50)
                XCTAssertFalse(frames.isEmpty)
                XCTAssertFalse(
                    frames.contains(where: { $0.contains("CaliperMainThreadWatchdog") }),
                    "Watchdog must capture main thread frames, never its own background queue stack"
                )
                exp.fulfill()
            }
        }
        wd.start(pingTarget: { _ in })
        wait(for: [exp], timeout: 2.0)
        wd.stop()
    }

    func testMainThreadWatchdogCapturesRealMainThreadFramesWithoutSelfDeadlock() {
        // Calling on the main thread itself must return [] rather than self-suspending.
        if Thread.isMainThread {
            XCTAssertTrue(CaliperMainThreadWatchdog.captureMainThreadFrames().isEmpty)
        }

        final class CapturedBox: @unchecked Sendable {
            private let lock = NSLock()
            var frames: [StackFrame] = []
            var durMS: Int = 0
            func record(dur: Int, frames: [StackFrame]) -> Bool {
                lock.lock()
                defer { lock.unlock() }
                guard self.frames.isEmpty else { return false }
                self.durMS = dur
                self.frames = frames
                return true
            }
        }

        let box = CapturedBox()
        let wd = CaliperMainThreadWatchdog(thresholdMS: 50, pollIntervalMS: 20, onHangFrames: { durMS, frames in
            _ = box.record(dur: durMS, frames: frames)
        })
        wd.start(pingTarget: { _ in })
        // Deliberately stall the main thread inside an unmangled marker function while Watchdog fires.
        caliperTestMainThreadStallMarker(durationSec: 0.25)
        wd.stop()

        XCTAssertGreaterThanOrEqual(box.durMS, 45)
        XCTAssertFalse(box.frames.isEmpty, "expected real main-thread Mach frames on hang")
        XCTAssertTrue(box.frames.allSatisfy { ($0.address ?? "").hasPrefix("0x") })
        XCTAssertFalse(
            box.frames.contains(where: { $0.symbol.contains("CaliperMainThreadWatchdog") }),
            "captured stack must belong to main thread, not watchdog worker thread: \(box.frames)"
        )
        XCTAssertTrue(
            box.frames.contains(where: { $0.symbol.contains("caliper_test_main_thread_stall_marker") }),
            "expected main-thread stall marker in captured frames, got: \(box.frames.map(\.symbol))"
        )
    }

    func testMetricKitCallStackParserDirectWrappedHotBranchAndFallback() throws {
        // 1. Direct MXCallStackTree.jsonRepresentation() with 2 threads -> selects threadAttributed=true & leaf-first order
        let directJSON = """
        {
          "callStackPerThread": true,
          "callStacks": [
            {
              "threadAttributed": false,
              "callStackRootFrames": [
                {
                  "binaryName": "libsystem_pthread.dylib",
                  "binaryUUID": "00000000-0000-0000-0000-000000000001",
                  "offsetIntoBinaryTextSegment": 4096
                }
              ]
            },
            {
              "threadAttributed": true,
              "callStackRootFrames": [
                {
                  "binaryName": "UIKitCore",
                  "binaryUUID": "11111111-2222-3333-4444-555555555555",
                  "offsetIntoBinaryTextSegment": 14500,
                  "sampleCount": 1,
                  "subFrames": [
                    {
                      "binaryName": "AtlasReader",
                      "binaryUUID": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
                      "offsetIntoBinaryTextSegment": 28832,
                      "sampleCount": 1,
                      "subFrames": [
                        {
                          "binaryName": "AtlasReader",
                          "binaryUUID": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
                          "offsetIntoBinaryTextSegment": 32768,
                          "sampleCount": 1
                        }
                      ]
                    }
                  ]
                }
              ]
            }
          ]
        }
        """
        let direct = CaliperMetricKitCallStackParser.parse(jsonData: Data(directJSON.utf8))
        XCTAssertTrue(direct.threadAttributed)
        XCTAssertEqual(direct.frames.count, 3)
        // Leaf-first: crashing frame (32768 = 0x8000) is index 0, UIKitCore root is last.
        XCTAssertEqual(direct.frames[0].module, "AtlasReader")
        XCTAssertEqual(direct.frames[0].address, "0x8000")
        XCTAssertEqual(direct.frames[0].inApp, true)
        XCTAssertEqual(direct.frames[1].address, "0x70a0")
        XCTAssertEqual(direct.frames[2].module, "UIKitCore")
        XCTAssertEqual(direct.frames[2].inApp, false)
        XCTAssertEqual(direct.binaryUUIDs, [
            "11111111-2222-3333-4444-555555555555",
            "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
        ])

        // 2. Wrapped {"callStackTree": ...} hang tree with competing subFrames -> follows hottest sampleCount branch
        let wrappedJSON = """
        {
          "callStackTree": {
            "callStackPerThread": true,
            "callStacks": [
              {
                "threadAttributed": true,
                "callStackRootFrames": [
                  {
                    "binaryName": "CoreFoundation",
                    "binaryUUID": "22222222-2222-2222-2222-222222222222",
                    "offsetIntoBinaryTextSegment": 4096,
                    "sampleCount": 50,
                    "subFrames": [
                      {
                        "binaryName": "AtlasReader",
                        "binaryUUID": "BBBBBBBB-BBBB-BBBB-BBBB-BBBBBBBBBBBB",
                        "offsetIntoBinaryTextSegment": 4096,
                        "sampleCount": 3
                      },
                      {
                        "binaryName": "AtlasReader",
                        "binaryUUID": "BBBBBBBB-BBBB-BBBB-BBBB-BBBBBBBBBBBB",
                        "offsetIntoBinaryTextSegment": 65536,
                        "sampleCount": 47
                      }
                    ]
                  }
                ]
              }
            ]
          }
        }
        """
        let wrapped = CaliperMetricKitCallStackParser.parse(jsonData: Data(wrappedJSON.utf8))
        XCTAssertEqual(wrapped.frames.count, 2)
        XCTAssertEqual(wrapped.frames[0].module, "AtlasReader")
        XCTAssertEqual(wrapped.frames[0].address, "0x10000") // 65536 = 0x10000 (sampleCount 47 branch)
        XCTAssertEqual(wrapped.frames[1].module, "CoreFoundation")
        XCTAssertEqual(wrapped.frames[1].inApp, false)

        // 3. Malformed / empty input degrades safely with empty frames
        XCTAssertTrue(CaliperMetricKitCallStackParser.parse(jsonData: Data()).frames.isEmpty)
        XCTAssertTrue(CaliperMetricKitCallStackParser.parse(jsonData: Data("not-json".utf8)).frames.isEmpty)
    }
}
