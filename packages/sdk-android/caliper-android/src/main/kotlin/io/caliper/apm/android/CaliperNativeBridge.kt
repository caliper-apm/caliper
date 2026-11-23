package io.caliper.apm.android

import android.os.Looper
import io.caliper.apm.CaliperClient
import io.caliper.apm.LooperDispatchTracker
import io.caliper.apm.NativeTombstoneParser
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Safe JNI bridge to `libcaliper_native.so` (NDK C++ Signal Crash + SIGQUIT ANR interceptor).
 * Degrades gracefully when an app ships pure Java/Kotlin without native `.so` libraries.
 */
object CaliperNativeBridge {
    private val loaded = AtomicBoolean(false)
    @Volatile
    private var activeClient: CaliperClient? = null
    @Volatile
    private var activeLooperTracker: LooperDispatchTracker? = null

    @JvmStatic
    fun install(
        tombstoneDir: File,
        client: CaliperClient,
        looperTracker: LooperDispatchTracker? = null,
        enableSigQuitAnr: Boolean = true,
    ): Boolean {
        tombstoneDir.mkdirs()
        activeClient = client
        activeLooperTracker = looperTracker

        // Always ingest any prior NDK tombstones first (even before re-arming handlers).
        NativeTombstoneParser.ingestPendingDir(tombstoneDir, client)

        if (!ensureLoaded()) return false
        return runCatching {
            nativeInstall(tombstoneDir.absolutePath, enableSigQuitAnr) == 0
        }.getOrDefault(false)
    }

    @JvmStatic
    fun setScene(scene: String) {
        if (!loaded.get()) return
        runCatching { nativeSetScene(scene) }
    }

    @JvmStatic
    fun refreshMaps() {
        if (!loaded.get()) return
        runCatching { nativeRefreshMaps() }
    }

    @JvmStatic
    fun uninstall() {
        if (!loaded.get()) return
        runCatching { nativeUninstall() }
    }

    /** Invoked asynchronously from `libcaliper_native.so` when Android delivers SIGQUIT (ANR). */
    @JvmStatic
    fun onSigQuitAnrDetected(timestampMs: Long) {
        val c = activeClient ?: return
        runCatching {
            val mainThread = Looper.getMainLooper().thread
            val frames = mainThread.stackTrace.take(32)
            val framesJson = frames.joinToString(",") { f ->
                """{"symbol":"${esc("${f.className}.${f.methodName}")}","file":"${esc(f.fileName ?: "SourceFile")}","line":${f.lineNumber.coerceAtLeast(0)},"in_app":true}"""
            }
            val crumbs = ArrayList<String>()
            activeLooperTracker?.currentInFlight(timestampMs)?.let { inf ->
                crumbs.add("""{"timestamp_ms":${inf.startedAtMs},"category":"looper_inflight","message":"${esc("${inf.target} (${inf.callback}:${inf.what}) blocked ${inf.elapsedMs}ms")}"}""")
            }
            activeLooperTracker?.snapshotHistory()?.takeLast(8)?.forEach { rec ->
                crumbs.add("""{"timestamp_ms":${rec.endedAtMs},"category":"looper_history","message":"${esc("${rec.target} (${rec.callback}:${rec.what}) ${rec.durationMs}ms")}"}""")
            }
            val top = frames.firstOrNull()
            val fp = "android-sigquit-${top?.className ?: "main"}-${top?.lineNumber ?: 0}"
            val title = "Android SIGQUIT ANR at ${top?.className ?: "main"}.${top?.methodName ?: "loop"}"
            val payload = """{"fingerprint":"${esc(fp)}","title":"${esc(title)}","fatal":false,"exception_type":"ANR_SIGQUIT","thread_name":"main","frames":[$framesJson],"breadcrumbs":[${crumbs.joinToString(",")}]}"""
            c.enqueue(kind = "anr", payloadJson = payload, occurredAtMs = timestampMs, bypassSampling = true)
            runCatching { c.flushAsync() }
        }
    }

    private fun ensureLoaded(): Boolean {
        if (loaded.get()) return true
        return runCatching {
            System.loadLibrary("caliper_native")
            loaded.set(true)
            true
        }.getOrDefault(false)
    }

    private fun esc(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")

    @JvmStatic
    private external fun nativeInstall(tombstoneDir: String, enableSigQuitAnr: Boolean): Int

    @JvmStatic
    private external fun nativeSetScene(scene: String)

    @JvmStatic
    private external fun nativeRefreshMaps()

    @JvmStatic
    private external fun nativeUninstall()
}
