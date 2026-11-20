package io.caliper.apm.android

import io.caliper.apm.CaliperClient
import io.caliper.apm.FluencyWindowAccumulator

/**
 * Pure Android OS integration contracts (zero React Native dependencies).
 * Intended for Android library AAR targets using public Android API 26+/30+/31+ and OkHttp APIs:
 *
 * 1. Fluency Collector:
 *    - API 31+: Window.OnFrameMetricsAvailableListener with FrameMetrics.DEADLINE
 *      -> emits algorithm = "android_frame_metrics_deadline"
 *    - API 26..30: Window.OnFrameMetricsAvailableListener compared against Display.refreshRate budget
 *      -> emits algorithm = "android_frame_metrics_heuristic"
 *    - Zero-frame windows are always preserved with frame_count = 0 (never reported as 0% jank).
 *
 * 2. Resource Collector:
 *    - CPU: /proc/self/stat utime+stime delta / monotonic elapsed wall time (100% = 1 core).
 *    - Memory: Debug.MemoryInfo totalPss (KiB) -> memory_kind = "android_pss_kib".
 *
 * 3. Startup & Abnormal Exit:
 *    - Cold start anchor: Application.onCreate -> first content frame; sub-stages validated by
 *      CaliperContract.validateStartupStages before emit.
 *    - API 30+: ActivityManager.getHistoricalProcessExitReasons -> maps REASON_ANR,
 *      REASON_LOW_MEMORY, REASON_EXCESSIVE_RESOURCE_USAGE, REASON_CRASH_NATIVE.
 *
 * 4. Transport Stages:
 *    - OkHttp EventListener subclass records dnsStart/dnsEnd, connectStart/connectEnd,
 *      secureConnectStart/secureConnectEnd, requestHeadersEnd/responseHeadersStart (TTFB),
 *      responseBodyStart/responseBodyEnd, and emits kind = "network_transport".
 */
class AndroidFluencySession(
    private val client: CaliperClient,
    private val sdkInt: Int,
    private val scene: String,
    private val windowDurationMs: Long = 30_000L
) {
    val algorithm: String =
        if (sdkInt >= 31) "android_frame_metrics_deadline" else "android_frame_metrics_heuristic"

    private val accumulator = FluencyWindowAccumulator()

    fun onFrame(durationMs: Double, targetMs: Double) {
        accumulator.recordFrame(durationMs, targetMs)
    }

    fun emitWindow(stallStack: List<String> = emptyList(), bypassSampling: Boolean = false): Boolean {
        val bucketsJson = accumulator.frameDurationBuckets.joinToString(prefix = "[", postfix = "]")
        val stackPart = if (stallStack.isEmpty()) "" else {
            val escaped = stallStack.joinToString(prefix = "[", postfix = "]") {
                "\"${it.replace("\\", "\\\\").replace("\"", "\\\"")}\""
            }
            ""","stall_stack":$escaped"""
        }
        val payload = """{"algorithm":"$algorithm","window_duration_ms":$windowDurationMs,"frame_count":${accumulator.frameCount},"jank_frame_count":${accumulator.jankFrameCount},"severe_jank_frame_count":${accumulator.severeJankFrameCount},"frame_duration_buckets":$bucketsJson$stackPart}"""
        return client.enqueue(
            kind = "fluency_window",
            payloadJson = payload,
            scene = scene,
            bypassSampling = bypassSampling
        )
    }
}
