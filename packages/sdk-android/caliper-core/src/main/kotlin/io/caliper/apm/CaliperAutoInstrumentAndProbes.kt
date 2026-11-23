package io.caliper.apm

import java.io.File

/**
 * Compile-time annotation recognized by the `io.caliper.apm.autoinstrument` Gradle ASM plugin.
 * Methods annotated with [@CaliperTrace] are automatically wrapped with [CaliperAutoInstrumentHooks.beginTrace]
 * and [CaliperAutoInstrumentHooks.endTrace] (covering both normal returns and uncaught exceptions).
 */
@Target(AnnotationTarget.FUNCTION)
@Retention(AnnotationRetention.BINARY)
annotation class CaliperTrace(
    val name: String = "",
    val scene: String = "",
)

/**
 * Reflection-free static hook entrypoints invoked by ASM-woven bytecode (`io.caliper.apm.autoinstrument`).
 * Wired automatically by `CaliperAndroidAgent.install(...)`; no-op when unconfigured.
 */
object CaliperAutoInstrumentHooks {
    @Volatile
    var traceSink: ((name: String, scene: String, durationMs: Double, failed: Boolean) -> Unit)? = null

    @Volatile
    var okHttpBuilderHook: ((builder: Any) -> Unit)? = null

    @Volatile
    var activityStageSink: ((activityName: String, stage: String, durationMs: Double) -> Unit)? = null

    @JvmStatic
    fun beginTrace(name: String): Long = System.nanoTime()

    @JvmStatic
    fun endTrace(name: String, scene: String, startNanos: Long, failed: Boolean) {
        val sink = traceSink ?: return
        val durMs = ((System.nanoTime() - startNanos).coerceAtLeast(0L)) / 1_000_000.0
        runCatching { sink.invoke(name, scene, durMs, failed) }
    }

    @JvmStatic
    fun onOkHttpBuilder(builder: Any): Any {
        val hook = okHttpBuilderHook
        if (hook != null) {
            runCatching { hook.invoke(builder) }
        }
        return builder
    }

    @JvmStatic
    fun onActivityStage(activityName: String, stage: String, durationMs: Double) {
        val sink = activityStageSink ?: return
        runCatching { sink.invoke(activityName, stage, durationMs.coerceAtLeast(0.0)) }
    }
}

// ===== Battery & Thermal State Evaluator =====

data class ThermalBatterySnapshot(
    val thermalLevel: String,      // nominal | light | moderate | severe | critical | emergency
    val batteryLevelPct: Int,      // 0..100 or -1 if unknown
    val batteryTempCelsius: Double,// e.g. 36.5
    val isCharging: Boolean,
    val scene: String = "unknown",
)

object ThermalBatteryEvaluator {
    fun isSevere(snap: ThermalBatterySnapshot): Boolean =
        snap.thermalLevel == "severe" ||
            snap.thermalLevel == "critical" ||
            snap.thermalLevel == "emergency" ||
            snap.batteryTempCelsius >= 42.0

    fun record(client: CaliperClient, snap: ThermalBatterySnapshot): Boolean {
        val severe = isSevere(snap)
        val status = if (severe) "failure" else "success"
        val errCode = when {
            snap.thermalLevel == "critical" || snap.thermalLevel == "emergency" -> "THERMAL_CRITICAL"
            snap.thermalLevel == "severe" || snap.batteryTempCelsius >= 42.0 -> "THERMAL_SEVERE"
            else -> null
        }
        val stages = buildList {
            if (snap.batteryTempCelsius > 0) add("battery_temp_c" to snap.batteryTempCelsius)
            if (snap.batteryLevelPct >= 0) add("battery_pct" to snap.batteryLevelPct.toDouble())
        }
        return client.recordCustomJourney(
            journeyName = "device_thermal_state",
            status = status,
            durationMs = snap.batteryTempCelsius.coerceAtLeast(0.0),
            stages = stages,
            errorCode = errCode,
            scene = snap.scene,
        )
    }
}

// ===== Bounded, Zero-PII Sandbox Disk Storage Auditor =====

data class DiskBucketStat(
    val bucket: String,
    val totalBytes: Long,
    val fileCount: Int,
)

data class DiskLargeFile(
    val sanitizedPath: String,
    val sizeBytes: Long,
)

data class DiskAuditReport(
    val totalBytes: Long,
    val visitedFiles: Int,
    val truncated: Boolean,
    val buckets: List<DiskBucketStat>,
    val largeFiles: List<DiskLargeFile>,
)

/**
 * Bounded, Zero-PII sandbox disk auditor (max depth, max visited files, 24h cooldown).
 * Strips UUIDs, hex hashes, emails, and numeric user IDs from file paths before telemetry reporting.
 */
