package io.caliper.apm

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.nio.file.Files

class NativeProbesTest {
    @Test
    fun parsesAndIngestsNdkCrashAndSigQuitTombstones() {
        val dir = Files.createTempDirectory("caliper-ndk-tombstones").toFile()
        try {
            File(dir, "ndk-crash-1700000001000.tombstone").writeText(
                """
                format=caliper_native_crash_v1
                mechanism=signal
                exception=SIGSEGV
                code=1
                fault_addr=0x0
                timestamp_ms=1700000001000
                scene=video_decoder
                frame=libatlas_ndk.so||0x42bc|0x7b123442bc
                frame=libc.so||0x18a0|0x7b999918a0
                """.trimIndent()
            )
            File(dir, "ndk-anr-1700000002000.tombstone").writeText(
                """
                format=caliper_native_crash_v1
                mechanism=sigquit
                exception=SIGQUIT
                code=0
                fault_addr=0x0
                timestamp_ms=1700000002000
                scene=home_feed
                """.trimIndent()
            )

            val parsedCrash = NativeTombstoneParser.parse(File(dir, "ndk-crash-1700000001000.tombstone").readText())
            assertNotNull(parsedCrash)
            assertEquals("SIGSEGV", parsedCrash!!.exception)
            assertEquals("video_decoder", parsedCrash.scene)
            assertEquals(2, parsedCrash.frames.size)
            assertEquals("libatlas_ndk.so", parsedCrash.frames[0].module)
            assertEquals("0x42bc", parsedCrash.frames[0].relativeHex)

            val client = CaliperClient(
                CaliperConfig(
                    endpointUrl = "http://127.0.0.1:8080",
                    resource = ResourceIdentity(
                        appId = "dev.caliper.demo",
                        appVersion = "1.7.0",
                        buildNumber = "170",
                        installHash = "0123456789abcdef",
                    ),
                )
            )
            val ingested = NativeTombstoneParser.ingestPendingDir(dir, client)
            assertEquals(2, ingested)
            assertEquals(2, client.pendingCount())
            assertTrue(dir.listFiles().isNullOrEmpty())
        } finally {
            dir.deleteRecursively()
        }
    }

    @Test
    fun tracksLooperDispatchLatencyHistoryAndSlowMessages() {
        val captured = ArrayList<Pair<LooperMessageRecord, Int>>()
        val tracker = LooperDispatchTracker(
            slowThresholdMs = 500L,
            maxHistory = 5,
            onSlowMessage = { rec, hist -> captured.add(rec to hist.size) },
        )

        // Fast message (12ms)
        tracker.onPrinterLine(">>>>> Dispatching to Handler (android.view.Choreographer\$FrameHandler) {1a2b3c} null: 0", 1000L)
        val inFlight = tracker.currentInFlight(1005L)
        assertNotNull(inFlight)
        assertEquals("android.view.Choreographer\$FrameHandler", inFlight!!.target)
        assertEquals(5L, inFlight.elapsedMs)
        tracker.onPrinterLine("<<<<< Finished to Handler (android.view.Choreographer\$FrameHandler) {1a2b3c} null", 1012L)
        assertNull(tracker.currentInFlight(1020L))
        assertTrue(captured.isEmpty())

        // Slow blocking message (820ms)
        tracker.onPrinterLine(">>>>> Dispatching to Handler (android.app.ActivityThread\$H) {deadbeef} com.atlas.DbMigrationTask: 159", 2000L)
        val blocked = tracker.currentInFlight(2600L)
        assertNotNull(blocked)
        assertEquals("com.atlas.DbMigrationTask", blocked!!.callback)
        assertEquals("159", blocked.what)
        assertEquals(600L, blocked.elapsedMs)
        tracker.onPrinterLine("<<<<< Finished to Handler (android.app.ActivityThread\$H) {deadbeef} com.atlas.DbMigrationTask", 2820L)

        assertEquals(1, captured.size)
        assertEquals(820L, captured[0].first.durationMs)
        assertEquals("android.app.ActivityThread\$H", captured[0].first.target)
        assertEquals("com.atlas.DbMigrationTask", captured[0].first.callback)
        assertEquals("159", captured[0].first.what)
        assertEquals(2, captured.size.let { captured[0].second })
        assertEquals(2, tracker.snapshotHistory().size)
    }

    @Test
    fun processCpuSamplerParsesProcStatAndComputesDeltaPct() {
        val sampler = CaliperProcessCpuSampler(clockTicksPerSecond = 100L)
        // Process name intentionally contains spaces and parentheses.
        val stat1 = "12345 (com.example (app)) S 1 12345 12345 0 -1 4194304 100 0 0 0 150 50 0 0 20 0 12 0 1000"
        val stat2 = "12345 (com.example (app)) S 1 12345 12345 0 -1 4194304 100 0 0 0 210 70 0 0 20 0 12 0 1000"

        // Baseline returns null (no prior observation -> never fabricate 0%).
        assertNull(sampler.sample(stat1, wallMs = 10_000L))

        // +80 ticks (= 0.80s CPU) over 10.0s wall time -> 8.0% of 1 core.
        val pct = sampler.sample(stat2, wallMs = 20_000L)
        assertNotNull(pct)
        assertEquals(8.0, pct!!, 0.001)
    }
}
