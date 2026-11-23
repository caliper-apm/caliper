package io.caliper.apm.android

import android.app.Activity
import android.app.ActivityManager
import android.app.Application
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import android.os.Bundle
import android.os.Debug
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.os.Process
import android.os.SystemClock
import android.view.FrameMetrics
import android.view.Window
import io.caliper.apm.CaliperClient
import io.caliper.apm.CaliperConfig
import io.caliper.apm.CaliperDiagnosticRingLog
import io.caliper.apm.CaliperRemoteLogAgent
import io.caliper.apm.CaliperWatchdog
import io.caliper.apm.FluencyWindowAccumulator
import io.caliper.apm.PolicyLease
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Drop-in Pure-Native Android APM Agent (Zero React Native dependencies).
 *
 * Wires public Android OS APIs automatically on [install]:
 * - Single-anchor 3-stage Cold Start conservation (`Process.getStartElapsedRealtime()` -> first content frame)
 * - API 31+ `FrameMetrics.DEADLINE` vs API 26-30 `heuristic` strictly isolated fluency windows
 * - API 30+ `ActivityManager.getHistoricalProcessExitReasons` (ANR / OOM / LMK abnormal exit attribution)
 * - Chained `UncaughtExceptionHandler` (never swallows host/other crash handlers)
 * - Off-main-thread Watchdog ANR / stall stack sampler + bounded offline disk spool + remote log poller
 */
object CaliperAndroidAgent {
    private val installed = AtomicBoolean(false)
    private lateinit var client: CaliperClient
    private val ringLog = CaliperDiagnosticRingLog(maxLines = 500)
    private var watchdog: CaliperWatchdog? = null
    private var looperMonitor: CaliperLooperMonitor? = null
    private var processStartElapsedMs: Long = 0L
    private var nativeInitDoneElapsedMs: Long = 0L
    private val firstFrameReported = AtomicBoolean(false)

    @JvmStatic
    fun install(application: Application, config: CaliperConfig, defaultLease: PolicyLease? = null): CaliperClient {
        if (!installed.compareAndSet(false, true)) {
            return client
        }
        processStartElapsedMs = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            Process.getStartElapsedRealtime()
        } else {
            SystemClock.elapsedRealtime()
        }
        val spool = config.spoolDir ?: File(application.filesDir, "caliper-spool")
        client = CaliperClient(config.copy(spoolDir = spool))
        defaultLease?.let { client.applyLease(it) }

        looperMonitor = CaliperLooperMonitor(client).also { it.start() }
        val tombstoneDir = File(application.filesDir, "caliper-tombstones")
        CaliperNativeBridge.install(
            tombstoneDir = tombstoneDir,
            client = client,
            looperTracker = looperMonitor?.tracker,
            enableSigQuitAnr = true,
        )
        installUncaughtExceptionChain()
        reportHistoricalAbnormalExits(application)
        installLifecycleAndFrameMetrics(application)
        installWatchdog()

