package com.caliper.sample

import io.caliper.apm.CaliperClient
import io.caliper.apm.CaliperConfig
import io.caliper.apm.CaliperTraceContext
import io.caliper.apm.PolicyLease
import io.caliper.apm.ResourceIdentity
import io.caliper.apm.android.CaliperOkHttpNetworkBridge
import okhttp3.OkHttpClient
import okhttp3.Request
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.BufferedInputStream
import java.io.ByteArrayOutputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import java.util.zip.GZIPInputStream

/**
 * Real JVM integration test executing REAL OkHttp 4.12.0 + REAL CaliperOkHttpNetworkBridge
 * against live loopback TCP HTTP/1.1 servers (pure java.net.ServerSocket) and verifying:
 * 1. Backend receives a valid W3C `traceparent` header over the wire.
 * 2. CaliperOkHttpEventListener enqueues a `network_transport` event carrying the exact same
 *    `trace_id` & `span_id`, HTTP status 200, stripped URL path, and transport stage timings.
 */
class RealOkHttpTraceparentIntegrationTest {
    @Test
    fun injectsTraceparentOverRealOkHttp412AndCorrelatesTransportEvent() {
        val receivedTraceparent = AtomicReference<String?>()
        val backendSocket = ServerSocket(0, 4, InetAddress.getByName("127.0.0.1"))
        val backendDone = CountDownLatch(1)
        val backendThread = Thread {
            runCatching {
                backendSocket.accept().use { conn ->
                    val reader = conn.getInputStream().bufferedReader()
                    while (true) {
                        val line = reader.readLine() ?: break
                        if (line.isEmpty()) break
                        if (line.lowercase().startsWith("traceparent:")) {
                            receivedTraceparent.set(line.substringAfter(':').trim())
                        }
                    }
                    val body = """{"ok":true}"""
                    val resp = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: ${body.toByteArray().size}\r\nConnection: close\r\n\r\n$body"
                    conn.getOutputStream().write(resp.toByteArray())
                    conn.getOutputStream().flush()
                }
            }
            backendDone.countDown()
        }.apply { isDaemon = true; start() }

        val capturedBatch = AtomicReference<String?>()
        val collectorSocket = ServerSocket(0, 4, InetAddress.getByName("127.0.0.1"))
        val collectorDone = CountDownLatch(1)
        val collectorThread = Thread {
            runCatching {
                collectorSocket.accept().use { conn ->
                    val bis = BufferedInputStream(conn.getInputStream())
                    val headerLines = ArrayList<String>()
                    val lineBuf = ByteArrayOutputStream()
                    var prev = -1
                    while (true) {
                        val b = bis.read()
                        if (b == -1) break
                        if (prev == '\r'.code && b == '\n'.code) {
                            val line = lineBuf.toString(Charsets.UTF_8.name()).removeSuffix("\r")
                            lineBuf.reset()
                            if (line.isEmpty()) break
                            headerLines.add(line)
                        } else {
                            lineBuf.write(b)
                        }
                        prev = b
                    }
                    var contentLength = 0
                    var isGzip = false
                    for (h in headerLines) {
                        val lower = h.lowercase()
                        if (lower.startsWith("content-length:")) {
                            contentLength = h.substringAfter(':').trim().toIntOrNull() ?: 0
                        } else if (lower.startsWith("content-encoding:") && lower.contains("gzip")) {
                            isGzip = true
                        }
                    }
                    val bodyBytes = ByteArray(contentLength)
                    var read = 0
                    while (read < contentLength) {
                        val n = bis.read(bodyBytes, read, contentLength - read)
                        if (n <= 0) break
                        read += n
                    }
                    val text = if (isGzip) {
                        GZIPInputStream(bodyBytes.inputStream()).bufferedReader().readText()
                    } else {
                        String(bodyBytes, Charsets.UTF_8)
                    }
                    capturedBatch.set(text)
                    val ack = """{"accepted":1,"duplicate":0,"rejected":0}"""
                    val resp = "HTTP/1.1 202 Accepted\r\nContent-Type: application/json\r\nContent-Length: ${ack.toByteArray().size}\r\nConnection: close\r\n\r\n$ack"
                    conn.getOutputStream().write(resp.toByteArray())
                    conn.getOutputStream().flush()
                }
            }
            collectorDone.countDown()
        }.apply { isDaemon = true; start() }

        try {
            val client = CaliperClient(
                CaliperConfig(
                    endpointUrl = "http://127.0.0.1:${collectorSocket.localPort}/v1/ingest",
                    resource = ResourceIdentity(
                        appId = "com.caliper.sample",
                        platform = "android",
                        env = "prod",
                        appVersion = "1.9.0",
                        buildNumber = "190",
                        installHash = "0123456789abcdef0123456789abcdef",
                    ),
                    tracePropagationTargets = listOf("127.0.0.1"),
                ),
            )
            client.applyLease(
                PolicyLease(
                    policyId = "pol-okhttp-e2e",
                    ratePPM = 1_000_000L,
                    issuedAtMs = System.currentTimeMillis() - 1000L,
                    leaseTtlMs = 3_600_000L,
                    maxEvents24h = 100,
                    enabledCollectors = setOf("network"),
                ),
            )

            val builder = OkHttpClient.Builder()
            CaliperOkHttpNetworkBridge.installOnBuilder(builder, client)
            val okHttp = builder.build()

            val req = Request.Builder()
                .url("http://127.0.0.1:${backendSocket.localPort}/v1/orders/checkout?secret=do_not_log")
                .get()
                .build()
            okHttp.newCall(req).execute().use { resp ->
                assertEquals(200, resp.code)
            }
            assertTrue("Backend did not finish within 5s", backendDone.await(5, TimeUnit.SECONDS))

            val wireTraceparent = receivedTraceparent.get()
            assertNotNull("Backend server must receive W3C traceparent header", wireTraceparent)
            val parsedCtx = CaliperTraceContext.parseTraceparent(wireTraceparent)
            assertNotNull("Received traceparent must conform to W3C Trace Context Level 1: $wireTraceparent", parsedCtx)

            client.flushBlocking()
            assertTrue("Collector did not receive batch within 5s", collectorDone.await(5, TimeUnit.SECONDS))
            val batchJson = capturedBatch.get()
            assertNotNull("Expected telemetry batch delivered to collector", batchJson)
            assertTrue("Expected network_transport kind in batch: $batchJson", batchJson!!.contains("\"kind\":\"network_transport\""))
            assertTrue("Expected stripped path without query PII: $batchJson", batchJson.contains("\"operation\":\"/v1/orders/checkout\""))
            assertTrue("Must never leak URL query secrets: $batchJson", !batchJson.contains("do_not_log"))
            assertTrue("Expected HTTP status_code 200: $batchJson", batchJson.contains("\"status_code\":200"))
            assertTrue("Expected exact matching trace_id: $batchJson", batchJson.contains("\"trace_id\":\"${parsedCtx!!.traceId}\""))
            assertTrue("Expected exact matching span_id: $batchJson", batchJson.contains("\"span_id\":\"${parsedCtx.spanId}\""))
        } finally {
            runCatching { backendSocket.close() }
            runCatching { collectorSocket.close() }
            backendThread.interrupt()
            collectorThread.interrupt()
        }
    }
}
