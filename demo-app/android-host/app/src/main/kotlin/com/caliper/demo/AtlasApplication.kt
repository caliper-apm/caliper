package com.caliper.demo

import android.app.Application
import android.os.Build
import io.caliper.apm.CaliperConfig
import io.caliper.apm.PolicyLease
import io.caliper.apm.ResourceIdentity
import io.caliper.apm.android.CaliperAndroidAgent

/**
 * Atlas Reader Hybrid Demo Android Application entry point.
 * Initializes Pure-Native CaliperAndroidAgent on process start; optional RN screens attach via bridge.
 */
class AtlasApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        CaliperAndroidAgent.install(
            application = this,
            config = CaliperConfig(
                // Works on both Android Emulator and USB physical devices via: adb reverse tcp:8080 tcp:8080
                endpointUrl = "http://127.0.0.1:8080",
                resource = ResourceIdentity(
                    appId = "dev.caliper.demo",
                    platform = "android",
                    env = "prod",
                    appVersion = "1.1.0",
                    buildNumber = "110",
                    installHash = "0123456789abcdef",
                    userHash = "0000111122223333",
                    osVersion = "Android ${Build.VERSION.RELEASE}",
                    deviceModel = Build.MODEL,
                ),
            ),
            defaultLease = PolicyLease(
                policyId = "demo_chaos_v1",
                ratePPM = 1_000_000L,
                expiresAtMs = System.currentTimeMillis() + 86_400_000L,
                maxEvents24h = 500,
            ),
        )
        CaliperAndroidAgent.pollRemoteLogTasksAsync()
    }
}
