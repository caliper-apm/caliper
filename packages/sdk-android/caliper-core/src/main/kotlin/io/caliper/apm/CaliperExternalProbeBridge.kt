package io.caliper.apm

/**
 * Stack frame supplied by an external deep collector (e.g. Tencent Matrix, iQIYI xCrash,
 * ByteDance btrace/Raphael, Bugly) bridged into Caliper APM with zero third-party SDK dependencies.
 */
data class ExternalStackFrame(
    val symbol: String,
    val module: String? = null,
    val file: String? = null,
    val line: Int? = null,
    val address: String? = null,
    val inApp: Boolean = true,
)

/**
 * Privacy-safe class-level retained memory dominator entry supplied by an external OOM/heap tool
 * (e.g. Kuaishou KOOM, ByteDance Tailor / Raphael, Tencent Matrix ResourceCanary).
 */
data class ExternalHeapDominator(
    val className: String,
    val instanceCount: Long,
    val retainedKiB: Double,
    val retainerPath: List<String> = emptyList(),
)

/**
 * Shared frame/module classifier distinguishing application code from Android/JDK/runtime frames.
 */
object AndroidFrameClassifier {
    private val systemClassPrefixes = listOf(
        "java.", "javax.", "kotlin.", "kotlinx.", "android.", "androidx.",
        "dalvik.", "com.android.", "sun.", "libcore.", "okhttp3.", "okio.",
    )
    private val systemNativePrefixes = listOf(
        "libc.", "libm.", "libdl.", "libart.", "libandroid_runtime.", "libbinder.",
        "libutils.", "libcutils.", "libhwui.", "libgui.", "libui.", "liblog.",
    )

    @JvmStatic
    fun isInAppClass(className: String): Boolean {
        val trimmed = className.trim()
        if (trimmed.isEmpty()) return false
        return systemClassPrefixes.none { trimmed.startsWith(it) }
    }

    @JvmStatic
    fun isInAppNativeModule(moduleName: String): Boolean {
        val trimmed = moduleName.trim()
        if (trimmed.isEmpty() || trimmed == "unknown" || trimmed == "anonymous") return false
        return systemNativePrefixes.none { trimmed.startsWith(it) }
    }
}

/**
 * Zero-dependency "Bring Your Own Deep Collector" bridge enabling teams running specialized
 * open-source Android diagnostic libraries — Tencent Matrix (`TraceCanary`, `IOCanary`, `SQLiteLint`,
 * `BatteryCanary`, `ResourceCanary`), Kuaishou KOOM, ByteDance `btrace`/`Tailor`/`Raphael`, or
 * iQIYI `xCrash` — to route their findings straight into Caliper APM's Zero-Lie analytics,
 * pure-Go R8/ELF DWARF Symbolicator, Heap Dominator comparator, and CI/CD Release Gate.
 */
object CaliperExternalProbeBridge {
    private val backtraceLineRegex = Regex("""#\d+\s+pc\s+([0-9a-fA-F]+)\s+(\S+)""")

    /**
     * Bridges a crash report from an external native/Java collector (e.g. xCrash, Matrix, Bugly)
     * into Caliper's `crash` pipeline with `bypassSampling = true` and strict schema bounds.
     */
    @JvmStatic
    @JvmOverloads
    fun ingestExternalCrash(
        client: CaliperClient,
        source: String,
        exceptionType: String,
        title: String,
        frames: List<ExternalStackFrame>,
        fatal: Boolean = true,
        threadName: String = "crashed-thread",
        fingerprint: String? = null,
        breadcrumbs: List<Pair<String, String>> = emptyList(),
        scene: String? = null,
        occurredAtMs: Long = System.currentTimeMillis(),
    ): Boolean {
        val cleanSrc = sanitizeTag(source, "external")
        val cleanExc = sanitizeTag(exceptionType, "ExternalCrash")
        val boundedFrames = frames.take(128)
        val top = boundedFrames.firstOrNull { it.inApp } ?: boundedFrames.firstOrNull()
        val rawFp = fingerprint ?: "ext-$cleanSrc-$cleanExc-${top?.module ?: top?.symbol ?: "unknown"}-${top?.address ?: top?.line ?: 0}"
        val safeFp = normalizeFingerprint(rawFp, "ext-$cleanSrc-crash")
        val safeTitle = normalizeTitle(title, "[$cleanSrc] $cleanExc")

        val framesJson = boundedFrames.joinToString(",") { encodeFrameJson(it) }
        val crumbsJson = breadcrumbs.takeLast(32).joinToString(",") { (cat, msg) ->
            """{"timestamp_ms":$occurredAtMs,"category":"${esc(sanitizeTag(cat, "ext"))}","message":"${esc(msg.take(200))}"}"""
        }
        val payload = buildString {
            append("""{"fingerprint":"${esc(safeFp)}","title":"${esc(safeTitle)}","fatal":$fatal,"exception_type":"${esc(cleanExc)}","thread_name":"${esc(threadName.take(64))}","frames":[$framesJson]""")
            if (crumbsJson.isNotEmpty()) append(""","breadcrumbs":[$crumbsJson]""")
            append('}')
        }
        return client.enqueue(
            kind = "crash",
            payloadJson = payload,
            scene = scene,
            occurredAtMs = occurredAtMs,
            bypassSampling = true,
        )
    }

