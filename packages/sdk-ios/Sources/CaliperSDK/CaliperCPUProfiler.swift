import Foundation
import CaliperCrashCore

/// Bounded, ultra-low-overhead iOS/macOS Mach CPU sampling profiler emitting Caliper `cpu_profile`
/// folded-stack payloads compatible with server-side Mach-O `.dSYM` DWARF + Swift 5+ flamegraphs.
///
/// Hot-path optimization (matching production profilers):
/// - Inside the sampling loop, invokes ONLY `caliper_capture_main_thread_pcs` (pure Mach
///   `thread_suspend` -> `thread_get_state` + `vm_read_overwrite` -> `thread_resume`, zero
///   `dladdr`, zero string allocation, ~30 us per sample) and folds raw `[UInt]` PC stacks.
/// - After the profiling window completes, resolves each **unique** PC at most once via
///   `caliper_resolve_pc_frame` into bare slide-relative `0x...` hex (for stripped in-app DWARF)
///   or `<module>::<symbol>` (for resolved system/debug frames).
public final class CaliperIOSCPUProfiler: @unchecked Sendable {
    // Use .userInitiated (rather than .utility) so Darwin timer coalescing does not stretch
    // 10-15ms sampling intervals into ~100ms buckets during short on-demand profiling windows.
    private let queue = DispatchQueue(label: "dev.caliper.apm.cpu-profiler", qos: .userInitiated)

    public init() {}

    /// Synchronously samples the registered main thread from a **background** thread and returns a
    /// schema-compliant `CPUProfilePayload` (or `nil` if called on the main thread or 0 samples).
    public func sampleMainThreadBlocking(
        durationMS: Int = 2000,
        intervalMS: Int = 15,
        maxUniqueStacks: Int = 128,
        maxFrames: Int = 48
    ) -> CPUProfilePayload? {
        guard !Thread.isMainThread else {
            return nil // Never suspend the calling main thread!
        }

        let safeDurationMS = max(50, min(durationMS, 60_000))
        let safeIntervalMS = max(5, min(intervalMS, 200))
        let safeMaxStacks = max(1, min(maxUniqueStacks, 256))
        let safeMaxFrames = max(1, min(maxFrames, 64))

        var rawCounts: [[UInt]: Int64] = [:]
        var orderedRawKeys: [[UInt]] = []
        var totalSamples: Int64 = 0
        var pcBuf = Array<UInt>(repeating: 0, count: safeMaxFrames)

        let startUptime = ProcessInfo.processInfo.systemUptime
        let deadline = startUptime + Double(safeDurationMS) / 1000.0
        let sleepSec = Double(safeIntervalMS) / 1000.0

        // Phase 1: Hot sampling loop — ZERO dladdr, ZERO string formatting!
        while ProcessInfo.processInfo.systemUptime < deadline {
            let n = Int(caliper_capture_main_thread_pcs(&pcBuf, Int32(safeMaxFrames)))
            if n > 0 {
                let stack = Array(pcBuf.prefix(n))
                if let prev = rawCounts[stack] {
                    rawCounts[stack] = prev + 1
                    totalSamples += 1
                } else if rawCounts.count < safeMaxStacks {
                    rawCounts[stack] = 1
                    orderedRawKeys.append(stack)
                    totalSamples += 1
                }
            }
            Thread.sleep(forTimeInterval: sleepSec)
        }

        guard totalSamples > 0, !orderedRawKeys.isEmpty else {
            return nil
        }

        let elapsedMS = max(1, Int64((ProcessInfo.processInfo.systemUptime - startUptime) * 1000.0))

        // Phase 2: Resolve each unique PC at most ONCE after sampling finishes.
        var pcFrameCache: [UInt: StackFrame] = [:]
        var formattedCounts: [[String]: Int64] = [:]
        var orderedFormattedKeys: [[String]] = []

        for rawStack in orderedRawKeys {
            guard let sampleCount = rawCounts[rawStack], sampleCount > 0 else { continue }
            var leafFirst: [StackFrame] = []
            leafFirst.reserveCapacity(rawStack.count)
            for pc in rawStack {
                if let cached = pcFrameCache[pc] {
                    leafFirst.append(cached)
                } else {
                    var info = caliper_captured_frame_t()
                    caliper_resolve_pc_frame(pc, true, &info)
                    let mod = Self.cString(from: info.module)
                    let sym = Self.cString(from: info.symbol)
                    let relHex = "0x" + String(info.relative_addr, radix: 16)
                    let frame = StackFrame(
                        module: mod.isEmpty ? "unknown" : mod,
                        symbol: sym.isEmpty ? relHex : sym,
                        address: relHex,
                        inApp: CaliperModuleClassifier.isInAppModule(mod)
                    )
                    pcFrameCache[pc] = frame
                    leafFirst.append(frame)
                }
            }
            let folded = Self.formatRootToLeafFrames(leafFirst)
            guard !folded.isEmpty else { continue }
            if let prev = formattedCounts[folded] {
                formattedCounts[folded] = prev + sampleCount
            } else {
                formattedCounts[folded] = sampleCount
                orderedFormattedKeys.append(folded)
            }
        }

        let samples = orderedFormattedKeys.compactMap { frames -> ProfileStackSample? in
            guard let n = formattedCounts[frames], n > 0 else { return nil }
            return ProfileStackSample(frames: frames, samples: n)
        }
        guard !samples.isEmpty else { return nil }

        return CPUProfilePayload(
            runtime: "native",
            threadName: "main",
            sampleIntervalMS: safeIntervalMS,
            durationMS: min(elapsedMS, 300_000),
            totalSamples: totalSamples,
            stacks: samples
        )
    }

