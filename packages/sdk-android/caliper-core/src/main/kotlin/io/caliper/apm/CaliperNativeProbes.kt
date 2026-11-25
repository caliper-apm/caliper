package io.caliper.apm

import java.io.File

/** Parsed representation of a single native stack frame emitted by libcaliper_native.so. */
data class NativeTombstoneFrame(
    val module: String,
    val buildId: String,
    val relativeHex: String,
    val absoluteHex: String,
)

/** Parsed representation of an async-signal-safe Android NDK crash or SIGQUIT ANR tombstone. */
data class NativeTombstoneReport(
    val mechanism: String,   // "signal" | "sigquit"
    val exception: String,   // e.g. "SIGSEGV", "SIGABRT", "SIGQUIT"
    val code: Long,
    val faultAddress: String,
    val timestampMs: Long,
    val scene: String,
    val frames: List<NativeTombstoneFrame>,
)

/**
 * Pure-Kotlin parser and ingestor for NDK crash & SIGQUIT ANR tombstones written by libcaliper_native.so.
 * Produces schema-compliant `crash` / `anr` payloads carrying ELF-relative hex offsets ready for
 * server-side `native_elf` DWARF symbolication.
 */
object NativeTombstoneParser {
    fun parse(raw: String): NativeTombstoneReport? {
        var mechanism = "signal"
        var exception = ""
        var code = 0L
        var faultAddr = "0x0"
        var tsMs = System.currentTimeMillis()
        var scene = "unknown"
        val frames = ArrayList<NativeTombstoneFrame>()

        for (line in raw.lineSequence()) {
            val idx = line.indexOf('=')
            if (idx <= 0) continue
            val key = line.substring(0, idx)
            val value = line.substring(idx + 1)
            when (key) {
                "mechanism" -> mechanism = value
                "exception" -> exception = value
                "code" -> code = value.toLongOrNull() ?: 0L
                "fault_addr" -> faultAddr = value
                "timestamp_ms" -> tsMs = value.toLongOrNull() ?: tsMs
                "scene" -> scene = value.ifEmpty { "unknown" }
                "frame" -> {
                    val parts = value.split('|')
                    if (parts.size >= 3) {
                        frames.add(
                            NativeTombstoneFrame(
                                module = parts[0],
                                buildId = parts[1],
                                relativeHex = parts[2],
                                absoluteHex = parts.getOrElse(3) { parts[2] },
                            )
                        )
                    }
                }
            }
        }
        if (exception.isEmpty()) return null
        return NativeTombstoneReport(
            mechanism = mechanism,
            exception = exception,
            code = code,
            faultAddress = faultAddr,
            timestampMs = tsMs,
            scene = scene,
            frames = frames,
        )
    }

    /**
     * Scans [tombstoneDir] for `.tombstone` files left by a prior NDK signal crash or SIGQUIT ANR,
     * enqueues them into [client] with `bypassSampling = true`, and deletes consumed files.
     */
    fun ingestPendingDir(tombstoneDir: File, client: CaliperClient): Int {
        if (!tombstoneDir.exists() || !tombstoneDir.isDirectory) return 0
        val files = tombstoneDir.listFiles { f -> f.isFile && f.name.endsWith(".tombstone") }
            ?.sortedBy { it.name }
            ?: return 0
        var ingested = 0
        for (f in files) {
            val raw = runCatching { f.readText(Charsets.UTF_8) }.getOrNull()
            runCatching { f.delete() }
            if (raw.isNullOrBlank()) continue
            val report = parse(raw) ?: continue
            val isAnr = report.mechanism == "sigquit" || report.exception == "SIGQUIT"
            val top = report.frames.firstOrNull()
            val topMod = top?.module ?: "native"
            val topAddr = top?.relativeHex ?: report.faultAddress
            val fp = if (isAnr) {
                "android-ndk-sigquit-${report.scene}"
            } else {
                "android-ndk-${report.exception}-$topMod-$topAddr"
            }
            val title = if (isAnr) {
                "Native SIGQUIT ANR Intercepted (scene=${report.scene})"
            } else {
                "Native NDK Crash: ${report.exception} in $topMod (fault=${report.faultAddress})"
            }
            val framesJson = report.frames.take(32).joinToString(",") { fr ->
                val inApp = !fr.module.startsWith("libc.") && !fr.module.startsWith("libart.") && !fr.module.startsWith("libandroid_runtime.")
                """{"module":"${escJson(fr.module)}","symbol":"${escJson(fr.relativeHex)}","address":"${escJson(fr.relativeHex)}","in_app":$inApp}"""
            }
            val payload = """{"fingerprint":"${escJson(fp)}","title":"${escJson(title)}","fatal":${!isAnr},"exception_type":"${escJson(report.exception)}","thread_name":"native","frames":[$framesJson]}"""
            client.enqueue(
                kind = if (isAnr) "anr" else "crash",
                payloadJson = payload,
                scene = report.scene,
                occurredAtMs = report.timestampMs,
                bypassSampling = true,
            )
            ingested++
        }
        return ingested
    }

