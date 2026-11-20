package io.caliper.apm.gradle

import com.sun.net.httpserver.HttpServer
import org.gradle.testfixtures.ProjectBuilder
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.File
import java.net.InetSocketAddress
import java.nio.charset.StandardCharsets
import java.util.concurrent.CopyOnWriteArrayList
import java.util.zip.GZIPInputStream

class CaliperSymbolsPluginTest {

    @Test
    fun pluginRegistersTaskAndUploadsR8AndSourceMap() {
        val requests = CopyOnWriteArrayList<Triple<String, String, String>>()
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/v1/releases/register") { ex ->
            val body = ex.requestBody.readBytes().toString(StandardCharsets.UTF_8)
            requests += Triple("/v1/releases/register", ex.requestHeaders.getFirst("X-Caliper-Key").orEmpty(), body)
            val resp = """{"status":"registered"}""".toByteArray()
            ex.sendResponseHeaders(200, resp.size.toLong())
            ex.responseBody.use { it.write(resp) }
        }
        server.createContext("/v1/symbols/upload") { ex ->
            val rawGz = ex.requestBody.readBytes()
            val decompressed = GZIPInputStream(ByteArrayInputStream(rawGz)).readBytes()
            val expectedSha = ex.requestHeaders.getFirst("X-Caliper-SHA256")
            val actualSha = CaliperSymbolUploader.sha256Hex(decompressed)
            assertEquals("SHA-256 header must match decompressed payload", expectedSha, actualSha)
            requests += Triple(
                "/v1/symbols/upload?${ex.requestURI.rawQuery}",
                ex.requestHeaders.getFirst("X-Caliper-Key").orEmpty(),
                decompressed.toString(StandardCharsets.UTF_8),
            )
            val resp = """{"symbol_id":"sym-test-1"}""".toByteArray()
            ex.sendResponseHeaders(201, resp.size.toLong())
            ex.responseBody.use { it.write(resp) }
        }
        server.start()

        try {
            val project = ProjectBuilder.builder().build()
            project.plugins.apply("io.caliper.apm.symbols")

            val task = project.tasks.findByName("caliperUploadSymbols") as? CaliperUploadSymbolsTask
            assertNotNull("caliperUploadSymbols task must be registered", task)

            val r8File = File(project.projectDir, "mapping.txt").apply {
                writeText("com.example.Feed -> a.b:\n    void decode() -> a\n")
            }
            val mapFile = File(project.projectDir, "index.android.bundle.map").apply {
                writeText("""{"version":3,"sources":["App.tsx"],"names":[],"mappings":"AAAA"}""")
            }

            val ext = project.extensions.getByType(CaliperSymbolsExtension::class.java)
            ext.serverUrl.set("http://127.0.0.1:${server.address.port}")
            ext.apiKey.set("ci-secret-key")
            ext.appId.set("dev.caliper.demo")
            ext.appVersion.set("1.1.0")
            ext.buildNumber.set("110")
            ext.gitSha.set("1100002abcdef")
            ext.r8MappingFile.set(r8File)
            ext.hermesSourceMapFile.set(mapFile)

            task!!.run()

            assertEquals(3, requests.size)
            assertEquals("/v1/releases/register", requests[0].first)
            assertEquals("ci-secret-key", requests[0].second)
            assertTrue(requests[0].third.contains("\"app_version\":\"1.1.0\""))
            assertTrue(requests[1].first.contains("kind=r8_mapping"))
            assertTrue(requests[1].third.contains("com.example.Feed"))
            assertTrue(requests[2].first.contains("kind=hermes_sourcemap"))
            assertTrue(requests[2].third.contains("\"version\":3"))
        } finally {
            server.stop(0)
        }
    }
}
