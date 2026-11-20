package io.caliper.apm

import java.io.ByteArrayOutputStream
import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.zip.GZIPOutputStream

data class ResourceIdentity(
    val appId: String,
    val platform: String = "android",
    val env: String = "prod",
    val appVersion: String,
    val buildNumber: String,
    val gitSha: String? = null,
    val symbolId: String? = null,
    val adapter: String = "native",
    val sdkName: String = "caliper-android",
    val sdkVersion: String = "1.1.0",
    val installHash: String,
    val userHash: String? = null,
    val osVersion: String? = null,
    val deviceModel: String? = null
)

data class CaliperConfig(
    val endpointUrl: String,
    val apiKey: String? = null,
    val resource: ResourceIdentity,
    val installSeed: String = UUID.randomUUID().toString(),
    val maxQueueSize: Int = 200,
    val maxConsecutiveFailures: Int = 5,
    val spoolDir: File? = null
)

data class TelemetryEvent(
    val eventId: String = UUID.randomUUID().toString(),
    val kind: String,
    val occurredAtMs: Long,
    val contractVersion: Int = 1,
    val scene: String? = null,
    val windowId: String? = null,
    val policyId: String? = null,
    val sampleRatePPM: Long? = null,
    val payloadJson: String
)

/**
 * Thread-safe, off-main-thread, fail-closed Pure-Kotlin Native Android APM runtime.
 * Zero React Native dependencies; optional RN adapters call setScene / enqueueRaw / addBreadcrumb.
 */
class CaliperClient(private val config: CaliperConfig) {
    private val worker = Executors.newSingleThreadExecutor { r ->
        Thread(r, "caliper-apm-worker").apply {
            isDaemon = true
            priority = Thread.MIN_PRIORITY
        }
    }

    private val lock = Any()
    private var currentScene: String = "startup"
    private val breadcrumbs = BreadcrumbRingBuffer(32)
    private val queue = ArrayDeque<TelemetryEvent>(config.maxQueueSize.coerceIn(1, 200))
    private var lease: PolicyLease? = null
    private var quota = SlidingQuota(86_400_000L, 50)
    private var consecutiveFailures = 0
    private var circuitTripped = false
    private val diskSpool: CaliperDiskQueue? = config.spoolDir?.let { CaliperDiskQueue(it) }

    fun getConfig(): CaliperConfig = config
    fun currentSceneName(): String = synchronized(lock) { currentScene }

    fun applyLease(nextLease: PolicyLease) = synchronized(lock) {
        lease = nextLease
        quota = SlidingQuota(86_400_000L, nextLease.maxEvents24h)
    }

    fun setScene(scene: String) {
        synchronized(lock) { currentScene = scene }
        breadcrumbs.append(System.currentTimeMillis(), "navigation", "scene:$scene")
    }

    fun addBreadcrumb(category: String, message: String) {
        breadcrumbs.append(System.currentTimeMillis(), category, message)
    }

    fun breadcrumbsSnapshot(): List<BreadcrumbRingBuffer.Crumb> = breadcrumbs.snapshot()

    fun enqueue(
        kind: String,
        payloadJson: String,
        scene: String? = null,
        windowId: String? = null,
        occurredAtMs: Long = System.currentTimeMillis(),
        bypassSampling: Boolean = false
    ): Boolean = synchronized(lock) {
        if (circuitTripped) return false
        var activePolicyId: String? = null
        var activeRatePPM: Long? = null

        if (!bypassSampling) {
            val active = lease ?: return false
            if (!active.isActive(occurredAtMs, config.installSeed)) return false
            if (!quota.tryConsume(occurredAtMs)) return false
            activePolicyId = active.policyId
            activeRatePPM = active.ratePPM
        }

        val maxCap = config.maxQueueSize.coerceIn(1, 200)
        while (queue.size >= maxCap) queue.removeFirst()
        queue.addLast(
            TelemetryEvent(
                kind = kind,
                occurredAtMs = occurredAtMs,
                scene = scene ?: currentScene,
                windowId = windowId,
                policyId = activePolicyId,
                sampleRatePPM = activeRatePPM,
                payloadJson = payloadJson
            )
        )
        true
    }

    fun pendingCount(): Int = synchronized(lock) { queue.size }

    fun flushAsync(onComplete: ((Int, Throwable?) -> Unit)? = null) {
        worker.submit {
            val result = runCatching { flushBlocking() }
            onComplete?.invoke(result.getOrDefault(0), result.exceptionOrNull())
        }
    }