    private fun escJson(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")
}

/** One completed main-thread Looper dispatch record for ANR / freeze attribution. */
data class LooperMessageRecord(
    val target: String,
    val callback: String,
    val what: String,
    val durationMs: Long,
    val endedAtMs: Long,
)

/** Currently executing (in-flight) main-thread Looper message. */
data class LooperInFlightMessage(
    val target: String,
    val callback: String,
    val what: String,
    val startedAtMs: Long,
    val elapsedMs: Long,
)

/**
 * Pure-Kotlin, zero-regex Android Main-Thread Looper Message-Dispatch Latency & History Tracker.
 *
 * Parses standard Android `Looper.setMessageLogging` lines:
 *   `>>>>> Dispatching to Handler (android.app.ActivityThread$H) {1a2b3c} null: 159`
 *   `<<<<< Finished to Handler (android.app.ActivityThread$H) {1a2b3c} null`
 *
 * Maintains a bounded ring buffer of recent dispatches so Watchdog stalls, SIGQUIT ANRs,
 * and slow-message freezes carry exact `MessageQueue` history attribution.
 */
class LooperDispatchTracker(
    private val slowThresholdMs: Long = 700L,
    private val maxHistory: Int = 20,
    private val onSlowMessage: ((LooperMessageRecord, List<LooperMessageRecord>) -> Unit)? = null,
) {
    private val lock = Any()
    private val history = ArrayDeque<LooperMessageRecord>(maxHistory + 1)

    @Volatile
    private var activeStartMs: Long = 0L

    @Volatile
    private var activeTarget: String = ""

    @Volatile
    private var activeCallback: String = ""

    @Volatile
    private var activeWhat: String = ""

    fun onPrinterLine(line: String, nowMs: Long = System.currentTimeMillis()) {
        if (line.startsWith(">>>>> Dispatching to ")) {
            val (target, callback, what) = parseDispatchLine(line)
            activeTarget = target
            activeCallback = callback
            activeWhat = what
            activeStartMs = nowMs
        } else if (line.startsWith("<<<<< Finished to ")) {
            val started = activeStartMs
            if (started <= 0L) return
            val dur = (nowMs - started).coerceAtLeast(0L)
            val rec = LooperMessageRecord(
                target = activeTarget,
                callback = activeCallback,
                what = activeWhat,
                durationMs = dur,
                endedAtMs = nowMs,
            )
            activeStartMs = 0L

            var slowSnapshot: List<LooperMessageRecord>? = null
            synchronized(lock) {
                if (history.size >= maxHistory) history.removeFirst()
                history.addLast(rec)
                if (dur >= slowThresholdMs && onSlowMessage != null) {
                    slowSnapshot = history.toList()
                }
            }
            slowSnapshot?.let { onSlowMessage?.invoke(rec, it) }
        }
    }

    fun currentInFlight(nowMs: Long = System.currentTimeMillis()): LooperInFlightMessage? {
        val started = activeStartMs
        if (started <= 0L) return null
        return LooperInFlightMessage(
            target = activeTarget,
            callback = activeCallback,
            what = activeWhat,
            startedAtMs = started,
            elapsedMs = (nowMs - started).coerceAtLeast(0L),
        )
    }

    fun snapshotHistory(): List<LooperMessageRecord> = synchronized(lock) { history.toList() }

    companion object {
        internal fun parseDispatchLine(line: String): Triple<String, String, String> {
            // Example: ">>>>> Dispatching to Handler (android.view.Choreographer$FrameHandler) {hash} com.app.Task: 0"
            val openParen = line.indexOf('(')
            val closeParen = if (openParen >= 0) line.indexOf(')', openParen + 1) else -1
            val target = if (openParen >= 0 && closeParen > openParen) {
                line.substring(openParen + 1, closeParen).trim()
            } else {
                "Handler"
            }

            val closeBrace = if (closeParen >= 0) line.indexOf('}', closeParen + 1) else -1
            val tail = if (closeBrace >= 0 && closeBrace + 1 < line.length) {
                line.substring(closeBrace + 1).trim()
            } else {
                ""
            }
            val colon = tail.lastIndexOf(':')
            val callback = if (colon >= 0) tail.substring(0, colon).trim() else tail
            val what = if (colon >= 0 && colon + 1 < tail.length) tail.substring(colon + 1).trim() else "0"
            return Triple(target, callback.ifEmpty { "null" }, what.ifEmpty { "0" })
        }
    }
}

/**
 * Pure-Kotlin Linux `/proc/self/stat` CPU delta sampler (100% = 1 full CPU core).
 * Safe against process names containing spaces or nested parentheses; runs strictly off main thread.
 */
class CaliperProcessCpuSampler(
    private val clockTicksPerSecond: Long = 100L,
) {
    private val lock = Any()
    private var prevCpuTicks: Long = -1L
    private var prevWallMs: Long = -1L

    /**
     * Records one `/proc/self/stat` snapshot at monotonic [wallMs].
     * Returns `null` on the initial baseline observation (or invalid inputs), and normalized
     * single-core CPU percentage (`[0.0, 1600.0]`) on subsequent observations.
     */
    fun sample(procStatLine: String, wallMs: Long): Double? {
        val ticks = parseProcessCpuTicks(procStatLine) ?: return null
        synchronized(lock) {
            val lastTicks = prevCpuTicks
            val lastWall = prevWallMs
            prevCpuTicks = ticks
            prevWallMs = wallMs
            if (lastTicks < 0L || lastWall < 0L) return null
            val dtWallMs = wallMs - lastWall
            if (dtWallMs <= 0L) return null
            val dtTicks = (ticks - lastTicks).coerceAtLeast(0L)
            val cpuSec = dtTicks.toDouble() / clockTicksPerSecond.coerceAtLeast(1L).toDouble()
            val wallSec = dtWallMs.toDouble() / 1000.0
            return ((cpuSec / wallSec) * 100.0).coerceIn(0.0, 1600.0)
        }
    }

    /** Convenience helper reading `/proc/self/stat` directly on Android/Linux hosts (fail-closed). */
    fun sampleSelf(wallMs: Long = System.currentTimeMillis()): Double? {
        val line = runCatching { File("/proc/self/stat").readText(Charsets.UTF_8) }.getOrNull() ?: return null
        return sample(line, wallMs)
    }

    companion object {
        /**
         * Extracts `utime + stime` (fields 14 and 15 in `/proc/<pid>/stat`, 1-based) by locating
         * the closing `')'` of the `comm` field so process names with spaces/parens parse reliably.
         */
        fun parseProcessCpuTicks(statLine: String): Long? {
            val rparen = statLine.lastIndexOf(')')
            if (rparen < 0 || rparen + 2 >= statLine.length) return null
            val fields = statLine.substring(rparen + 2).trim().split(Regex("\\s+"))
            // After ") ": index 0 is state (field 3), so utime (field 14) is index 11 and stime (field 15) is index 12.
            if (fields.size <= 12) return null
            val utime = fields[11].toLongOrNull() ?: return null
            val stime = fields[12].toLongOrNull() ?: return null
            if (utime < 0L || stime < 0L) return null
            return utime + stime
        }
    }
}
