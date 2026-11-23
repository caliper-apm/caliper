package io.caliper.apm

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.nio.file.Files

class SpecialtyProbesTest {
    private fun makeActiveClient(): CaliperClient {
        val c = CaliperClient(
            CaliperConfig(
                endpointUrl = "http://127.0.0.1:8080",
                resource = ResourceIdentity(
                    appId = "dev.caliper.demo",
                    appVersion = "1.8.0",
                    buildNumber = "180",
                    installHash = "0123456789abcdef",
                ),
            )
        )
        c.applyLease(
            PolicyLease(
                policyId = "pol-test",
                ratePPM = 1_000_000L,
                issuedAtMs = System.currentTimeMillis() - 1000L,
                leaseTtlMs = 3_600_000L,
                maxEvents24h = 100,
                enabledCollectors = setOf("startup", "crash", "anr", "fluency", "resources", "network", "custom_scene"),
            )
        )
        return c
    }

    @Test
    fun thermalBatteryEvaluatorDetectsSevereThermalAndRecordsJourney() {
        val client = makeActiveClient()
        val normal = ThermalBatterySnapshot("nominal", 88, 34.2, false, "home_feed")
        val severe = ThermalBatterySnapshot("severe", 62, 43.8, true, "video_editor")

        assertFalse(ThermalBatteryEvaluator.isSevere(normal))
        assertTrue(ThermalBatteryEvaluator.isSevere(severe))
        assertTrue(ThermalBatteryEvaluator.record(client, normal))
        assertTrue(ThermalBatteryEvaluator.record(client, severe))
        assertEquals(2, client.pendingCount())
    }

    @Test
    fun diskAuditorSanitizesPiiPathsAndEnforcesBounds() {
        val root = Files.createTempDirectory("caliper-disk-audit").toFile()
        try {
            val cache = File(root, "cache").apply { mkdirs() }
            val userDir = File(cache, "user_987654321/550e8400-e29b-41d4-a716-446655440000").apply { mkdirs() }
            File(userDir, "alice@example.com_0123456789abcdef1234.bin").writeBytes(ByteArray(4096))

            val sanitized = CaliperDiskAuditor.sanitizePath("user_987654321/550e8400-e29b-41d4-a716-446655440000/alice@example.com_0123456789abcdef1234.bin")
            assertFalse(sanitized.contains("987654321"))
            assertFalse(sanitized.contains("550e8400"))
            assertFalse(sanitized.contains("alice@example.com"))
            assertFalse(sanitized.contains("0123456789abcdef"))

            val report = CaliperDiskAuditor.auditDirectories(
                roots = mapOf("cache" to cache),
                largeFileThresholdBytes = 1024L,
            )
            assertEquals(4096L, report.totalBytes)
            assertEquals(1, report.visitedFiles)
            assertEquals(1, report.largeFiles.size)
            assertFalse(report.largeFiles[0].sanitizedPath.contains("alice@example.com"))

            val client = makeActiveClient()
            assertTrue(CaliperDiskAuditor.recordAudit(client, report, quotaBudgetMiB = 0.001))
            assertEquals(1, client.pendingCount())
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun webViewProbeStripsUrlPiiAndIngestsNavBlankAndJsError() {
        val client = makeActiveClient()
        assertEquals(
            "https://m.example.com/promo/spring",
            CaliperWebViewProbe.normalizeUrl("https://m.example.com/promo/spring?token=secret&uid=42#top"),
        )

        val navOk = """{"type":"nav","url":"https://m.example.com/promo/spring?token=abc","dns_ms":12,"tcp_ms":18,"ttfb_ms":95,"dom_ms":240,"load_ms":420,"fcp_ms":180,"blank":false}"""
        val navBlank = """{"type":"nav","url":"https://m.example.com/broken","dns_ms":8,"tcp_ms":12,"ttfb_ms":50,"dom_ms":90,"load_ms":120,"fcp_ms":0,"blank":true}"""
        val jsErr = """{"type":"js_error","url":"https://m.example.com/promo/spring?auth=1","message":"TypeError: Cannot read properties of undefined"}"""

        assertTrue(CaliperWebViewProbe.ingestMessageJson(navOk, client))
        assertTrue(CaliperWebViewProbe.ingestMessageJson(navBlank, client))
        assertTrue(CaliperWebViewProbe.ingestMessageJson(jsErr, client))
        assertEquals(3, client.pendingCount())
    }

    @Test
    fun autoInstrumentHooksDispatchTracesAndOkHttpBuilder() {
        var capturedTrace: Triple<String, String, Boolean>? = null
        var capturedBuilder: Any? = null
        try {
            CaliperAutoInstrumentHooks.traceSink = { name, scene, _, failed ->
                capturedTrace = Triple(name, scene, failed)
            }
            CaliperAutoInstrumentHooks.okHttpBuilderHook = { b -> capturedBuilder = b }

            val t0 = CaliperAutoInstrumentHooks.beginTrace("checkout_flow")
            CaliperAutoInstrumentHooks.endTrace("checkout_flow", "checkout", t0, false)
            assertEquals(Triple("checkout_flow", "checkout", false), capturedTrace)

            val fakeBuilder = Any()
            val ret = CaliperAutoInstrumentHooks.onOkHttpBuilder(fakeBuilder)
            assertEquals(fakeBuilder, ret)
            assertEquals(fakeBuilder, capturedBuilder)
        } finally {
            CaliperAutoInstrumentHooks.traceSink = null
            CaliperAutoInstrumentHooks.okHttpBuilderHook = null
        }
    }
}
