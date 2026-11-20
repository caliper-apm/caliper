package io.caliper.apm

import com.sun.net.httpserver.HttpServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.net.InetSocketAddress
import java.nio.file.Files
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import java.util.zip.GZIPInputStream

class ProductionModulesTest {

    @Test
    fun diskQueuePersistsEvictsAndDrainsInOrder() {
        val dir = Files.createTempDirectory("caliper-spool").toFile()
        try {
            val q = CaliperDiskQueue(dir, maxFiles = 3, maxTotalBytes = 1024 * 64)
            q.enqueue("""{"id":1}""")
            Thread.sleep(2)
            q.enqueue("""{"id":2}""")
            Thread.sleep(2)
            q.enqueue("""{"id":3}""")
            Thread.sleep(2)
            q.enqueue("""{"id":4}""")

            // Oldest (id=1) must be evicted by maxFiles=3 cap.
            assertEquals(3, q.count())
            val drained = mutableListOf<String>()
            val count = q.drainEach { drained += it }
            assertEquals(3, count)
            assertEquals(0, q.count())
            assertTrue(drained.first().contains("2"))
            assertTrue(drained.last().contains("4"))
        } finally {
            dir.deleteRecursively()
        }
    }

    @Test
    fun watchdogDetectsMainThreadStallWithRealStackFrames() {
        val latch = CountDownLatch(1)
        val capturedFrames = AtomicReference<List<String>>(emptyList())

        // Simulate a frozen main thread that ignores posted runnables.
        val watchdog = CaliperWatchdog(
            mainThread = Thread.currentThread(),
            postToMain = { /* intentionally stalled */ },
            hangThresholdMs = 80L,
            pollIntervalMs = 25L,
            onHangDetected = { _, frames ->
                capturedFrames.set(frames)
                latch.countDown()
            },
        )
        watchdog.start()
        try {
            assertTrue("watchdog must fire on stalled thread", latch.await(2, TimeUnit.SECONDS))
            assertTrue("watchdog must capture real stack frames", capturedFrames.get().isNotEmpty())
        } finally {
            watchdog.stop()
        }
    }

    @Test
    fun cpuProfilerProducesValidFoldedStackJson() {
        val profiler = CaliperCpuProfiler()
        val json = profiler.sampleThreadBlocking(Thread.currentThread(), durationMs = 60L, intervalMs = 10)
        assertTrue(json.contains("\"runtime\":\"native\""))
        assertTrue(json.contains("\"stacks\":["))
        assertTrue(json.contains("ProductionModulesTest"))
    }

    @Test
    fun remoteLogAgentScrubsPIIPollsAndUploadsGzip() {
        val ring = CaliperDiagnosticRingLog(maxLines = 20)
        ring.append("INFO", "Auth", "login user alice@example.com with Bearer secretToken123")
        ring.append("WARN", "Feed", "frame drop 142ms")
        val dumped = ring.dumpText()
        assertFalse(dumped.contains("alice@example.com"))
        assertFalse(dumped.contains("secretToken123"))
        assertTrue(dumped.contains("[REDACTED_EMAIL]"))
        assertTrue(dumped.contains("[REDACTED_TOKEN]"))

        val uploaded = AtomicReference("")
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/v1/diagnostics/log-tasks/pending") { ex ->
            val resp = """{"tasks":[{"task_id":"rlog-test-1"}]}""".toByteArray()
            ex.sendResponseHeaders(200, resp.size.toLong())
            ex.responseBody.use { it.write(resp) }
        }
        server.createContext("/v1/diagnostics/log-tasks/rlog-test-1/upload") { ex ->
            val gz = ex.requestBody.readBytes()
            val plain = GZIPInputStream(ByteArrayInputStream(gz)).readBytes().toString(Charsets.UTF_8)
            uploaded.set(plain)
            val resp = """{"status":"completed"}""".toByteArray()
            ex.sendResponseHeaders(200, resp.size.toLong())
            ex.responseBody.use { it.write(resp) }
        }
        server.start()
        try {
            val cfg = CaliperConfig(
                endpointUrl = "http://127.0.0.1:${server.address.port}",
                resource = ResourceIdentity(
                    appId = "dev.caliper.demo",
                    appVersion = "1.1.0",
                    buildNumber = "110",
                    installHash = "0123456789abcdef",
                    userHash = "0000111122223333",
                ),
            )
            val fulfilled = CaliperRemoteLogAgent(cfg, ring).pollAndFulfillOnce()
            assertEquals(1, fulfilled)
            assertTrue(uploaded.get().contains("[REDACTED_EMAIL]"))
            assertTrue(uploaded.get().contains("frame drop 142ms"))
        } finally {
            server.stop(0)
        }
    }
}