object CaliperDiskAuditor {
    private val uuidRegex = Regex("[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
    private val hexHashRegex = Regex("[0-9a-fA-F]{16,}")
    private val emailRegex = Regex("[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}")
    private val numericIdRegex = Regex("(?<![0-9])\\d{6,}(?![0-9])")

    fun sanitizePath(relativePath: String): String {
        var out = relativePath.replace('\\', '/')
        out = emailRegex.replace(out, "<email>")
        out = uuidRegex.replace(out, "<uuid>")
        out = hexHashRegex.replace(out, "<hash>")
        out = numericIdRegex.replace(out, "<id>")
        return out
    }

    fun auditDirectories(
        roots: Map<String, File>,
        maxDepth: Int = 4,
        maxFiles: Int = 2000,
        largeFileThresholdBytes: Long = 20L * 1024L * 1024L,
    ): DiskAuditReport {
        var totalBytes = 0L
        var visited = 0
        var truncated = false
        val buckets = ArrayList<DiskBucketStat>(roots.size)
        val largeFiles = ArrayList<DiskLargeFile>()

        for ((bucketName, rootDir) in roots) {
            if (!rootDir.exists() || !rootDir.isDirectory) {
                buckets.add(DiskBucketStat(bucketName, 0L, 0))
                continue
            }
            var bucketBytes = 0L
            var bucketCount = 0

            fun walk(dir: File, depth: Int) {
                if (depth > maxDepth || visited >= maxFiles) {
                    if (visited >= maxFiles) truncated = true
                    return
                }
                val children = dir.listFiles() ?: return
                for (child in children) {
                    if (visited >= maxFiles) {
                        truncated = true
                        return
                    }
                    if (child.isDirectory) {
                        walk(child, depth + 1)
                    } else if (child.isFile) {
                        visited++
                        val sz = child.length().coerceAtLeast(0L)
                        bucketBytes += sz
                        bucketCount++
                        if (sz >= largeFileThresholdBytes && largeFiles.size < 10) {
                            val rel = runCatching { child.relativeTo(rootDir).path }.getOrDefault(child.name)
                            largeFiles.add(DiskLargeFile("$bucketName/${sanitizePath(rel)}", sz))
                        }
                    }
                }
            }
            walk(rootDir, 0)
            totalBytes += bucketBytes
            buckets.add(DiskBucketStat(bucketName, bucketBytes, bucketCount))
        }

        return DiskAuditReport(
            totalBytes = totalBytes,
            visitedFiles = visited,
            truncated = truncated,
            buckets = buckets,
            largeFiles = largeFiles.sortedByDescending { it.sizeBytes },
        )
    }

    fun recordAudit(
        client: CaliperClient,
        report: DiskAuditReport,
        quotaBudgetMiB: Double = 500.0,
    ): Boolean {
        val totalMiB = report.totalBytes / (1024.0 * 1024.0)
        val exceeded = totalMiB > quotaBudgetMiB
        val stages = report.buckets.map { b ->
            "${b.bucket}_mb" to (b.totalBytes / (1024.0 * 1024.0))
        }
        return client.recordCustomJourney(
            journeyName = "disk_storage_audit",
            status = if (exceeded) "failure" else "success",
            durationMs = totalMiB,
            stages = stages,
            errorCode = if (exceeded) "DISK_QUOTA_EXCEEDED" else null,
            scene = "disk_audit",
        )
    }
}

// ===== WebView / H5 Performance & Blank-Screen Probe =====

data class WebViewMetricsPayload(
    val normalizedUrl: String,
    val dnsMs: Double,
    val tcpMs: Double,
    val ttfbMs: Double,
    val domInteractiveMs: Double,
    val loadCompleteMs: Double,
    val fcpMs: Double,
    val blankScreen: Boolean,
    val jsErrorMessage: String = "",
)

/**
 * Shared lightweight vanilla JS probe (~1.3 KB) and PII-safe parser for Android WebView & iOS WKWebView.
 * Strips URL query strings / tokens and emits Zero-Lie `custom_scene` (`webview_page_load`) + JS error events.
 */
object CaliperWebViewProbe {
    const val INJECTED_JS: String = """(function(){if(window.__caliperWV)return;window.__caliperWV=1;function emit(p){try{var s=JSON.stringify(p);if(window.CaliperWV&&window.CaliperWV.postMessage){window.CaliperWV.postMessage(s);}else if(window.webkit&&window.webkit.messageHandlers&&window.webkit.messageHandlers.caliperWV){window.webkit.messageHandlers.caliperWV.postMessage(s);}}catch(_){}}var fcp=0;try{new PerformanceObserver(function(l){l.getEntries().forEach(function(e){if(e.name==='first-contentful-paint')fcp=e.startTime;});}).observe({type:'paint',buffered:true});}catch(_){}window.addEventListener('error',function(e){emit({type:'js_error',url:location.origin+location.pathname,message:String(e.message||'Script error')});});window.addEventListener('unhandledrejection',function(e){emit({type:'js_error',url:location.origin+location.pathname,message:String(e.reason||'Unhandled rejection')});});function reportNav(){try{var n=performance.getEntriesByType('navigation')[0];var txt=(document.body&&document.body.innerText||'').trim();var nodes=document.body?document.body.querySelectorAll('*').length:0;var blank=(txt.length===0&&nodes<4);emit({type:'nav',url:location.origin+location.pathname,dns_ms:n?Math.max(0,n.domainLookupEnd-n.domainLookupStart):0,tcp_ms:n?Math.max(0,n.connectEnd-n.connectStart):0,ttfb_ms:n?Math.max(0,n.responseStart-n.requestStart):0,dom_ms:n?Math.max(0,n.domInteractive-n.startTime):0,load_ms:n?Math.max(0,n.loadEventEnd-n.startTime):0,fcp_ms:Math.max(0,fcp),blank:blank});}catch(_){}}if(document.readyState==='complete'){setTimeout(reportNav,50);}else{window.addEventListener('load',function(){setTimeout(reportNav,50);});}})();"""

