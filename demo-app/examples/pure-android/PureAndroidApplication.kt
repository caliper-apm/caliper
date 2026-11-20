package com.caliper.example.pureandroid

import android.app.Application
import io.caliper.apm.CaliperConfig
import io.caliper.apm.PolicyLease
import io.caliper.apm.ResourceIdentity
import io.caliper.apm.android.CaliperAndroidAgent

/**
 * Minimal Pure-Android Integration Example (15 lines, ZERO React Native dependencies).
 */
class PureAndroidApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        CaliperAndroidAgent.install(
            application = this,
            config = CaliperConfig(
                // Works on both Emulator and USB devices via: adb reverse tcp:8080 tcp:8080
                endpointUrl = "http://127.0.0.1:8080",
                resource = ResourceIdentity(
                    appId = "dev.caliper.pureandroid",
                    appVersion = "1.0.0",
                    buildNumber = "100",
                    installHash = "1122334455667788",
                ),
            ),
            defaultLease = PolicyLease(
                policyId = "default_v1",
                ratePPM = 1_000_000L,
                expiresAtMs = System.currentTimeMillis() + 86_400_000L,
                maxEvents24h = 200,
            ),
        )
    }
}
