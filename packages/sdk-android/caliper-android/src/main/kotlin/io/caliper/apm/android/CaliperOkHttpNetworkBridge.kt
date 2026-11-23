package io.caliper.apm.android

import io.caliper.apm.CaliperClient
import io.caliper.apm.CaliperTraceContext
import okhttp3.Call
import okhttp3.Connection
import okhttp3.EventListener
import okhttp3.Handshake
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import java.io.IOException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Proxy
import java.net.SocketTimeoutException
import java.util.Collections
import java.util.WeakHashMap

/**
 * Production OkHttp 4.x W3C `traceparent` injector & transport waterfall instrumentation bridge.
 *
 * Automatically installed on every `OkHttpClient.Builder.build()` when the
 * `io.caliper.apm.autoinstrument` Gradle ASM9 plugin is applied (or manually via
 * [installOnBuilder]). Preserves host interceptors and existing `EventListener.Factory`.
 */
object CaliperOkHttpNetworkBridge {
    internal val callTraces: MutableMap<Call, CaliperTraceContext> =
        Collections.synchronizedMap(WeakHashMap())

    @JvmStatic
    fun installOnBuilder(
        builderObj: Any,
        client: CaliperClient,
        tracePropagationTargets: List<String> = client.getConfig().tracePropagationTargets,
    ) {
        val builder = builderObj as? OkHttpClient.Builder ?: return

        // 1. Idempotently install W3C traceparent interceptor (safe allowlist, preserves host headers).
        if (builder.interceptors().none { it is CaliperOkHttpInterceptor }) {
            builder.addInterceptor(
                CaliperOkHttpInterceptor(
                    tracePropagationTargets = tracePropagationTargets,
                    collectorEndpointUrl = client.getConfig().endpointUrl,
                ),
            )
        }

        // 2. Idempotently wrap EventListener.Factory without clobbering any host EventListener.
        val existingFactory = readExistingEventListenerFactory(builder)
        if (existingFactory !is CaliperEventListenerFactory) {
            builder.eventListenerFactory(
                CaliperEventListenerFactory(
                    client = client,
                    delegate = existingFactory,
                    collectorEndpointUrl = client.getConfig().endpointUrl,
                ),
            )
        }
    }

    private fun readExistingEventListenerFactory(builder: OkHttpClient.Builder): EventListener.Factory? {
        return runCatching {
            val field = OkHttpClient.Builder::class.java.getDeclaredField("eventListenerFactory")
            field.isAccessible = true
            field.get(builder) as? EventListener.Factory
        }.getOrNull()
    }
}

/**
 * OkHttp application interceptor injecting W3C Trace Context (`traceparent`) onto allowlisted
 * hosts and tagging the request with [CaliperTraceContext] for correlation inside [CaliperOkHttpEventListener].
 */
class CaliperOkHttpInterceptor(
    private val tracePropagationTargets: List<String>,
    private val collectorEndpointUrl: String? = null,
) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val req = chain.request()
        val host = req.url.host
        if (!CaliperTraceContext.shouldPropagate(host, tracePropagationTargets, collectorEndpointUrl)) {
            return chain.proceed(req)
        }

        val existing = CaliperTraceContext.parseTraceparent(req.header(CaliperTraceContext.HEADER_NAME))
        val ctx = existing ?: CaliperTraceContext.newRoot(sampled = true)
        CaliperOkHttpNetworkBridge.callTraces[chain.call()] = ctx

        val updatedReq = if (existing != null) {
            req.newBuilder()
                .tag(CaliperTraceContext::class.java, ctx)
                .build()
        } else {
            req.newBuilder()
                .header(CaliperTraceContext.HEADER_NAME, ctx.toTraceparent())
                .tag(CaliperTraceContext::class.java, ctx)
                .build()
        }
        return chain.proceed(updatedReq)
    }
}

class CaliperEventListenerFactory(
    private val client: CaliperClient,
    private val delegate: EventListener.Factory? = null,
    private val collectorEndpointUrl: String? = null,
) : EventListener.Factory {
    override fun create(call: Call): EventListener {
        val delegateListener = runCatching { delegate?.create(call) }.getOrNull()
        return CaliperOkHttpEventListener(
            client = client,
            delegate = delegateListener,
            collectorEndpointUrl = collectorEndpointUrl,
        )
    }
}

