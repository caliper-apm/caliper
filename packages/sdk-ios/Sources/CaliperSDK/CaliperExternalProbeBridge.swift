import Foundation

/// Zero-dependency adapter ("Bring Your Own Deep Collector") enabling teams running deep third-party
/// iOS collectors — such as Tencent Matrix-iOS (`WCCrashBlockMonitor`, `WCMemoryStat`, `WCFPSMonitor`),
/// KSCrash, PLCrashReporter, or ByteDance `btrace` — to route their native findings directly into
/// Caliper APM's Zero-Lie analytics engine, pure-Go Mach-O DWARF/Swift 5+ Symbolicator,
/// Heap Dominator leak comparator, and CI/CD Release Gate.
public enum CaliperExternalProbeBridge {

    /// Normalizes and enqueues a crash report captured by an external collector (e.g. KSCrash,
    /// Matrix-iOS `WCCrashBlockMonitor`, PLCrashReporter) with `bypassSampling: true`.
    @discardableResult
    public static func ingestExternalCrash(
        client: CaliperClient,
        source: String,
        exceptionType: String,
        title: String,
        frames: [StackFrame],
        fatal: Bool = true,
        threadName: String = "crashed-thread",
        fingerprint: String? = nil,
        breadcrumbs: [Breadcrumb]? = nil,
        scene: String? = nil,
        occurredAtMS: Int64 = Int64(Date().timeIntervalSince1970 * 1000)
    ) -> Bool {
        let cleanSource = sanitizeTag(source, fallback: "external")
        let cleanExc = sanitizeTag(exceptionType, fallback: "ExternalCrash")
        let boundedFrames = Array(frames.prefix(128))
        let top = boundedFrames.first(where: { $0.inApp == true }) ?? boundedFrames.first
        let rawFP = fingerprint ?? "ext-\(cleanSource)-\(cleanExc)-\(top?.module ?? "app")-\(top?.address ?? top?.symbol ?? "0x0")"
        let safeFP = normalizeFingerprint(rawFP, fallback: "ext-\(cleanSource)-crash")
        let safeTitle = normalizeTitle(title, fallback: "[\(cleanSource)] \(cleanExc)")
        let boundedCrumbs = breadcrumbs.map { Array($0.suffix(32)) }

        return client.enqueue(
            kind: "crash",
            payload: .stability(StabilityPayload(
                fingerprint: safeFP,
                title: safeTitle,
                fatal: fatal,
                exceptionType: cleanExc,
                threadName: threadName,
                frames: boundedFrames,
                breadcrumbs: boundedCrumbs
            )),
            scene: scene,
            occurredAtMS: occurredAtMS,
            bypassSampling: true
        )
    }

    /// Normalizes and enqueues a main-thread lag / hang / block tree from an external probe
    /// (e.g. Matrix-iOS `WCBlockMonitorMgr` or `btrace`) as a Caliper `anr` event.
    @discardableResult
    public static func ingestExternalHangOrLag(
        client: CaliperClient,
        source: String,
        durationMS: Int64,
        frames: [StackFrame],
        title: String? = nil,
        traceReference: String? = nil,
        scene: String? = nil,
        occurredAtMS: Int64 = Int64(Date().timeIntervalSince1970 * 1000)
    ) -> Bool {
        let cleanSource = sanitizeTag(source, fallback: "external")
        let safeDur = max(1, durationMS)
        let boundedFrames = Array(frames.prefix(128))
        let top = boundedFrames.first(where: { $0.inApp == true }) ?? boundedFrames.first
        let fp = normalizeFingerprint(
            "ext-\(cleanSource)-hang-\(top?.module ?? "main")-\(top?.address ?? top?.symbol ?? "\(safeDur / 500 * 500)")",
            fallback: "ext-\(cleanSource)-hang"
        )
        let resolvedTitle = normalizeTitle(
            title ?? "[\(cleanSource)] Main-Thread Lag (\(safeDur)ms)",
            fallback: "[\(cleanSource)] Main-Thread Lag"
        )
        var crumbs: [Breadcrumb] = []
        if let ref = traceReference, !ref.isEmpty {
            crumbs.append(Breadcrumb(
                timestampMS: occurredAtMS,
                category: "external_trace",
                message: String("\(cleanSource):\(ref)".prefix(200))
            ))
        }

        return client.enqueue(
            kind: "anr",
            payload: .stability(StabilityPayload(
                fingerprint: fp,
                title: resolvedTitle,
                fatal: false,
                exceptionType: "ExternalLag_\(cleanSource)",
                threadName: "main",
                frames: boundedFrames,
                breadcrumbs: crumbs.isEmpty ? nil : crumbs
            )),
            scene: scene,
            occurredAtMS: occurredAtMS,
            bypassSampling: true
        )
    }

