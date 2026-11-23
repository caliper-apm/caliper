package com.caliper.sample

import android.app.Application
import io.caliper.apm.CaliperConfig
import io.caliper.apm.ResourceIdentity
import io.caliper.apm.android.CaliperAndroidAgent

class SampleApp : Application() {
    override fun onCreate() {
        super.onCreate()
        CaliperAndroidAgent.install(
            application = this,
            config = CaliperConfig(
                endpointUrl = "https://apm.example.internal/v1/ingest",
                resource = ResourceIdentity(
                    appId = "com.caliper.sample",
                    platform = "android",
                    env = "prod",
                    appVersion = "1.9.0",
                    buildNumber = "190",
                    installHash = "0123456789abcdef0123456789abcdef",
                ),
                tracePropagationTargets = listOf(".example.internal", "127.0.0.1"),
            ),
        )
    }
}