    /// Asynchronously profiles the main thread off-main and enqueues `kind = "cpu_profile"` into `client`.
    public func sampleMainThreadAsync(
        client: CaliperClient,
        scene: String? = nil,
        durationMS: Int = 2000,
        intervalMS: Int = 15,
        maxUniqueStacks: Int = 128,
        bypassSampling: Bool = false,
        completion: (@Sendable (CPUProfilePayload?) -> Void)? = nil
    ) {
        if Thread.isMainThread {
            caliper_register_main_thread()
        } else {
            DispatchQueue.main.async {
                caliper_register_main_thread()
            }
        }
        queue.async { [weak self] in
            guard let self else {
                completion?(nil)
                return
            }
            let profile = self.sampleMainThreadBlocking(
                durationMS: durationMS,
                intervalMS: intervalMS,
                maxUniqueStacks: maxUniqueStacks
            )
            if let profile {
                _ = client.enqueue(
                    kind: "cpu_profile",
                    payload: .cpuProfile(profile),
                    scene: scene,
                    bypassSampling: bypassSampling
                )
            }
            completion?(profile)
        }
    }

    /// Formats leaf-first `StackFrame`s into root-to-leaf flamegraph strings:
    /// - Stripped in-app frames (`symbol == address`) emit bare slide-relative `0x...` hex so
    ///   Caliper Server's `ResolveLines` automatically symbolicates them via Mach-O DWARF + Swift 5+ demangler.
    /// - Frames with resolved symbols emit `<module>::<symbol>` for immediate readability.
    internal static func formatRootToLeafFrames(_ leafFirst: [StackFrame]) -> [String] {
        leafFirst.reversed().prefix(64).map { frame in
            let addr = frame.address ?? ""
            if frame.inApp == true && !addr.isEmpty && frame.symbol == addr {
                return addr
            }
            let mod = frame.module ?? "unknown"
            return "\(mod)::\(frame.symbol)"
        }
    }

    private static func cString<T>(from tuple: T) -> String {
        withUnsafeBytes(of: tuple) { raw in
            let bytes = raw.prefix { $0 != 0 }
            return String(decoding: bytes, as: UTF8.self)
        }
    }
}