    /**
     * Parses standard Android tombstone / xCrash native backtrace lines (`#00 pc 00042bc0 /.../libfoo.so`)
     * into ELF-relative [ExternalStackFrame] entries ready for Caliper Server's pure-Go ELF DWARF symbolicator.
     */
    @JvmStatic
    fun parseAndroidNativeBacktrace(rawBacktrace: String, maxFrames: Int = 48): List<ExternalStackFrame> {
        val limit = maxFrames.coerceIn(1, 128)
        val out = ArrayList<ExternalStackFrame>()
        for (line in rawBacktrace.lineSequence()) {
            if (out.size >= limit) break
            val m = backtraceLineRegex.find(line) ?: continue
            val rawHex = m.groupValues[1].trimStart('0').ifEmpty { "0" }.lowercase()
            val relHex = "0x$rawHex"
            val path = m.groupValues[2]
            val mod = path.substringAfterLast('/').ifEmpty { "unknown" }
            out.add(
                ExternalStackFrame(
                    symbol = relHex,
                    module = mod,
                    address = relHex,
                    inApp = AndroidFrameClassifier.isInAppNativeModule(mod),
                )
            )
        }
        return out
    }

    /**
     * Bridges an external ANR / slow-method / UI-lag event (e.g. Tencent Matrix `EvilMethodTracer`,
     * `SignalAnrTracer`, `TouchEventLagTracer`, or ByteDance `btrace`) into Caliper's `anr` pipeline.
     */
    @JvmStatic
    @JvmOverloads
    fun ingestExternalLagOrAnr(
        client: CaliperClient,
        source: String,
        durationMs: Long,
        frames: List<ExternalStackFrame>,
        title: String? = null,
        traceReference: String? = null,
        scene: String? = null,
        occurredAtMs: Long = System.currentTimeMillis(),
    ): Boolean {
        val cleanSrc = sanitizeTag(source, "external")
        val safeDur = durationMs.coerceAtLeast(1L)
        val boundedFrames = frames.take(128)
        val top = boundedFrames.firstOrNull { it.inApp } ?: boundedFrames.firstOrNull()
        val fp = normalizeFingerprint(
            "ext-$cleanSrc-lag-${top?.module ?: top?.symbol ?: "main"}-${top?.address ?: (safeDur / 500L * 500L)}",
            "ext-$cleanSrc-lag",
        )
        val safeTitle = normalizeTitle(
            title ?: "[$cleanSrc] Main-Thread Lag (${safeDur}ms)",
            "[$cleanSrc] Main-Thread Lag",
        )
        val framesJson = boundedFrames.joinToString(",") { encodeFrameJson(it) }
        val crumbsPart = if (!traceReference.isNullOrBlank()) {
            ""","breadcrumbs":[{"timestamp_ms":$occurredAtMs,"category":"external_trace","message":"${esc("$cleanSrc:$traceReference".take(200))}"}]"""
        } else {
            ""
        }
        val payload = """{"fingerprint":"${esc(fp)}","title":"${esc(safeTitle)}","fatal":false,"exception_type":"ExternalLag_$cleanSrc","thread_name":"main","frames":[$framesJson]$crumbsPart}"""
        return client.enqueue(
            kind = "anr",
            payloadJson = payload,
            scene = scene,
            occurredAtMs = occurredAtMs,
            bypassSampling = true,
        )
    }