    fun normalizeUrl(rawUrl: String): String {
        val noQuery = rawUrl.substringBefore('?').substringBefore('#').trim()
        return if (noQuery.isEmpty()) "webview://unknown" else noQuery.take(180)
    }

    fun ingestMessageJson(rawJson: String, client: CaliperClient): Boolean {
        val type = extractJsonStr(rawJson, "type")
        val normUrl = normalizeUrl(extractJsonStr(rawJson, "url"))
        if (type == "js_error") {
            val msg = extractJsonStr(rawJson, "message").ifEmpty { "WebView JS Error" }.take(200)
            val fp = "webview-jserr-${normUrl.hashCode()}-${msg.hashCode()}"
            val payload = """{"fingerprint":"${esc(fp)}","title":"WebView JS Error: ${esc(msg)}","fatal":false,"exception_type":"WebViewJSError","thread_name":"webview-js","frames":[{"module":"webview","symbol":"${esc(normUrl)}","in_app":true}]}"""
            return client.enqueue(kind = "crash", payloadJson = payload, scene = normUrl, bypassSampling = true)
        }
        if (type == "nav") {
            val dns = extractJsonNum(rawJson, "dns_ms")
            val tcp = extractJsonNum(rawJson, "tcp_ms")
            val ttfb = extractJsonNum(rawJson, "ttfb_ms")
            val dom = extractJsonNum(rawJson, "dom_ms")
            val load = extractJsonNum(rawJson, "load_ms")
            val fcp = extractJsonNum(rawJson, "fcp_ms")
            val blank = rawJson.contains("\"blank\":true")
            val total = maxOf(load, dom, fcp, ttfb)
            return client.recordCustomJourney(
                journeyName = "webview_page_load",
                status = if (blank) "failure" else "success",
                durationMs = total,
                stages = listOf(
                    "dns" to dns,
                    "tcp" to tcp,
                    "ttfb" to ttfb,
                    "dom_interactive" to dom,
                    "fcp" to fcp,
                    "load_complete" to load,
                ),
                errorCode = if (blank) "WEBVIEW_BLANK_SCREEN" else null,
                scene = normUrl,
            )
        }
        return false
    }

    private fun extractJsonStr(json: String, key: String): String {
        val marker = "\"$key\":\""
        val start = json.indexOf(marker)
        if (start < 0) return ""
        val i0 = start + marker.length
        val sb = StringBuilder()
        var i = i0
        while (i < json.length) {
            val ch = json[i]
            if (ch == '\\' && i + 1 < json.length) {
                sb.append(json[i + 1])
                i += 2
                continue
            }
            if (ch == '"') break
            sb.append(ch)
            i++
        }
        return sb.toString()
    }

    private fun extractJsonNum(json: String, key: String): Double {
        val marker = "\"$key\":"
        val start = json.indexOf(marker)
        if (start < 0) return 0.0
        var i = start + marker.length
        while (i < json.length && json[i] == ' ') i++
        val numStart = i
        while (i < json.length && (json[i].isDigit() || json[i] == '.' || json[i] == '-')) i++
        return json.substring(numStart, i).toDoubleOrNull()?.coerceAtLeast(0.0) ?: 0.0
    }

    private fun esc(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")
}
