package io.caliper.apm

/**
 * Zero-Lie cross-platform contract primitives (byte-for-byte parity with contract-vectors.json).
 * Pure Kotlin with zero React Native or third-party dependencies.
 */
object CaliperContract {
    const val MAX_SAMPLING_RATE_PPM: Long = 1_000_000L
    const val HISTOGRAM_BUCKET_COUNT: Int = 19

    val DURATION_BOUNDS_MS: DoubleArray = doubleArrayOf(
        16.0, 33.0, 50.0, 100.0, 250.0, 500.0, 750.0, 1000.0, 1500.0, 2000.0,
        3000.0, 5000.0, 8000.0, 10000.0, 15000.0, 30000.0, 60000.0, 120000.0
    )

    val MEMORY_BOUNDS_MIB: DoubleArray = doubleArrayOf(
        64.0, 96.0, 128.0, 160.0, 192.0, 224.0, 256.0, 320.0, 384.0, 448.0,
        512.0, 640.0, 768.0, 896.0, 1024.0, 1536.0, 2048.0, 4096.0
    )

    /** Unsigned 32-bit FNV-1a over ASCII bytes, returned as non-negative Long in [0, 2^32-1]. */
    fun fnv1a32(input: String): Long {
        var hash: UInt = 2166136261u
        for (byte in input.toByteArray(Charsets.UTF_8)) {
            hash = (hash xor byte.toUByte().toUInt()) * 16777619u
        }
        return hash.toLong()
    }

    fun samplingBucketPPM(installSeed: String, policyId: String): Long {
        return fnv1a32("$installSeed|$policyId") % MAX_SAMPLING_RATE_PPM
    }

    fun isAdmitted(installSeed: String, policyId: String, ratePPM: Long): Boolean {
        if (ratePPM <= 0L) return false
        if (ratePPM >= MAX_SAMPLING_RATE_PPM) return true
        return samplingBucketPPM(installSeed, policyId) < ratePPM
    }

    fun durationBucketIndex(ms: Double): Int = bucketIndex(DURATION_BOUNDS_MS, ms)

    fun memoryBucketIndex(mib: Double): Int = bucketIndex(MEMORY_BOUNDS_MIB, mib)

    private fun bucketIndex(bounds: DoubleArray, value: Double): Int {
        if (!value.isFinite() || value < 0.0) return bounds.size
        for (i in bounds.indices) {
            if (value <= bounds[i]) return i
        }
        return bounds.size
    }

    fun validateStartupStages(
        totalMs: Double,
        nativeInitMs: Double? = null,
        runtimeInitMs: Double? = null,
        firstFrameMs: Double? = null
    ): Boolean {
        if (!totalMs.isFinite() || totalMs < 0.0) return false
        var sum = 0.0
        for (stage in listOfNotNull(nativeInitMs, runtimeInitMs, firstFrameMs)) {
            if (!stage.isFinite() || stage < 0.0) return false
            sum += stage
        }
        return sum <= totalMs + 1e-6
    }
}

data class FluencyAssessment(
    val jankRatio: Double?,
    val severity: String
)

/** Accumulates frame intervals in a bounded window; frameCount == 0 MUST yield unknown. */
class FluencyWindowAccumulator {
    var frameCount: Long = 0
        private set
    var jankFrameCount: Long = 0
        private set
    var severeJankFrameCount: Long = 0
        private set
    val frameDurationBuckets: LongArray = LongArray(CaliperContract.HISTOGRAM_BUCKET_COUNT)

    fun recordFrame(durationMs: Double, targetMs: Double) {
        if (!durationMs.isFinite() || durationMs < 0.0 || !targetMs.isFinite() || targetMs <= 0.0) return
        frameCount += 1
        frameDurationBuckets[CaliperContract.durationBucketIndex(durationMs)] += 1
        if (durationMs > targetMs * 1.5) jankFrameCount += 1
        if (durationMs > targetMs * 3.0) severeJankFrameCount += 1
    }

    fun classify(): FluencyAssessment = classify(frameCount, jankFrameCount)

    companion object {
        fun classify(frameCount: Long, jankFrameCount: Long): FluencyAssessment {
            if (frameCount <= 0L || jankFrameCount < 0L || jankFrameCount > frameCount) {
                return FluencyAssessment(jankRatio = null, severity = "unknown")
            }
            val ratio = jankFrameCount.toDouble() / frameCount.toDouble()
            val severity = when {
                ratio >= 0.10 -> "critical"
                ratio >= 0.03 -> "degraded"
                else -> "healthy"
            }
            return FluencyAssessment(jankRatio = ratio, severity = severity)
        }
    }
}

/** Rolling 24h (or configurable window) installation event cap. */
class SlidingQuota(
    val windowMs: Long,
    val cap: Int
) {
    private val timestamps = ArrayDeque<Long>()

    fun tryConsume(timestampMs: Long): Boolean {
        if (cap <= 0 || windowMs <= 0L) return false
        val cutoff = timestampMs - windowMs
        while (timestamps.isNotEmpty() && timestamps.first() <= cutoff) {
            timestamps.removeFirst()
        }
        if (timestamps.size >= cap) return false
        timestamps.addLast(timestampMs)
        return true
    }
}

/** Fail-closed remote policy lease; expired leases automatically stop collection. */
data class PolicyLease(
    val policyId: String,
    val ratePPM: Long,
    val issuedAtMs: Long,
    val leaseTtlMs: Long,
    val maxEvents24h: Int,
    val enabledCollectors: Set<String>
) {
    fun isActive(nowMs: Long, installSeed: String): Boolean {
        if (leaseTtlMs <= 0L || nowMs < issuedAtMs || (nowMs - issuedAtMs) >= leaseTtlMs) {
            return false
        }
        return CaliperContract.isAdmitted(installSeed, policyId, ratePPM)
    }
}

/** Bounded pre-fault breadcrumb ring buffer. */
class BreadcrumbRingBuffer(private val capacity: Int = 32) {
    data class Crumb(val timestampMs: Long, val category: String, val message: String)
    private val items = ArrayDeque<Crumb>(capacity.coerceAtLeast(1))

    @Synchronized
    fun append(timestampMs: Long, category: String, message: String) {
        val cap = capacity.coerceAtLeast(1)
        while (items.size >= cap) items.removeFirst()
        items.addLast(Crumb(timestampMs, category, message))
    }

    @Synchronized
    fun snapshot(): List<Crumb> = items.toList()
}
