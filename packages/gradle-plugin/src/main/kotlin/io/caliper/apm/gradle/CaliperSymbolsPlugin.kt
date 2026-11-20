package io.caliper.apm.gradle

import org.gradle.api.DefaultTask
import org.gradle.api.GradleException
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.file.RegularFileProperty
import org.gradle.api.provider.Property
import org.gradle.api.tasks.Input
import org.gradle.api.tasks.InputFile
import org.gradle.api.tasks.Optional
import org.gradle.api.tasks.TaskAction
import java.io.ByteArrayOutputStream
import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.security.MessageDigest
import java.util.zip.GZIPOutputStream

/**
 * Gradle DSL extension:
 * ```kotlin
 * caliperSymbols {
 *     serverUrl.set("https://apm.example.com")
 *     apiKey.set(System.getenv("CALIPER_API_KEY"))
 *     appId.set("dev.caliper.demo")
 *     appVersion.set("1.1.0")
 *     buildNumber.set("110")
 *     r8MappingFile.set(layout.buildDirectory.file("outputs/mapping/release/mapping.txt"))
 * }
 * ```
 */
abstract class CaliperSymbolsExtension {
    abstract val serverUrl: Property<String>
    abstract val apiKey: Property<String>
    abstract val appId: Property<String>
    abstract val appVersion: Property<String>
    abstract val buildNumber: Property<String>
    abstract val env: Property<String>
    abstract val gitSha: Property<String>
    abstract val adapter: Property<String>
    abstract val r8MappingFile: RegularFileProperty
    abstract val hermesSourceMapFile: RegularFileProperty
    abstract val dryRun: Property<Boolean>

    init {
        env.convention("prod")
        adapter.convention("native")
        dryRun.convention(false)
    }
}

/**
 * Gradle plugin `io.caliper.apm.symbols` wiring automatic Android release registration and
 * R8 / Hermes SourceMap symbol upload with zero external runtime dependencies.
 */
class CaliperSymbolsPlugin : Plugin<Project> {
    override fun apply(project: Project) {
        val ext = project.extensions.create("caliperSymbols", CaliperSymbolsExtension::class.java)
        project.tasks.register("caliperUploadSymbols", CaliperUploadSymbolsTask::class.java) { task ->
            task.group = "caliper"
            task.description = "Registers release metadata and uploads R8/Hermes symbol files to Caliper APM."
            task.serverUrl.set(ext.serverUrl)
            task.apiKey.set(ext.apiKey)
            task.appId.set(ext.appId)
            task.appVersion.set(ext.appVersion)
            task.buildNumber.set(ext.buildNumber)
            task.env.set(ext.env)
            task.gitSha.set(ext.gitSha)
            task.adapter.set(ext.adapter)
            task.r8MappingFile.set(ext.r8MappingFile)
            task.hermesSourceMapFile.set(ext.hermesSourceMapFile)
            task.dryRun.set(ext.dryRun)
        }
    }
}

abstract class CaliperUploadSymbolsTask : DefaultTask() {
    @get:Input abstract val serverUrl: Property<String>
    @get:Input @get:Optional abstract val apiKey: Property<String>
    @get:Input abstract val appId: Property<String>
    @get:Input abstract val appVersion: Property<String>
    @get:Input abstract val buildNumber: Property<String>
    @get:Input abstract val env: Property<String>
    @get:Input @get:Optional abstract val gitSha: Property<String>
    @get:Input abstract val adapter: Property<String>
    @get:InputFile @get:Optional abstract val r8MappingFile: RegularFileProperty
    @get:InputFile @get:Optional abstract val hermesSourceMapFile: RegularFileProperty
    @get:Input abstract val dryRun: Property<Boolean>

    @TaskAction
    fun run() {
        val resolvedSha = gitSha.orNull?.takeIf { it.isNotBlank() } ?: CaliperSymbolUploader.detectGitSha(project.projectDir)
        val cfg = CaliperUploadConfig(
            serverUrl = serverUrl.get(),
            apiKey = apiKey.orNull.orEmpty(),
            appId = appId.get(),
            platform = "android",
            env = env.get(),
            appVersion = appVersion.get(),
            buildNumber = buildNumber.get(),
            gitSha = resolvedSha,
            adapter = adapter.get(),
            dryRun = dryRun.get(),
        )
        val uploader = CaliperSymbolUploader()
        val receipts = mutableListOf<UploadReceipt>()
        uploader.registerRelease(cfg)
        r8MappingFile.orNull?.asFile?.takeIf { it.exists() }?.let { file ->
            receipts += uploader.uploadSymbol(cfg, "r8_mapping", file)
        }
        hermesSourceMapFile.orNull?.asFile?.takeIf { it.exists() }?.let { file ->
            receipts += uploader.uploadSymbol(cfg, "hermes_sourcemap", file)
        }
        if (receipts.isEmpty()) {
            throw GradleException("caliperUploadSymbols: neither r8MappingFile nor hermesSourceMapFile exists")
        }
        receipts.forEach { r ->
            logger.lifecycle("Caliper uploaded ${r.kind}: sha256=${r.sha256.take(12)} raw=${r.rawBytes}B gzip=${r.gzipBytes}B status=${r.httpStatus}")
        }
    }
}