/**
 * Zero-PII OkHttp [EventListener] capturing DNS / TCP / TLS / TTFB / Body transport waterfalls,
 * HTTP status codes, protocol, connection reuse, and W3C `trace_id` / `span_id`.
 */
class CaliperOkHttpEventListener(
    private val client: CaliperClient,
    private val delegate: EventListener? = null,
    private val collectorEndpointUrl: String? = null,
) : EventListener() {

    private var callStartNs = 0L
    private var dnsStartNs = 0L
    private var dnsEndNs = 0L
    private var connectStartNs = 0L
    private var connectEndNs = 0L
    private var tlsStartNs = 0L
    private var tlsEndNs = 0L
    private var reqEndNs = 0L
    private var respHeadersStartNs = 0L
    private var respBodyStartNs = 0L
    private var respBodyEndNs = 0L

    private var statusCode = 0
    private var protocolName: String? = null
    private var connectionReused = true
    private var wireRequest: Request? = null
    private var emitted = false

    override fun callStart(call: Call) {
        callStartNs = System.nanoTime()
        delegate?.callStart(call)
    }

    override fun dnsStart(call: Call, domainName: String) {
        dnsStartNs = System.nanoTime()
        delegate?.dnsStart(call, domainName)
    }

    override fun dnsEnd(call: Call, domainName: String, inetAddressList: List<InetAddress>) {
        dnsEndNs = System.nanoTime()
        delegate?.dnsEnd(call, domainName, inetAddressList)
    }

    override fun connectStart(call: Call, inetSocketAddress: InetSocketAddress, proxy: Proxy) {
        connectStartNs = System.nanoTime()
        connectionReused = false
        delegate?.connectStart(call, inetSocketAddress, proxy)
    }

    override fun secureConnectStart(call: Call) {
        tlsStartNs = System.nanoTime()
        delegate?.secureConnectStart(call)
    }

    override fun secureConnectEnd(call: Call, handshake: Handshake?) {
        tlsEndNs = System.nanoTime()
        delegate?.secureConnectEnd(call, handshake)
    }

    override fun connectEnd(call: Call, inetSocketAddress: InetSocketAddress, proxy: Proxy, protocol: Protocol?) {
        connectEndNs = System.nanoTime()
        if (protocol != null) protocolName = protocol.toString()
        delegate?.connectEnd(call, inetSocketAddress, proxy, protocol)
    }

    override fun connectionAcquired(call: Call, connection: Connection) {
        if (protocolName == null) {
            protocolName = connection.protocol().toString()
        }
        delegate?.connectionAcquired(call, connection)
    }

    override fun requestHeadersEnd(call: Call, request: Request) {
        reqEndNs = System.nanoTime()
        wireRequest = request
        delegate?.requestHeadersEnd(call, request)
    }

    override fun requestBodyEnd(call: Call, byteCount: Long) {
        reqEndNs = System.nanoTime()
        delegate?.requestBodyEnd(call, byteCount)
    }

    override fun responseHeadersStart(call: Call) {
        respHeadersStartNs = System.nanoTime()
        delegate?.responseHeadersStart(call)
    }

    override fun responseHeadersEnd(call: Call, response: Response) {
        statusCode = response.code
        if (protocolName == null) {
            protocolName = response.protocol.toString()
        }
        delegate?.responseHeadersEnd(call, response)
    }

    override fun responseBodyStart(call: Call) {
        respBodyStartNs = System.nanoTime()
        delegate?.responseBodyStart(call)
    }

    override fun responseBodyEnd(call: Call, byteCount: Long) {
        respBodyEndNs = System.nanoTime()
        delegate?.responseBodyEnd(call, byteCount)
    }

    override fun callEnd(call: Call) {
        emit(call, error = null, cancelled = false)
        delegate?.callEnd(call)
    }

    override fun callFailed(call: Call, ioe: IOException) {
        emit(call, error = ioe, cancelled = false)
        delegate?.callFailed(call, ioe)
    }

    override fun canceled(call: Call) {
        emit(call, error = null, cancelled = true)
        delegate?.canceled(call)
    }

    private fun emit(call: Call, error: IOException?, cancelled: Boolean) {
        if (emitted) return
        emitted = true

        val req = wireRequest ?: call.request()
        val traceCtx = CaliperOkHttpNetworkBridge.callTraces.remove(call)
            ?: req.tag(CaliperTraceContext::class.java)
            ?: CaliperTraceContext.parseTraceparent(req.header(CaliperTraceContext.HEADER_NAME))
        // Never recursively report telemetry ingestion requests to Caliper itself.
        if (isCollectorIngestCall(req.url.host, req.url.port, req.url.encodedPath)) {
            return
        }

        val endNs = System.nanoTime()
        val totalMs = if (callStartNs > 0L) nsToMs(endNs - callStartNs) else 0.0
        val dnsMs = deltaMs(dnsStartNs, dnsEndNs)
        val tcpEnd = if (tlsStartNs > 0L) tlsStartNs else connectEndNs
        val tcpMs = deltaMs(connectStartNs, tcpEnd)
        val tlsMs = deltaMs(tlsStartNs, tlsEndNs)
        val ttfbMs = deltaMs(reqEndNs, respHeadersStartNs)
        val bodyMs = deltaMs(respBodyStartNs, respBodyEndNs)

        val method = when (req.method.uppercase()) {
            "GET", "POST", "PUT", "PATCH", "DELETE" -> req.method.uppercase()
            else -> "GET"
        }
        val op = req.url.encodedPath.ifEmpty { "/" }.take(128)
        val result = when {
            cancelled -> "cancelled"
            error is SocketTimeoutException -> "timeout"
            error != null -> "failed"
            statusCode >= 500 || statusCode == 0 -> "failed"
            else -> "success"
        }

        val payload = buildString {
            append("""{"operation":"${esc(op)}","method":"$method","status_code":$statusCode,"result":"$result"""")
            if (dnsMs != null) append(""","dns_ms":$dnsMs""")
            if (tcpMs != null) append(""","tcp_ms":$tcpMs""")
            if (tlsMs != null) append(""","tls_ms":$tlsMs""")
            if (ttfbMs != null) append(""","ttfb_ms":$ttfbMs""")
            if (bodyMs != null) append(""","body_ms":$bodyMs""")
            append(""","total_ms":$totalMs,"connection_reused":$connectionReused""")
            if (!protocolName.isNullOrEmpty()) append(""","protocol":"${esc(protocolName!!.take(16))}"""")
            if (traceCtx != null) {
                append(""","trace_id":"${traceCtx.traceId}","span_id":"${traceCtx.spanId}"""")
            }
            append('}')
        }
        client.enqueue(kind = "network_transport", payloadJson = payload)
    }

    private fun isCollectorIngestCall(host: String, port: Int, path: String): Boolean {
        val raw = collectorEndpointUrl ?: return false
        val uri = runCatching { java.net.URI(raw) }.getOrNull() ?: return false
        val cHost = uri.host ?: return false
        if (!host.equals(cHost, ignoreCase = true)) return false
        val cPort = if (uri.port > 0) uri.port else if (uri.scheme.equals("https", ignoreCase = true)) 443 else 80
        return port == cPort && path.startsWith("/v1/ingest")
    }

    private fun deltaMs(startNs: Long, endNs: Long): Double? {
        if (startNs <= 0L || endNs < startNs) return null
        return nsToMs(endNs - startNs)
    }

    private fun nsToMs(deltaNs: Long): Double = (deltaNs.coerceAtLeast(0L)) / 1_000_000.0

    private fun esc(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")

    companion object {
        @JvmStatic
        fun factory(client: CaliperClient, delegate: EventListener.Factory? = null): EventListener.Factory =
            CaliperEventListenerFactory(
                client = client,
                delegate = delegate,
                collectorEndpointUrl = client.getConfig().endpointUrl,
            )
    }
}