    /** Gzip-compresses and posts queued batch (+ offline disk spool) to POST /v1/ingest; fail-closed on repeated errors. */
    fun flushBlocking(): Int {
        var sentCount = 0
        // 1. Drain any previously spooled offline batches first.
        diskSpool?.drainEach { spooledJson ->
            postGzipJson(spooledJson)
            sentCount += 1
        }

        val batch: List<TelemetryEvent> = synchronized(lock) {
            if (circuitTripped || queue.isEmpty()) return sentCount
            val copy = queue.toList()
            queue.clear()
            copy
        }
        val bodyJson = encodeBatchJson(System.currentTimeMillis(), config.resource, batch)
        return try {
            postGzipJson(bodyJson)
            synchronized(lock) { consecutiveFailures = 0 }
            sentCount + batch.size
        } catch (t: Throwable) {
            diskSpool?.enqueue(bodyJson)
            recordFailure()
            throw t
        }
    }

    private fun postGzipJson(bodyJson: String) {
        val compressed = gzip(bodyJson.toByteArray(Charsets.UTF_8))
        val conn = (URI.create("${config.endpointUrl.trimEnd('/')}/v1/ingest").toURL().openConnection() as HttpURLConnection).apply {
            requestMethod = "POST"
            connectTimeout = 10_000
            readTimeout = 10_000
            doOutput = true
            setRequestProperty("Content-Type", "application/json")
            setRequestProperty("Content-Encoding", "gzip")
            config.apiKey?.takeIf { it.isNotEmpty() }?.let { setRequestProperty("X-Caliper-Key", it) }
        }
        try {
            conn.outputStream.use { it.write(compressed) }
            val status = conn.responseCode
            if (status !in 200..299) {
                throw IllegalStateException("ingest HTTP $status")
            }
        } finally {
            conn.disconnect()
        }
    }

    fun shutdown() {
        worker.shutdown()
        runCatching { worker.awaitTermination(2, TimeUnit.SECONDS) }
    }

    private fun recordFailure() = synchronized(lock) {
        consecutiveFailures += 1
        if (consecutiveFailures >= config.maxConsecutiveFailures) {
            circuitTripped = true
        }
    }

    companion object {
        internal fun encodeBatchJson(sentAtMs: Long, r: ResourceIdentity, events: List<TelemetryEvent>): String {
            val sb = StringBuilder(256 + events.size * 180)
            sb.append("""{"schema_version":1,"sent_at_ms":$sentAtMs,"resource":{""")
            sb.append(""""app_id":${q(r.appId)},"platform":${q(r.platform)},"env":${q(r.env)},""")
            sb.append(""""app_version":${q(r.appVersion)},"build_number":${q(r.buildNumber)},""")
            r.gitSha?.let { sb.append(""""git_sha":${q(it)},""") }
            r.symbolId?.let { sb.append(""""symbol_id":${q(it)},""") }
            sb.append(""""adapter":${q(r.adapter)},"sdk_name":${q(r.sdkName)},"sdk_version":${q(r.sdkVersion)},""")
            sb.append(""""install_hash":${q(r.installHash)}""")
            r.userHash?.let { sb.append(""","user_hash":${q(it)}""") }
            r.osVersion?.let { sb.append(""","os_version":${q(it)}""") }
            r.deviceModel?.let { sb.append(""","device_model":${q(it)}""") }
            sb.append("""},"events":[""")
            events.forEachIndexed { idx, ev ->
                if (idx > 0) sb.append(',')
                sb.append("""{"event_id":${q(ev.eventId)},"kind":${q(ev.kind)},"occurred_at_ms":${ev.occurredAtMs},"contract_version":${ev.contractVersion}""")
                ev.scene?.let { sb.append(""","scene":${q(it)}""") }
                ev.windowId?.let { sb.append(""","window_id":${q(it)}""") }
                ev.policyId?.let { sb.append(""","policy_id":${q(it)}""") }
                ev.sampleRatePPM?.let { sb.append(""","sample_rate_ppm":$it""") }
                sb.append(""","payload":${ev.payloadJson}}""")
            }
            sb.append("]}")
            return sb.toString()
        }

        private fun q(s: String): String {
            val escaped = buildString(s.length + 2) {
                append('"')
                for (ch in s) {
                    when (ch) {
                        '"' -> append("\\\"")
                        '\\' -> append("\\\\")
                        '\n' -> append("\\n")
                        '\r' -> append("\\r")
                        '\t' -> append("\\t")
                        else -> append(ch)
                    }
                }
                append('"')
            }
            return escaped
        }

        private fun gzip(raw: ByteArray): ByteArray {
            val out = ByteArrayOutputStream(raw.size)
            GZIPOutputStream(out).use { it.write(raw) }
            return out.toByteArray()
        }
    }
}
