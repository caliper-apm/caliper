package io.caliper.apm

import com.sun.net.httpserver.HttpServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.net.InetSocketAddress
import java.util.zip.GZIPInputStream

class GoldenContractTest {
    private fun loadFixtureText(): String {
        val candidates = listOf(
            File("../../sdk-core/fixtures/contract-vectors.json"),
            File("../sdk-core/fixtures/contract-vectors.json"),
            File("packages/sdk-core/fixtures/contract-vectors.json")
        )
        val file = candidates.firstOrNull { it.exists() }
            ?: error("Could not locate contract-vectors.json from ${File(".").absolutePath}")
        return file.readText()
    }

    @Test
    fun goldenContractParity() {
        val text = loadFixtureText()

        // Verify all 4 deterministic FNV-1a vectors from contract-vectors.json
        val samplingVectors = listOf(
            Triple("00000000-0000-4000-8000-000000000001", "fluency_v1", 245132696L to 132696L),
            Triple("11111111-1111-4111-8111-111111111111", "fluency_v1", 3931717151L to 717151L),
            Triple("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "resource_v1", 1330532230L to 532230L),
            Triple("9f8e7d6c-5b4a-4321-8765-0123456789ab", "network_v1", 3967436213L to 436213L)
        )
        for ((seed, policy, expected) in samplingVectors) {
            assertTrue("Fixture must contain seed $seed", text.contains(seed))
            assertTrue("Fixture must contain expected hash ${expected.first}", text.contains(expected.first.toString()))
            assertEquals(expected.first, CaliperContract.fnv1a32("$seed|$policy"))
            assertEquals(expected.second, CaliperContract.samplingBucketPPM(seed, policy))
            assertFalse(CaliperContract.isAdmitted(seed, policy, expected.second))
            assertTrue(CaliperContract.isAdmitted(seed, policy, expected.second + 1))
        }

        // Verify zero-frame fluency contract
        val zero = FluencyWindowAccumulator.classify(0L, 0L)
        assertNull(zero.jankRatio)
        assertEquals("unknown", zero.severity)

        val healthy = FluencyWindowAccumulator.classify(600L, 0L)
        assertEquals(0.0, healthy.jankRatio!!, 1e-9)
        assertEquals("healthy", healthy.severity)

        // Verify 24h sliding quota case from fixture
        val quota = SlidingQuota(windowMs = 86_400_000L, cap = 2)
        val ts = longArrayOf(1000L, 2000L, 3000L, 86_401_000L)
        val want = booleanArrayOf(true, true, false, true)
        ts.indices.forEach { i ->
            assertEquals("quota step $i", want[i], quota.tryConsume(ts[i]))
        }

        // Verify startup stage conservation
        assertTrue(CaliperContract.validateStartupStages(500.0, 150.0, 200.0, 120.0))
        assertFalse(CaliperContract.validateStartupStages(400.0, 250.0, null, 200.0))
    }

    @Test
    fun failClosedLeaseAndLiveGzipIngest() {
        var receivedBody = ""
        var receivedEncoding = ""
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/v1/ingest") { ex ->
            receivedEncoding = ex.requestHeaders.getFirst("Content-Encoding") ?: ""
            receivedBody = GZIPInputStream(ex.requestBody).bufferedReader().readText()
            val resp = """{"accepted":1,"duplicates":0,"rejected":0}""".toByteArray()
            ex.sendResponseHeaders(202, resp.size.toLong())
            ex.responseBody.use { it.write(resp) }
        }
        server.start()

        try {
            val client = CaliperClient(
                CaliperConfig(
                    endpointUrl = "http://127.0.0.1:${server.address.port}",
                    apiKey = "k-test",
                    resource = ResourceIdentity(
                        appId = "dev.caliper.demo",
                        appVersion = "1.0.0",
                        buildNumber = "100",
                        installHash = "0123456789abcdef"
                    ),
                    installSeed = "00000000-0000-4000-8000-000000000001"
                )
            )
            // Fail closed without lease
            assertFalse(client.enqueue("startup", """{"total_duration_ms":400,"cold_start":true,"start_anchor":"application_on_create","end_anchor":"first_content_frame","result":"success"}"""))
            assertEquals(0, client.pendingCount())

            val now = 1_763_540_000_000L
            client.applyLease(PolicyLease("fluency_v1", 200_000L, now, 60_000L, 10, setOf("startup")))
            assertTrue(
                client.enqueue(
                    kind = "startup",
                    payloadJson = """{"total_duration_ms":400,"cold_start":true,"start_anchor":"application_on_create","end_anchor":"first_content_frame","result":"success"}""",
                    occurredAtMs = now + 1000L
                )
            )
            assertEquals(1, client.flushBlocking())
            assertEquals("gzip", receivedEncoding)
            assertTrue(receivedBody.contains(""""platform":"android""""))
            assertTrue(receivedBody.contains(""""adapter":"native""""))
            client.shutdown()
        } finally {
            server.stop(0)
        }
    }
}
