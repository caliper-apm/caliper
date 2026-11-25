package io.caliper.apm

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ExternalProbeBridgeTest {
    private fun makeActiveClient(): CaliperClient {
        val client = CaliperClient(
            CaliperConfig(
                endpointUrl = "http://127.0.0.1:8080",
                resource = ResourceIdentity(
                    appId = "dev.caliper.demo",
                    appVersion = "1.10.0",
                    buildNumber = "1100",
                    installHash = "0123456789abcdef",
                ),
            )
        )
        client.applyLease(
            PolicyLease(
                policyId = "pol-ext-bridge",
                ratePPM = 1_000_000L,
                issuedAtMs = System.currentTimeMillis() - 1000L,
                leaseTtlMs = 3_600_000L,
                maxEvents24h = 200,
                enabledCollectors = setOf("crash", "anr", "heap_snapshot", "custom_scene"),
            )
        )
        return client
    }

    @Test
    fun parsesAndroidXCrashBacktraceIntoElfRelativeFrames() {
        val raw = """
            backtrace:
                #00 pc 0000000000042bc0  /data/app/~~abc/com.example-1/lib/arm64/libatlas_video.so (decodeFrame+64)
                #01 pc 0000000000018a04  /system/lib64/libc.so (abort+164)
                #02 pc 0000000000000000  /vendor/lib64/libcustom_codec.so
        """.trimIndent()

        val frames = CaliperExternalProbeBridge.parseAndroidNativeBacktrace(raw)
        assertEquals(3, frames.size)
        assertEquals("libatlas_video.so", frames[0].module)
        assertEquals("0x42bc0", frames[0].address)
        assertTrue(frames[0].inApp)

        assertEquals("libc.so", frames[1].module)
        assertEquals("0x18a04", frames[1].address)
        assertFalse(frames[1].inApp)

        assertEquals("libcustom_codec.so", frames[2].module)
        assertEquals("0x0", frames[2].address)
        assertTrue(frames[2].inApp)
    }

    @Test
    fun classifiesAndroidInAppClassesAndBridgesMatrixKoomXCrashBtraceEvents() {
        assertFalse(AndroidFrameClassifier.isInAppClass("android.os.Looper"))
        assertFalse(AndroidFrameClassifier.isInAppClass("java.lang.Thread"))
        assertTrue(AndroidFrameClassifier.isInAppClass("com.atlas.reader.FeedAdapter"))

        val client = makeActiveClient()

        // 1. xCrash native crash
        val parsedFrames = CaliperExternalProbeBridge.parseAndroidNativeBacktrace(
            "#00 pc 000042bc /data/app/lib/arm64/libatlas_ndk.so"
        )
        assertTrue(
            CaliperExternalProbeBridge.ingestExternalCrash(
                client = client,
                source = "xcrash",
                exceptionType = "SIGSEGV",
                title = "xCrash SIGSEGV in libatlas_ndk.so",
                frames = parsedFrames,
                breadcrumbs = listOf("fd_count" to "412", "threads" to "98"),
                scene = "video_decoder",
            )
        )

        // 2. Tencent Matrix EvilMethod / ByteDance btrace lag
        assertTrue(
            CaliperExternalProbeBridge.ingestExternalLagOrAnr(
                client = client,
                source = "matrix_trace",
                durationMs = 1280L,
                frames = listOf(
                    ExternalStackFrame(
                        symbol = "com.atlas.reader.FeedAdapter.onBindViewHolder",
                        file = "FeedAdapter.kt",
                        line = 142,
                        inApp = true,
                    )
                ),
                traceReference = "btrace://perfetto-session-42",
                scene = "home_feed",
            )
        )

        // 3. Kuaishou KOOM / ByteDance Tailor / Raphael heap dominator summary
        assertTrue(
            CaliperExternalProbeBridge.ingestExternalHeapSnapshot(
                client = client,
                triggerReason = "oom_warning",
                memoryKind = "android_pss_kib",
                totalAllocatedKiB = 390_000.0,
                totalRetainedKiB = 285_000.0,
                memoryLimitKiB = 512_000.0,
                dominators = listOf(
                    ExternalHeapDominator(
                        className = "android.graphics.Bitmap",
                        instanceCount = 640L,
                        retainedKiB = 148_500.0,
                        retainerPath = listOf("ImagePipeline", "LruBitmapPool"),
                    ),
                    ExternalHeapDominator(
                        className = "com.atlas.reader.ArticleActivity",
                        instanceCount = 5L,
                        retainedKiB = 42_000.0,
                    ),
                ),
                scene = "media_gallery",
            )
        )

        // Empty dominators must return false (Zero-Lie)
        assertFalse(
            CaliperExternalProbeBridge.ingestExternalHeapSnapshot(
                client = client,
                totalAllocatedKiB = 100.0,
                totalRetainedKiB = 10.0,
                dominators = emptyList(),
            )
        )

        // 4. Matrix IOCanary / SQLiteLint / BatteryCanary specialty diagnostic journey
        assertTrue(
            CaliperExternalProbeBridge.ingestExternalDiagnosticJourney(
                client = client,
                source = "matrix_io",
                diagnosticName = "main_thread_io",
                passed = false,
                durationMs = 214.0,
                stages = listOf("file_read_small_buf" to 214.0),
                errorCode = "MATRIX_IO_MAIN_THREAD",
                scene = "startup",
            )
        )

        assertEquals(4, client.pendingCount())
    }
}
