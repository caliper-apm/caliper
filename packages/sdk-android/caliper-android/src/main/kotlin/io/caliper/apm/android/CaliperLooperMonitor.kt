package io.caliper.apm.android

import android.os.Looper
import android.os.SystemClock
import android.util.Printer
import io.caliper.apm.CaliperClient
import io.caliper.apm.LooperDispatchTracker

/**
 * Public-API Android Main-Thread Looper Message-Dispatch Monitor (zero reflection, zero hidden APIs).
 * Tracks slow UI message dispatches and maintains a bounded MessageQueue history ring for ANR attribution.
 */
class CaliperLooperMonitor(
    private val client: CaliperClient,
    slowThresholdMs: Long = 700L,
    maxHistory: Int = 20,
) {
    val tracker = LooperDispatchTracker(
        slowThresholdMs = slowThresholdMs,
        maxHistory = maxHistory,
        onSlowMessage = { slow, history ->
            val crumbs = history.takeLast(8).joinToString(",") { rec ->
                """{"timestamp_ms":${rec.endedAtMs},"category":"looper_history","message":"${esc("${rec.target} (${rec.callback}:${rec.what}) ${rec.durationMs}ms")}"}"""
            }
            val title = "Slow Main-Thread Looper Message (${slow.durationMs}ms): ${slow.target} (${slow.callback}:${slow.what})"
            val fp = "android-looper-slow-${slow.target}-${slow.what}"
            val payload = """{"fingerprint":"${esc(fp)}","title":"${esc(title)}","fatal":false,"exception_type":"LOOPER_SLOW_DISPATCH","thread_name":"main","frames":[{"symbol":"${esc("${slow.target}.${slow.callback}")}","in_app":true}],"breadcrumbs":[$crumbs]}"""
            client.enqueue(kind = "anr", payloadJson = payload, bypassSampling = true)
        },
    )

    private val printer = Printer { line ->
        if (line != null) {
            tracker.onPrinterLine(line, SystemClock.elapsedRealtime())
        }
    }

    fun start() {
        Looper.getMainLooper().setMessageLogging(printer)
    }

    fun stop() {
        Looper.getMainLooper().setMessageLogging(null)
    }

    private fun esc(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")
}
