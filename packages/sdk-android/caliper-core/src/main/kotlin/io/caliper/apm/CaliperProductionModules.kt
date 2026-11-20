package io.caliper.apm

import java.io.ByteArrayOutputStream
import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import java.net.URLEncoder
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong
import java.util.zip.GZIPOutputStream

/**
 * Bounded FIFO offline disk queue surviving process death & network outages.
 * Enforces strict maxFiles and maxTotalBytes caps via oldest-first eviction.
 */
class CaliperDiskQueue(
    private val dir: File,
    private val maxFiles: Int = 20,
    private val maxTotalBytes: Long = 2L * 1024L * 1024L,
) {
    private val lock = Any()
    private val seq = AtomicLong(0)

    init {
        if (!dir.exists()) dir.mkdirs()
    }

    fun enqueue(payloadJson: String): Boolean = synchronized(lock) {
        return runCatching {
            if (!dir.exists()) dir.mkdirs()
            val bytes = payloadJson.toByteArray(Charsets.UTF_8)
            if (bytes.size > maxTotalBytes) return false
            val id = "${System.currentTimeMillis()}-${seq.incrementAndGet()}"
            val tmp = File(dir, "$id.batch.tmp")
            val finalFile = File(dir, "$id.batch.json")
            tmp.writeBytes(bytes)
            if (!tmp.renameTo(finalFile)) {
                tmp.delete()
                return false
            }
            evictLocked()
            true
        }.getOrDefault(false)
    }

    fun count(): Int = synchronized(lock) { listBatchFilesLocked().size }

    fun drainEach(sender: (String) -> Unit): Int = synchronized(lock) {
        var sent = 0
        for (f in listBatchFilesLocked()) {
            val content = runCatching { f.readText(Charsets.UTF_8) }.getOrNull() ?: continue
            try {
                sender(content)
                f.delete()
                sent++
            } catch (_: Throwable) {
                // Stop draining on first transport error; keep remaining files intact.
                break
            }
        }
        sent
    }

    private fun listBatchFilesLocked(): List<File> =
        dir.listFiles { f -> f.isFile && f.name.endsWith(".batch.json") }
            ?.sortedBy { it.name }
            ?: emptyList()

    private fun evictLocked() {
        val files = listBatchFilesLocked().toMutableList()
        var total = files.sumOf { it.length() }
        while (files.size > maxFiles || total > maxTotalBytes) {
            val oldest = files.removeAt(0)
            total -= oldest.length()
            oldest.delete()
        }
    }
}

/**
 * Background low-priority Main-Thread Watchdog for ANR and UI stall stack capture.
 * Posts lightweight heartbeats onto the main thread runner; when heartbeat stalls exceed
 * [hangThresholdMs], captures exact main-thread stack frames without blocking UI execution.
 */
class CaliperWatchdog(
    private val mainThread: Thread,
    private val postToMain: (Runnable) -> Unit,
    private val hangThresholdMs: Long = 2000L,
    private val pollIntervalMs: Long = 250L,
    private val onHangDetected: (durationMs: Long, stackFrames: List<String>) -> Unit,
) {
    private val running = AtomicBoolean(false)
    private val pendingAck = AtomicBoolean(false)
    private var worker: Thread? = null

    fun start() {
        if (!running.compareAndSet(false, true)) return
        worker = Thread({
            var stallStartMs = 0L
            var reportedForCurrentStall = false
            while (running.get()) {
                if (!pendingAck.get()) {
                    pendingAck.set(true)
                    stallStartMs = System.currentTimeMillis()
                    reportedForCurrentStall = false
                    runCatching {
                        postToMain(Runnable { pendingAck.set(false) })
                    }
                } else {
                    val stalledFor = System.currentTimeMillis() - stallStartMs
                    if (stalledFor >= hangThresholdMs && !reportedForCurrentStall) {
                        reportedForCurrentStall = true
                        val frames = mainThread.stackTrace
                            .take(32)
                            .map { "${it.className}.${it.methodName}(${it.fileName ?: "Unknown"}:${it.lineNumber})" }
                        runCatching { onHangDetected(stalledFor, frames) }
                    }
                }
                try {
                    Thread.sleep(pollIntervalMs)
                } catch (_: InterruptedException) {
                    break
                }
            }
        }, "caliper-watchdog").apply {
            isDaemon = true
            priority = Thread.MIN_PRIORITY
            start()
        }
    }

    fun stop() {
        running.set(false)
        worker?.interrupt()
        worker = null
    }
}

/**
 * Bounded folded-stack CPU sampling profiler emitting Caliper v1.0 `cpu_profile` JSON
 * compatible with automatic server-side R8 flamegraph symbolication.
 */
