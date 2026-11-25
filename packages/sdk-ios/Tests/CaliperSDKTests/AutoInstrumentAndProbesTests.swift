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

    func testW3CTraceparentGenerationAllowlistAndURLRequestInjection() {
        let ctx = CaliperTraceContext.newRoot(sampled: true)
        XCTAssertEqual(ctx.traceID.count, 32)
        XCTAssertEqual(ctx.spanID.count, 16)
        XCTAssertNotEqual(ctx.traceID, String(repeating: "0", count: 32))
        XCTAssertNotEqual(ctx.spanID, String(repeating: "0", count: 16))

        let parsed = CaliperTraceContext.parse(traceparent: ctx.traceparentHeader)
        XCTAssertEqual(parsed, ctx)

        // Reject invalid / all-zero W3C traceparent headers
        XCTAssertNil(CaliperTraceContext.parse(traceparent: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"))
        XCTAssertNil(CaliperTraceContext.parse(traceparent: "00-00000000000000000000000000000000-00f067aa0ba902b7-01"))
        XCTAssertNil(CaliperTraceContext.parse(traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"))

        let collector = URL(string: "https://apm.mycorp.com/v1/ingest")!
        let apiURL = URL(string: "https://checkout.mycorp.com/v1/pay")!
        let thirdPartyURL = URL(string: "https://analytics.external.net/ping")!

        XCTAssertTrue(CaliperTraceContext.shouldPropagate(url: apiURL, targets: [".mycorp.com"], collectorEndpointURL: collector))
        XCTAssertFalse(CaliperTraceContext.shouldPropagate(url: thirdPartyURL, targets: [".mycorp.com"], collectorEndpointURL: collector))
        // Wildcard must NOT leak or loop onto collector host itself
        XCTAssertFalse(CaliperTraceContext.shouldPropagate(url: collector, targets: ["*"], collectorEndpointURL: collector))

        // Inject fresh traceparent
        let (injectedReq, injectedCtx) = CaliperTraceContext.inject(
            into: URLRequest(url: apiURL),
            targets: [".mycorp.com"],
            collectorEndpointURL: collector
        )
        XCTAssertNotNil(injectedCtx)
        XCTAssertEqual(injectedReq.value(forHTTPHeaderField: "traceparent"), injectedCtx?.traceparentHeader)

        // Preserve existing host traceparent without overwriting
        let existingHeader = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
        var presetReq = URLRequest(url: apiURL)
        presetReq.setValue(existingHeader, forHTTPHeaderField: "traceparent")
        let (adoptedReq, adoptedCtx) = CaliperTraceContext.inject(
            into: presetReq,
            targets: [".mycorp.com"],
            collectorEndpointURL: collector
        )
        XCTAssertEqual(adoptedReq.value(forHTTPHeaderField: "traceparent"), existingHeader)
        XCTAssertEqual(adoptedCtx?.traceID, "4bf92f3577b34da6a3ce929d0e0e4736")
        XCTAssertEqual(adoptedCtx?.spanID, "00f067aa0ba902b7")
    }

    func testAutoURLSessionDelegateRealLoopbackHTTPTraceparentEndToEnd() throws {
        // Spin up a real ephemeral POSIX TCP HTTP/1.1 responder on 127.0.0.1:0
        let listenFD = socket(AF_INET, SOCK_STREAM, 0)
        XCTAssertGreaterThanOrEqual(listenFD, 0)
        defer { close(listenFD) }
        var yes: Int32 = 1
        setsockopt(listenFD, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))

        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = 0
        addr.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))
        let bindRes = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.bind(listenFD, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        XCTAssertEqual(bindRes, 0)
        XCTAssertEqual(listen(listenFD, 4), 0)

        var boundAddr = sockaddr_in()
        var boundLen = socklen_t(MemoryLayout<sockaddr_in>.size)
        _ = withUnsafeMutablePointer(to: &boundAddr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                getsockname(listenFD, $0, &boundLen)
            }
        }
        let port = UInt16(bigEndian: boundAddr.sin_port)

        let capturedReqExpectation = expectation(description: "server received HTTP request")
        var receivedRequestText = ""
        DispatchQueue.global(qos: .userInitiated).async {
            let connFD = accept(listenFD, nil, nil)
            guard connFD >= 0 else { return }
            defer { close(connFD) }
            var buf = [UInt8](repeating: 0, count: 4096)
            let n = recv(connFD, &buf, buf.count, 0)
            if n > 0 {
                receivedRequestText = String(decoding: buf.prefix(n), as: UTF8.self)
            }
            let body = #"{"ok":true}"#
            let resp = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n\(body)"
            _ = resp.withCString { send(connFD, $0, strlen($0), 0) }
            capturedReqExpectation.fulfill()
        }

        let client = CaliperClient(config: CaliperConfig(
            endpointURL: URL(string: "https://collector.example.com/v1/ingest")!,
            resource: ResourceIdentity(
                appID: "dev.caliper.demo",
                appVersion: "1.9.0",
                buildNumber: "190",
                installHash: "0123456789abcdef"
            ),
            tracePropagationTargets: ["127.0.0.1"]
        ))
        client.applyLease(PolicyLease(
            policyID: "pol-trace-e2e",
            ratePPM: 1_000_000,
            issuedAtMS: Int64(Date().timeIntervalSince1970 * 1000) - 1000,
            leaseTTLMS: 3_600_000,
            maxEvents24h: 100,
            enabledCollectors: ["network"]
        ))

        let (session, proxy) = CaliperAutoURLSessionDelegate.makeSession(client: client, configuration: .ephemeral)
        defer { session.invalidateAndCancel() }

        let req = proxy.instrumentedRequest(URLRequest(url: URL(string: "http://127.0.0.1:\(port)/v1/checkout/pay?token=secret")!))
        let sentHeader = req.value(forHTTPHeaderField: "traceparent")
        XCTAssertNotNil(sentHeader)
        let expectedCtx = CaliperTraceContext.parse(traceparent: sentHeader)
        XCTAssertNotNil(expectedCtx)

        let done = expectation(description: "URLSession completed")
        session.dataTask(with: req) { _, response, error in
            XCTAssertNil(error)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
            done.fulfill()
        }.resume()

        wait(for: [capturedReqExpectation, done], timeout: 5.0)
        // Give URLSessionTaskMetrics delegate callback + worker queue a brief beat to enqueue
        Thread.sleep(forTimeInterval: 0.1)

        XCTAssertTrue(
            receivedRequestText.lowercased().contains("traceparent: \(sentHeader!.lowercased())"),
            "Server must receive exact W3C traceparent header over wire, got:\n\(receivedRequestText)"
        )
        XCTAssertEqual(client.pendingCount(), 1, "CaliperAutoURLSessionDelegate must enqueue 1 network_transport event")
    }

    func testIOSAgentColdStartAndResourceSampling() {
        let stateDir = FileManager.default.temporaryDirectory.appendingPathComponent("caliper-agent-auto-\(UUID().uuidString)")
        defer {
            CaliperIOSAgent.resetForTesting()
            try? FileManager.default.removeItem(at: stateDir)
        }

        let cfg = CaliperConfig(
            endpointURL: URL(string: "http://127.0.0.1:8080")!,
            resource: ResourceIdentity(
                appID: "dev.caliper.demo",
                appVersion: "1.10.0",
                buildNumber: "1100",
                installHash: "0123456789abcdef"
            )
        )
        let client = CaliperIOSAgent.install(
            config: cfg,
            options: CaliperIOSAgentOptions(
                enableNativeCrash: false,
                enableFOOMDetector: false,
                enableWatchdog: false,
                enableMetricKit: false,
                enableFluencyMonitor: false,
                enableResourceMonitor: false,
                autoInstrumentViewControllers: false,
                enableThermalMonitor: false,
                enableDailyDiskAudit: false,
                stateDirectoryURL: stateDir
            )
        )
        client.applyLease(PolicyLease(
            policyID: "pol-auto-ios",
            ratePPM: 1_000_000,
            issuedAtMS: Int64(Date().timeIntervalSince1970 * 1000) - 1000,
            leaseTTLMS: 3_600_000,
            maxEvents24h: 100,
            enabledCollectors: ["startup", "resources"]
        ))

        CaliperIOSAgent.markFirstContentFrame(runtimeInitMS: 12.0)
        // Second call must be idempotent (only one cold start per process launch).
        CaliperIOSAgent.markFirstContentFrame(runtimeInitMS: 99.0)
        CaliperIOSAgent.captureResourceSampleNow()
        XCTAssertEqual(client.pendingCount(), 2)
    }
}