        nativeInitDoneElapsedMs = SystemClock.elapsedRealtime()
        ringLog.append("INFO", "Caliper", "installed android agent sdk=${config.resource.sdkVersion}")
        return client
    }

    @JvmStatic
    fun client(): CaliperClient = client

    @JvmStatic
    fun log(level: String, tag: String, message: String) {
        ringLog.append(level, tag, message)
    }

    /** Call once when the first meaningful screen renders its initial content. */
    @JvmStatic
    fun markFirstContentFrame(runtimeInitMs: Double? = null) {
        if (!installed.get() || !firstFrameReported.compareAndSet(false, true)) return
        val now = SystemClock.elapsedRealtime()
        val total = (now - processStartElapsedMs).coerceAtLeast(1L).toDouble()
        val nativeInit = (nativeInitDoneElapsedMs - processStartElapsedMs).coerceIn(0L, total.toLong()).toDouble()
        val rt = runtimeInitMs?.coerceIn(0.0, (total - nativeInit).coerceAtLeast(0.0))
        val firstFrame = (total - nativeInit - (rt ?: 0.0)).coerceAtLeast(0.0)

        val payload = buildString {
            append("""{"total_duration_ms":$total,"native_init_ms":$nativeInit,""")
            if (rt != null) append(""""runtime_init_ms":$rt,""")
            append(""""first_frame_ms":$firstFrame,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}""")
        }
        client.enqueue(kind = "startup", payloadJson = payload, scene = "startup")
        client.flushAsync()
    }

    @JvmStatic
    fun captureProcessMemoryAndCpu(scene: String, cpuPct: Double, windowDurationMs: Long = 30_000L) {
        if (!installed.get()) return
        val memInfo = Debug.MemoryInfo()
        Debug.getMemoryInfo(memInfo)
        val totalPssKiB = memInfo.totalPss.toDouble().coerceAtLeast(0.0)
        val payload = """{"window_duration_ms":$windowDurationMs,"cpu_pct":${cpuPct.coerceAtLeast(0.0)},"memory_kind":"android_pss_kib","memory_peak_kib":$totalPssKiB,"memory_avg_kib":$totalPssKiB,"sample_count":1}"""
        client.enqueue(kind = "resource_window", payloadJson = payload, scene = scene)
    }

    @JvmStatic
    fun pollRemoteLogTasksAsync() {
        if (!installed.get()) return
        Thread({
            runCatching { CaliperRemoteLogAgent(client.getConfig(), ringLog).pollAndFulfillOnce() }
        }, "caliper-rlog-poller").apply { isDaemon = true; start() }
    }

    private fun installUncaughtExceptionChain() {
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            runCatching {
                val top = throwable.stackTrace.firstOrNull()
                val fp = "android-${throwable.javaClass.simpleName}-${top?.className ?: "unknown"}-${top?.lineNumber ?: 0}"
                val framesJson = throwable.stackTrace.take(32).joinToString(",") { f ->
                    """{"symbol":"${esc("${f.className}.${f.methodName}")}","file":"${esc(f.fileName ?: "SourceFile")}","line":${f.lineNumber.coerceAtLeast(0)},"in_app":true}"""
                }
                val crumbsJson = client.breadcrumbsSnapshot().joinToString(",") { c ->
                    """{"timestamp_ms":${c.timestampMs},"category":"${esc(c.category)}","message":"${esc(c.message)}"}"""
                }
                val title = esc("${throwable.javaClass.simpleName}: ${throwable.message ?: "uncaught exception"}").take(240)
                val payload = """{"fingerprint":"${esc(fp)}","title":"$title","fatal":true,"exception_type":"${esc(throwable.javaClass.name)}","thread_name":"${esc(thread.name)}","frames":[$framesJson],"breadcrumbs":[$crumbsJson]}"""
                client.enqueue(kind = "crash", payloadJson = payload, bypassSampling = true)
                // Persist synchronously to disk spool if network flush cannot finish before death.
                runCatching { client.flushBlocking() }
            }
            previous?.uncaughtException(thread, throwable)
        }
    }

    private fun reportHistoricalAbnormalExits(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return
        runCatching {
            val am = context.getSystemService(Context.ACTIVITY_SERVICE) as? ActivityManager ?: return
            val prefs = context.getSharedPreferences("caliper_apm_state", Context.MODE_PRIVATE)
            val lastReportedTs = prefs.getLong("last_exit_ts", 0L)
            val exits = am.getHistoricalProcessExitReasons(context.packageName, 0, 5)
            var newest = lastReportedTs
            for (info in exits) {
                if (info.timestamp <= lastReportedTs) continue
                if (info.timestamp > newest) newest = info.timestamp
                if (info.reason == ApplicationExitInfo.REASON_LOW_MEMORY ||
                    info.reason == ApplicationExitInfo.REASON_CRASH_NATIVE ||
                    info.reason == ApplicationExitInfo.REASON_ANR
                ) {
                    val reasonName = when (info.reason) {
                        ApplicationExitInfo.REASON_LOW_MEMORY -> "REASON_LOW_MEMORY"
                        ApplicationExitInfo.REASON_CRASH_NATIVE -> "REASON_CRASH_NATIVE"
                        ApplicationExitInfo.REASON_ANR -> "REASON_ANR"
                        else -> "REASON_OTHER"
                    }
                    val payload = """{"fingerprint":"android-exit-$reasonName","title":"Abnormal Process Exit ($reasonName)","fatal":true,"exit_reason":"$reasonName","process_importance":"${info.importance}","frames":[]}"""
                    client.enqueue(kind = "abnormal_exit", payloadJson = payload, occurredAtMs = info.timestamp, bypassSampling = true)
                }
            }
            if (newest > lastReportedTs) {
                prefs.edit().putLong("last_exit_ts", newest).apply()
            }
        }
    }

    private fun installLifecycleAndFrameMetrics(application: Application) {
        val metricsThread = HandlerThread("caliper-framemetrics", Process.THREAD_PRIORITY_BACKGROUND).apply { start() }
        val metricsHandler = Handler(metricsThread.looper)

        application.registerActivityLifecycleCallbacks(object : Application.ActivityLifecycleCallbacks {
            private val listeners = HashMap<Int, Triple<String, FluencyWindowAccumulator, Window.OnFrameMetricsAvailableListener>>()

            override fun onActivityResumed(activity: Activity) {
                val scene = activity.javaClass.simpleName
                client.setScene(scene)
                CaliperNativeBridge.setScene(scene)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
                    val algo = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                        "android_frame_metrics_deadline"
                    } else {
                        "android_frame_metrics_heuristic"
                    }
                    val acc = FluencyWindowAccumulator()
                    val listener = Window.OnFrameMetricsAvailableListener { _, frameMetrics, _ ->
                        val totalMs = frameMetrics.getMetric(FrameMetrics.TOTAL_DURATION) / 1_000_000.0
                        val targetMs = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                            (frameMetrics.getMetric(FrameMetrics.DEADLINE) / 1_000_000.0).coerceAtLeast(8.0)
                        } else {
                            16.67
                        }
                        acc.recordFrame(totalMs, targetMs)
                    }
                    runCatching {
                        activity.window?.addOnFrameMetricsAvailableListener(listener, metricsHandler)
                        listeners[System.identityHashCode(activity)] = Triple(algo, acc, listener)
                    }
                }
            }

            override fun onActivityPaused(activity: Activity) {
                val entry = listeners.remove(System.identityHashCode(activity)) ?: return
                val (algo, acc, listener) = entry
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
                    runCatching { activity.window?.removeOnFrameMetricsAvailableListener(listener) }
                }
                val buckets = acc.frameDurationBuckets.joinToString(",")
                val payload = """{"algorithm":"$algo","window_duration_ms":10000,"frame_count":${acc.frameCount},"jank_frame_count":${acc.jankFrameCount},"severe_jank_frame_count":${acc.severeJankFrameCount},"frame_duration_buckets":[$buckets]}"""
                client.enqueue(kind = "fluency_window", payloadJson = payload, scene = activity.javaClass.simpleName)
                client.flushAsync()
            }

            override fun onActivityCreated(a: Activity, s: Bundle?) {}
            override fun onActivityStarted(a: Activity) {}
            override fun onActivityStopped(a: Activity) {}
            override fun onActivitySaveInstanceState(a: Activity, outState: Bundle) {}
            override fun onActivityDestroyed(a: Activity) {}
        })
    }

    private fun installWatchdog() {
        val mainHandler = Handler(Looper.getMainLooper())
        watchdog = CaliperWatchdog(
            mainThread = Looper.getMainLooper().thread,
            postToMain = { mainHandler.post(it) },
            hangThresholdMs = 3000L,
            pollIntervalMs = 1000L,
            onHangDetected = { durMs, frames ->
                val framesJson = frames.joinToString(",") { f -> """{"symbol":"${esc(f)}","in_app":true}""" }
                val crumbs = ArrayList<String>()
                looperMonitor?.tracker?.currentInFlight(SystemClock.elapsedRealtime())?.let { inf ->
                    crumbs.add("""{"timestamp_ms":${inf.startedAtMs},"category":"looper_inflight","message":"${esc("${inf.target} (${inf.callback}:${inf.what}) blocked ${inf.elapsedMs}ms")}"}""")
                }
                looperMonitor?.tracker?.snapshotHistory()?.takeLast(6)?.forEach { rec ->
                    crumbs.add("""{"timestamp_ms":${rec.endedAtMs},"category":"looper_history","message":"${esc("${rec.target} (${rec.callback}:${rec.what}) ${rec.durationMs}ms")}"}""")
                }
                val payload = """{"fingerprint":"android-anr-${frames.firstOrNull()?.hashCode() ?: 0}","title":"Main Thread Stall (${durMs}ms)","fatal":false,"exception_type":"ANR","thread_name":"main","frames":[$framesJson],"breadcrumbs":[${crumbs.joinToString(",")}]}"""
                client.enqueue(kind = "anr", payloadJson = payload, bypassSampling = true)
            },
        ).also { it.start() }
    }

    private fun esc(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")
}
