package com.caliper.sample

import android.app.Activity
import android.os.Bundle
import android.widget.TextView
import io.caliper.apm.CaliperTrace
import io.caliper.apm.android.CaliperAndroidAgent
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit

class MainActivity : Activity() {
    private val httpClient: OkHttpClient by lazy {
        // Woven automatically by `io.caliper.apm.autoinstrument` ASM9 plugin ->
        // injects CaliperAutoInstrumentHooks.onOkHttpBuilder(builder) before .build()!
        OkHttpClient.Builder()
            .connectTimeout(5, TimeUnit.SECONDS)
            .readTimeout(5, TimeUnit.SECONDS)
            .build()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val status = loadCheckoutSummary("order-42")
        val tv = TextView(this).apply { text = "Caliper AGP 8.7 + OkHttp 4 + R8: $status" }
        setContentView(tv)
        CaliperAndroidAgent.markFirstContentFrame()
    }

    @CaliperTrace(name = "load_checkout_summary", scene = "MainActivity")
    fun loadCheckoutSummary(orderId: String): String {
        val req = Request.Builder()
            .url("https://api.example.internal/v1/orders/$orderId")
            .get()
            .build()
        return "${req.method} ${req.url.encodedPath} (${httpClient.interceptors.size} interceptors)"
    }
}