    /// Bridges trimmed class/allocator dominator summaries from an external OOM/memory probe
    /// (e.g. Matrix-iOS `WCMemoryStat`, custom malloc logger) into Caliper's `heap_snapshot`
    /// pipeline, directly powering `POST /v1/diagnostics/heap/compare` leak regression detection.
    @discardableResult
    public static func ingestExternalHeapSnapshot(
        client: CaliperClient,
        triggerReason: String = "high_watermark",
        memoryKind: String = "ios_phys_footprint_kib",
        totalAllocatedKiB: Double,
        totalRetainedKiB: Double,
        memoryLimitKiB: Double? = nil,
        dominators: [HeapDominator],
        scene: String? = nil,
        bypassSampling: Bool = true
    ) -> Bool {
        let validTriggers: Set<String> = ["oom_warning", "high_watermark", "manual"]
        let validMemKinds: Set<String> = ["ios_phys_footprint_kib", "hermes_heap_kib"]
        let safeTrigger = validTriggers.contains(triggerReason) ? triggerReason : "high_watermark"
        let safeMemKind = validMemKinds.contains(memoryKind) ? memoryKind : "ios_phys_footprint_kib"

        let cleanDoms = dominators.compactMap { d -> HeapDominator? in
            let cls = d.className.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !cls.isEmpty, d.retainedKiB.isFinite, d.retainedKiB >= 0 else { return nil }
            let path = d.retainerPath.map { Array($0.prefix(16)) }
            return HeapDominator(
                className: String(cls.prefix(160)),
                instanceCount: max(0, d.instanceCount),
                retainedKiB: d.retainedKiB,
                retainerPath: path?.isEmpty == true ? nil : path
            )
        }
        guard !cleanDoms.isEmpty else { return false }

        let boundedDoms = Array(cleanDoms.sorted { $0.retainedKiB > $1.retainedKiB }.prefix(128))
        let safeAlloc = (totalAllocatedKiB.isFinite && totalAllocatedKiB >= 0) ? totalAllocatedKiB : 0.0
        let safeRet = (totalRetainedKiB.isFinite && totalRetainedKiB >= 0) ? totalRetainedKiB : 0.0
        let safeLimit: Double? = (memoryLimitKiB.map { ($0.isFinite && $0 >= 0) ? $0 : nil }) ?? nil

        return client.enqueue(
            kind: "heap_snapshot",
            payload: .heapSnapshot(HeapSnapshotPayload(
                triggerReason: safeTrigger,
                memoryKind: safeMemKind,
                totalAllocatedKiB: safeAlloc,
                totalRetainedKiB: safeRet,
                memoryLimitKiB: safeLimit,
                dominators: boundedDoms
            )),
            scene: scene,
            bypassSampling: bypassSampling
        )
    }

    /// Bridges specialty diagnostics (e.g. `btrace` Perfetto trace checkpoints, Matrix-iOS FPS/IO
    /// alerts, custom power/thermal probes) into Caliper `custom_scene` journeys so they participate
    /// in Zero-Lie statistical comparison and `caliper-gate` CI release gates.
    @discardableResult
    public static func ingestExternalDiagnosticJourney(
        client: CaliperClient,
        source: String,
        diagnosticName: String,
        passed: Bool,
        durationMS: Double? = nil,
        stages: [SceneStage]? = nil,
        errorCode: String? = nil,
        scene: String? = nil
    ) -> Bool {
        let cleanSource = sanitizeTag(source, fallback: "ext")
        let cleanName = sanitizeTag(diagnosticName, fallback: "probe")
        let journey = String("\(cleanSource)_\(cleanName)".prefix(64))
        let safeDur: Double? = durationMS.flatMap { ($0.isFinite && $0 >= 0) ? min($0, 600_000.0) : nil }
        let safeStages: [SceneStage]? = stages.map { list in
            list.prefix(16).compactMap { st in
                let name = String(st.name.trimmingCharacters(in: .whitespacesAndNewlines).prefix(48))
                guard !name.isEmpty, st.durationMS.isFinite, st.durationMS >= 0 else { return nil }
                return SceneStage(name: name, durationMS: min(st.durationMS, 600_000.0))
            }
        }
        return client.recordCustomJourney(
            journeyName: journey,
            status: passed ? "success" : "failure",
            durationMS: safeDur,
            stages: safeStages?.isEmpty == true ? nil : safeStages,
            errorCode: passed ? nil : (errorCode ?? "\(cleanSource.uppercased())_REGRESSION"),
            scene: scene
        )
    }

    private static func sanitizeTag(_ raw: String, fallback: String) -> String {
        let allowed = raw.unicodeScalars.filter {
            CharacterSet.alphanumerics.contains($0) || $0 == "_" || $0 == "-"
        }
        let s = String(String.UnicodeScalarView(allowed))
        return s.isEmpty ? fallback : String(s.prefix(32))
    }

    private static func normalizeFingerprint(_ raw: String, fallback: String) -> String {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        let base = trimmed.count >= 4 ? trimmed : fallback
        return String(base.prefix(128))
    }

    private static func normalizeTitle(_ raw: String, fallback: String) -> String {
        let cleaned = raw
            .replacingOccurrences(of: "\n", with: " ")
            .replacingOccurrences(of: "\r", with: " ")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return String((cleaned.isEmpty ? fallback : cleaned).prefix(256))
    }
}
