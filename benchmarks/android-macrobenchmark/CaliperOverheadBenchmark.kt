package com.caliper.benchmark

/**
 * Reproducible Android Physical-Device Release Overhead Benchmark Harness (Jetpack Macrobenchmark).
 *
 * Run OFF vs ON on a mid-tier physical Android Release APK (e.g., Redmi Note 13 / Pixel 6a):
 *   1. Build baseline APK without CaliperAndroidAgent.install() -> measure cold start P95 + frame timing + PSS.
 *   2. Build instrumented APK with CaliperAndroidAgent.install() -> measure identical 25 iterations.
 *   3. Verify North Star Outcome #1 exit criteria:
 *      - Cold start P95 delta <= 15 ms (or <= 1.5%)
 *      - Resident PSS delta   <= 4 MiB
 *      - Active CPU delta     <= 1% of 1 core
 *      - Release APK delta    < 300 KB
 */
object CaliperAndroidOverheadSpec {
    const val ITERATIONS = 25
    const val COLD_START_P95_BUDGET_MS = 15.0
    const val MEMORY_PSS_BUDGET_MIB = 4.0
    const val APK_SIZE_BUDGET_KB = 300
}