data class CaliperUploadConfig(
    val serverUrl: String,
    val apiKey: String = "",
    val appId: String,
    val platform: String = "android",
    val env: String = "prod",
    val appVersion: String,
    val buildNumber: String,
    val gitSha: String = "",
    val adapter: String = "native",
    val dryRun: Boolean = false,
)

data class UploadReceipt(
    val kind: String,
    val sha256: String,
    val rawBytes: Int,
    val gzipBytes: Int,
    val httpStatus: Int,
    val responseBody: String,
)

/**
 * Pure-JVM HTTP uploader speaking Caliper APM's /v1/releases/register and /v1/symbols/upload APIs.
 */
class CaliperSymbolUploader(
    private val connectTimeoutMs: Int = 10_000,
    private val readTimeoutMs: Int = 30_000,
) {
    fun registerRelease(cfg: CaliperUploadConfig): Int {
        val base = cfg.serverUrl.trimEnd('/')
        val body = buildString {
            append("{")
            append("\"app_id\":\"${escapeJson(cfg.appId)}\",")
            append("\"platform\":\"${escapeJson(cfg.platform)}\",")
            append("\"env\":\"${escapeJson(cfg.env)}\",")
            append("\"app_version\":\"${escapeJson(cfg.appVersion)}\",")
            append("\"build_number\":\"${escapeJson(cfg.buildNumber)}\",")
            append("\"adapter\":\"${escapeJson(cfg.adapter)}\"")
            if (cfg.gitSha.isNotBlank()) {
                append(",\"git_sha\":\"${escapeJson(cfg.gitSha)}\"")
            }
            append("}")
        }
        if (cfg.dryRun) {
            return 200
        }
        val conn = openConnection("$base/v1/releases/register", cfg.apiKey)
        conn.setRequestProperty("Content-Type", "application/json")
        conn.outputStream.use { it.write(body.toByteArray(StandardCharsets.UTF_8)) }
        val code = conn.responseCode
        if (code !in 200..299) {
            val errBody = (conn.errorStream ?: conn.inputStream)?.bufferedReader()?.readText().orEmpty()
            throw GradleException("Caliper release register failed: HTTP $code: $errBody")
        }
        return code
    }

    fun uploadSymbol(cfg: CaliperUploadConfig, kind: String, file: File): UploadReceipt {
        val raw = file.readBytes()
        val digest = sha256Hex(raw)
        val gzBytes = gzipCompress(raw)
        if (cfg.dryRun) {
            return UploadReceipt(kind, digest, raw.size, gzBytes.size, 200, "{\"dry_run\":true}")
        }

        val qs = listOf(
            "app_id" to cfg.appId,
            "platform" to cfg.platform,
            "env" to cfg.env,
            "app_version" to cfg.appVersion,
            "build_number" to cfg.buildNumber,
            "kind" to kind,
            "git_sha" to cfg.gitSha,
        ).filter { it.second.isNotBlank() }.joinToString("&") { (k, v) ->
            "${URLEncoder.encode(k, "UTF-8")}=${URLEncoder.encode(v, "UTF-8")}"
        }
        val url = "${cfg.serverUrl.trimEnd('/')}/v1/symbols/upload?$qs"
        val conn = openConnection(url, cfg.apiKey)
        conn.setRequestProperty("Content-Type", "application/octet-stream")
        conn.setRequestProperty("Content-Encoding", "gzip")
        conn.setRequestProperty("X-Caliper-SHA256", digest)
        conn.outputStream.use { it.write(gzBytes) }

        val code = conn.responseCode
        val respText = (if (code in 200..299) conn.inputStream else conn.errorStream)
            ?.bufferedReader()?.readText().orEmpty()
        if (code !in 200..299) {
            throw GradleException("Caliper symbol upload ($kind) failed: HTTP $code: $respText")
        }
        return UploadReceipt(kind, digest, raw.size, gzBytes.size, code, respText)
    }

    private fun openConnection(urlStr: String, apiKey: String): HttpURLConnection {
        val conn = URI.create(urlStr).toURL().openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.connectTimeout = connectTimeoutMs
        conn.readTimeout = readTimeoutMs
        if (apiKey.isNotBlank()) {
            conn.setRequestProperty("X-Caliper-Key", apiKey)
        }
        return conn
    }

    companion object {
        fun sha256Hex(data: ByteArray): String {
            val md = MessageDigest.getInstance("SHA-256")
            return md.digest(data).joinToString("") { "%02x".format(it) }
        }

        fun gzipCompress(data: ByteArray): ByteArray {
            val out = ByteArrayOutputStream()
            GZIPOutputStream(out).use { it.write(data) }
            return out.toByteArray()
        }

        fun detectGitSha(workDir: File): String {
            return try {
                val proc = ProcessBuilder("git", "rev-parse", "--short=12", "HEAD")
                    .directory(workDir)
                    .redirectErrorStream(true)
                    .start()
                val text = proc.inputStream.bufferedReader().readText().trim()
                if (proc.waitFor() == 0 && Regex("^[a-fA-F0-9]{7,40}$").matches(text)) text else ""
            } catch (_: Exception) {
                ""
            }
        }

        private fun escapeJson(v: String): String =
            v.replace("\\", "\\\\").replace("\"", "\\\"")
    }
}
