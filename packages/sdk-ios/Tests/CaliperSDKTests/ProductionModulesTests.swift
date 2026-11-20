import XCTest
@testable import CaliperSDK

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
                exp.fulfill()
            }
        }
        wd.start(pingTarget: { _ in })
        wait(for: [exp], timeout: 2.0)
        wd.stop()
    }
}