    /**
     * Bridges privacy-safe class-level dominator summaries from an external OOM/heap analyzer
     * (e.g. Kuaishou KOOM, ByteDance Tailor / Raphael, Tencent Matrix `ResourceCanary`) into
     * Caliper's `heap_snapshot` pipeline to power cross-version leak regression detection
     * (`POST /v1/diagnostics/heap/compare`).
     */
    @JvmStatic
    @JvmOverloads
    fun ingestExternalHeapSnapshot(
        client: CaliperClient,
        triggerReason: String = "high_watermark",
        memoryKind: String = "android_pss_kib",
        totalAllocatedKiB: Double,
        totalRetainedKiB: Double,
        memoryLimitKiB: Double? = null,
        dominators: List<ExternalHeapDominator>,
        scene: String? = null,
        bypassSampling: Boolean = true,
    ): Boolean {
        val safeTrigger = when (triggerReason) {
            "oom_warning", "high_watermark", "manual" -> triggerReason
            else -> "high_watermark"
        }
        val safeMemKind = when (memoryKind) {
            "android_pss_kib", "hermes_heap_kib", "ios_phys_footprint_kib" -> memoryKind
            else -> "android_pss_kib"
        }
        val cleanDoms = dominators
            .mapNotNull { d ->
                val cls = d.className.trim()
                if (cls.isEmpty() || !d.retainedKiB.isFinite() || d.retainedKiB < 0.0) {
                    null
                } else {
                    d.copy(
                        className = cls.take(160),
                        instanceCount = d.instanceCount.coerceAtLeast(0L),
                        retainerPath = d.retainerPath.take(16).map { it.take(120) },
                    )
                }
            }
            .sortedByDescending { it.retainedKiB }
            .take(128)
        if (cleanDoms.isEmpty()) return false

        val safeAlloc = if (totalAllocatedKiB.isFinite() && totalAllocatedKiB >= 0.0) totalAllocatedKiB else 0.0
        val safeRet = if (totalRetainedKiB.isFinite() && totalRetainedKiB >= 0.0) totalRetainedKiB else 0.0
        val domsJson = cleanDoms.joinToString(",") { d ->
            buildString {
                append("""{"class_name":"${esc(d.className)}","instance_count":${d.instanceCount},"retained_kib":${d.retainedKiB}""")
                if (d.retainerPath.isNotEmpty()) {
                    val pathJson = d.retainerPath.joinToString(",") { """"${esc(it)}"""" }
                    append(""","retainer_path":[$pathJson]""")
                }
                append('}')
            }
        }
        val payload = buildString {
            append("""{"trigger_reason":"$safeTrigger","memory_kind":"$safeMemKind","total_allocated_kib":$safeAlloc,"total_retained_kib":$safeRet""")
            if (memoryLimitKiB != null && memoryLimitKiB.isFinite() && memoryLimitKiB >= 0.0) {
                append(""","memory_limit_kib":$memoryLimitKiB""")
            }
            append(""","dominators":[$domsJson]}""")
        }
        return client.enqueue(
            kind = "heap_snapshot",
            payloadJson = payload,
            scene = scene,
            bypassSampling = bypassSampling,
        )
    }

    /**
     * Bridges specialty diagnostics (e.g. Matrix `IOCanary` main-thread I/O, `SQLiteLint`,
     * `BatteryCanary` WakeLock alerts, or `btrace` Perfetto checkpoints) into `custom_scene`
     * journeys so they participate in Zero-Lie version comparison and `caliper-gate` CI gates.
     */
    @JvmStatic
    @JvmOverloads
    fun ingestExternalDiagnosticJourney(
        client: CaliperClient,
        source: String,
        diagnosticName: String,
        passed: Boolean,
        durationMs: Double? = null,
        stages: List<Pair<String, Double>> = emptyList(),
        errorCode: String? = null,
        scene: String? = null,
    ): Boolean {
        val cleanSrc = sanitizeTag(source, "ext")
        val cleanName = sanitizeTag(diagnosticName, "probe")
        val journey = "${cleanSrc}_$cleanName".take(64)
        val safeDur = durationMs?.takeIf { it.isFinite() && it >= 0.0 }?.coerceAtMost(600_000.0)
        val safeStages = stages.take(16).mapNotNull { (k, v) ->
            val name = k.trim().take(48)
            if (name.isEmpty() || !v.isFinite() || v < 0.0) null else name to v.coerceAtMost(600_000.0)
        }
        return client.recordCustomJourney(
            journeyName = journey,
            status = if (passed) "success" else "failure",
            durationMs = safeDur,
            stages = safeStages,
            errorCode = if (passed) null else (errorCode ?: "${cleanSrc.uppercase()}_REGRESSION"),
            scene = scene,
        )
    }

    private fun encodeFrameJson(f: ExternalStackFrame): String = buildString {
        append('{')
        if (!f.module.isNullOrBlank()) append(""""module":"${esc(f.module.take(96))}",""")
        append(""""symbol":"${esc(f.symbol.ifBlank { f.address ?: "unknown" }.take(240))}"""")
        if (!f.file.isNullOrBlank()) append(""","file":"${esc(f.file.take(160))}"""")
        if (f.line != null && f.line > 0) append(""","line":${f.line}""")
        if (!f.address.isNullOrBlank()) append(""","address":"${esc(f.address.take(32))}"""")
        append(""","in_app":${f.inApp}}""")
    }

    private fun sanitizeTag(raw: String, fallback: String): String {
        val cleaned = raw.filter { it.isLetterOrDigit() || it == '_' || it == '-' }.take(32)
        return cleaned.ifEmpty { fallback }
    }

    private fun normalizeFingerprint(raw: String, fallback: String): String {
        val trimmed = raw.trim()
        val base = if (trimmed.length >= 4) trimmed else fallback
        return base.take(128)
    }

    private fun normalizeTitle(raw: String, fallback: String): String {
        val cleaned = raw.replace('\n', ' ').replace('\r', ' ').trim()
        return (cleaned.ifEmpty { fallback }).take(256)
    }

    private fun esc(s: String): String =
        s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", " ")
}