class CaliperCpuProfiler {
    fun sampleThreadBlocking(
        targetThread: Thread,
        durationMs: Long = 2000L,
        intervalMs: Int = 10,
        maxUniqueStacks: Int = 128,
    ): String {
        val counts = LinkedHashMap<List<String>, Long>()
        val started = System.currentTimeMillis()
        var totalSamples = 0L
        val safeInterval = intervalMs.coerceIn(5, 200)
        val safeDuration = durationMs.coerceIn(50L, 60_000L)

        while (System.currentTimeMillis() - started < safeDuration) {
            val raw = targetThread.stackTrace
            if (raw.isNotEmpty()) {
                // Reverse to root-to-leaf flamegraph order and bound to 64 frames.
                val folded = raw.reversed().take(64).map {
                    "${it.className}.${it.methodName}(${it.fileName ?: "SourceFile"}:${it.lineNumber})"
                }
                val prev = counts[folded]
                if (prev != null) {
                    counts[folded] = prev + 1
                    totalSamples++
                } else if (counts.size < maxUniqueStacks) {
                    counts[folded] = 1
                    totalSamples++
                }
            }
            try {
                Thread.sleep(safeInterval.toLong())
            } catch (_: InterruptedException) {
                break
            }
        }
        val elapsed = (System.currentTimeMillis() - started).coerceAtLeast(1L)
        return buildString {
            append("""{"runtime":"native","thread_name":"${escape(targetThread.name)}","sample_interval_ms":$safeInterval,"duration_ms":$elapsed,"total_samples":$totalSamples,"stacks":[""")
            var idx = 0
            for ((frames, samples) in counts) {
                if (idx++ > 0) append(',')
                append("""{"frames":[""")
                frames.forEachIndexed { fIdx, f ->
                    if (fIdx > 0) append(',')
                    append('"').append(escape(f)).append('"')
                }
                append("""],"samples":$samples}""")
            }
            append("]}")
        }
    }

    private fun escape(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")
}

/**
 * Bounded PII-scrubbed diagnostic ring log buffer paired with audited remote pull tasks.
 */
class CaliperDiagnosticRingLog(private val maxLines: Int = 500) {
    private val lock = Any()
    private val lines = ArrayDeque<String>(maxLines)
    private val emailRegex = Regex("""[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}""")
    private val bearerRegex = Regex("""(?i)(bearer\s+)[A-Za-z0-9._-]+""")

    fun append(level: String, tag: String, message: String) {
        val scrubbed = message
            .replace(emailRegex, "[REDACTED_EMAIL]")
            .replace(bearerRegex, "$1[REDACTED_TOKEN]")
        val line = "${System.currentTimeMillis()} [$level] $tag: $scrubbed"
        synchronized(lock) {
            if (lines.size >= maxLines) lines.removeFirst()
            lines.addLast(line)
        }
    }

    fun dumpText(): String = synchronized(lock) { lines.joinToString("\n") }
}

/**
 * Polls `/v1/diagnostics/log-tasks/pending` and uploads gzipped ring logs when an operator
 * initiates an audited remote diagnostic pull task.
 */
class CaliperRemoteLogAgent(
    private val config: CaliperConfig,
    private val ringLog: CaliperDiagnosticRingLog,
) {
    private val taskIdRegex = Regex(""""task_id"\s*:\s*"([^"]+)"""")

    fun pollAndFulfillOnce(): Int {
        val base = config.endpointUrl.trimEnd('/')
        val qs = buildList {
            add("app_id=${URLEncoder.encode(config.resource.appId, "UTF-8")}")
            add("platform=${URLEncoder.encode(config.resource.platform, "UTF-8")}")
            add("install_hash=${URLEncoder.encode(config.resource.installHash, "UTF-8")}")
            config.resource.userHash?.let { add("user_hash=${URLEncoder.encode(it, "UTF-8")}") }
        }.joinToString("&")

        val pollConn = (URI.create("$base/v1/diagnostics/log-tasks/pending?$qs").toURL().openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 8_000
            readTimeout = 8_000
            config.apiKey?.takeIf { it.isNotEmpty() }?.let { setRequestProperty("X-Caliper-Key", it) }
        }
        val body = try {
            if (pollConn.responseCode !in 200..299) return 0
            pollConn.inputStream.bufferedReader(Charsets.UTF_8).readText()
        } finally {
            pollConn.disconnect()
        }

        val taskIds = taskIdRegex.findAll(body).map { it.groupValues[1] }.toList()
        if (taskIds.isEmpty()) return 0

        val logBytes = ringLog.dumpText().toByteArray(Charsets.UTF_8)
        val gzOut = ByteArrayOutputStream()
        GZIPOutputStream(gzOut).use { it.write(logBytes) }
        val gzPayload = gzOut.toByteArray()

        var fulfilled = 0
        for (taskId in taskIds) {
            val upConn = (URI.create("$base/v1/diagnostics/log-tasks/$taskId/upload").toURL().openConnection() as HttpURLConnection).apply {
                requestMethod = "POST"
                doOutput = true
                connectTimeout = 10_000
                readTimeout = 10_000
                setRequestProperty("Content-Encoding", "gzip")
                setRequestProperty("Content-Type", "text/plain; charset=utf-8")
                config.apiKey?.takeIf { it.isNotEmpty() }?.let { setRequestProperty("X-Caliper-Key", it) }
            }
            try {
                upConn.outputStream.use { it.write(gzPayload) }
                if (upConn.responseCode in 200..299) fulfilled++
            } finally {
                upConn.disconnect()
            }
        }
        return fulfilled
    }
}
