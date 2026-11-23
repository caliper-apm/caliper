package io.caliper.apm.android

import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.BatteryManager
import android.os.Build
import android.os.PowerManager
import android.webkit.JavascriptInterface
import android.webkit.WebView
import io.caliper.apm.CaliperClient
import io.caliper.apm.CaliperDiskAuditor
import io.caliper.apm.CaliperWebViewProbe
import io.caliper.apm.ThermalBatteryEvaluator
import io.caliper.apm.ThermalBatterySnapshot
import java.io.File

/**
 * Android Battery & Thermal State monitor using public PowerManager (API 29+) and BatteryManager APIs.
 */
object CaliperThermalMonitor {
    @JvmStatic
    fun captureSnapshot(context: Context, scene: String = "unknown"): ThermalBatterySnapshot {
        val thermalLevel = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val pm = context.getSystemService(Context.POWER_SERVICE) as? PowerManager
            when (pm?.currentThermalStatus) {
                PowerManager.THERMAL_STATUS_NONE -> "nominal"
                PowerManager.THERMAL_STATUS_LIGHT -> "light"
                PowerManager.THERMAL_STATUS_MODERATE -> "moderate"
                PowerManager.THERMAL_STATUS_SEVERE -> "severe"
                PowerManager.THERMAL_STATUS_CRITICAL -> "critical"
                PowerManager.THERMAL_STATUS_EMERGENCY,
                PowerManager.THERMAL_STATUS_SHUTDOWN -> "emergency"
                else -> "nominal"
            }
        } else {
            "nominal"
        }

        val batteryIntent = runCatching {
            context.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        }.getOrNull()
        val level = batteryIntent?.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = batteryIntent?.getIntExtra(BatteryManager.EXTRA_SCALE, 100) ?: 100
        val pct = if (level >= 0 && scale > 0) (level * 100) / scale else -1
        val tempTenths = batteryIntent?.getIntExtra(BatteryManager.EXTRA_TEMPERATURE, 0) ?: 0
        val tempC = tempTenths / 10.0
        val plugged = batteryIntent?.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) ?: 0

        return ThermalBatterySnapshot(
            thermalLevel = thermalLevel,
            batteryLevelPct = pct,
            batteryTempCelsius = tempC,
            isCharging = plugged != 0,
            scene = scene,
        )
    }

    @JvmStatic
    fun captureAndRecord(context: Context, client: CaliperClient, scene: String = "unknown") {
        val snap = captureSnapshot(context, scene)
        ThermalBatteryEvaluator.record(client, snap)
    }
}

/**
 * Low-frequency background Android sandbox disk auditor (runs at most once per 24h on a daemon thread).
 */
object CaliperDiskMonitor {
    private const val PREF_NAME = "caliper_disk_audit"
    private const val KEY_LAST_MS = "last_audit_ms"
    private const val ONE_DAY_MS = 24L * 60L * 60L * 1000L

    @JvmStatic
    fun scheduleDailyAuditAsync(
        context: Context,
        client: CaliperClient,
        quotaBudgetMiB: Double = 500.0,
        delayMs: Long = 15_000L,
    ) {
        val appCtx = context.applicationContext
        val prefs = appCtx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
        val now = System.currentTimeMillis()
        if (now - prefs.getLong(KEY_LAST_MS, 0L) < ONE_DAY_MS) {
            return
        }
        Thread({
            runCatching {
                if (delayMs > 0) Thread.sleep(delayMs)
                val roots = linkedMapOf(
                    "cache" to appCtx.cacheDir,
                    "files" to appCtx.filesDir,
                    "databases" to File(appCtx.applicationInfo.dataDir, "databases"),
                )
                val report = CaliperDiskAuditor.auditDirectories(roots)
                CaliperDiskAuditor.recordAudit(client, report, quotaBudgetMiB)
                prefs.edit().putLong(KEY_LAST_MS, System.currentTimeMillis()).apply()
            }
        }, "caliper-disk-audit").apply { isDaemon = true; start() }
    }
}

/**
 * Zero-PII Android WebView performance, JS error, and blank-screen bridge.
 *
 * Usage:
 * ```kotlin
 * CaliperWebViewBridge.attach(webView, CaliperAndroidAgent.client())
 * // In your WebViewClient.onPageFinished:
 * CaliperWebViewBridge.injectProbe(webView)
 * ```
 */
class CaliperWebViewBridge(private val client: CaliperClient) {
    @JavascriptInterface
    fun postMessage(jsonPayload: String) {
        runCatching { CaliperWebViewProbe.ingestMessageJson(jsonPayload, client) }
    }

    companion object {
        @JvmStatic
        fun attach(webView: WebView, client: CaliperClient): CaliperWebViewBridge {
            val bridge = CaliperWebViewBridge(client)
            webView.addJavascriptInterface(bridge, "CaliperWV")
            return bridge
        }

        @JvmStatic
        fun injectProbe(webView: WebView) {
            webView.evaluateJavascript(CaliperWebViewProbe.INJECTED_JS, null)
        }
    }
}
