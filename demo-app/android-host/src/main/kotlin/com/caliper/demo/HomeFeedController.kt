package com.caliper.demo

import io.caliper.apm.CaliperClient
import io.caliper.apm.android.AndroidFluencySession

/**
 * Pure Native Android Feed Controller for Atlas Reader Demo App.
 * Demonstrates zero-RN-dependency integration of Caliper Native SDK + Chaos Lab triggers.
 */
class HomeFeedController(
    private val client: CaliperClient,
    private val sdkInt: Int = 34
) {
    private val retainedLeakBuffers = mutableListOf<ByteArray>()
    private var fluencySession = AndroidFluencySession(client, sdkInt = sdkInt, scene = "home_feed")

    fun onEnterFeed() {
        client.setScene("home_feed")
        client.addBreadcrumb("ui", "entered native HomeFeed")
        fluencySession = AndroidFluencySession(client, sdkInt = sdkInt, scene = "home_feed")
    }

    /** Chaos Lab #2: Simulates synchronous bitmap decode on main thread during scroll. */
    fun injectScrollJankStall(stallMs: Long = 120L) {
        client.addBreadcrumb("chaos", "injectScrollJankStall(${stallMs}ms)")
        val targetMs = 16.67
        // Record realistic janky frames + capture obfuscated main-thread stack sample.
        repeat(60) { idx ->
            val dur = if (idx % 5 == 0) stallMs.toDouble() else 14.0
            fluencySession.onFrame(durationMs = dur, targetMs = targetMs)
        }
        fluencySession.emitWindow(
            stallStack = listOf(
                "a.b.c.a(SourceFile:12)",
                "a.b.d.a(SourceFile:32)"
            ),
            bypassSampling = true
        )
    }

    /** Chaos Lab #4: Retains 4 MiB native byte buffers across feed visits. */
    fun injectMemoryRetentionLeak(megabytes: Int = 4) {
        client.addBreadcrumb("chaos", "injectMemoryRetentionLeak(${megabytes}MiB)")
        retainedLeakBuffers.add(ByteArray(megabytes.coerceIn(1, 32) * 1024 * 1024))
    }

    fun clearRetainedLeakBuffers() {
        retainedLeakBuffers.clear()
        client.addBreadcrumb("chaos", "cleared retained buffers")
    }

    /** Chaos Lab #5: Emits an obfuscated Android crash payload resolvable via uploaded R8 mapping. */
    fun emitSimulatedObfuscatedCrash() {
        val payload = """{
          "fingerprint": "fp-android-r8-decode-oom",
          "title": "OutOfMemoryError in a.b.c.a",
          "fatal": true,
          "exception_type": "java.lang.OutOfMemoryError",
          "thread_name": "main",
          "frames": [
            {"module": "AtlasDemo", "symbol": "a.b.c.a", "line": 12, "in_app": true},
            {"module": "AtlasDemo", "symbol": "a.b.d.a", "line": 32, "in_app": true}
          ]
        }"""
        client.enqueue(kind = "crash", payloadJson = payload, scene = "home_feed", bypassSampling = true)
    }
}
