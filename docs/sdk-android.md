# Android Native SDK Integration Guide (`packages/sdk-android`)

`caliper-core` and `caliper-android` are **100% Pure Kotlin / Android SDK** with **zero React Native or heavy third-party dependencies**.

---

## 1. Quickstart (`Application.onCreate`)

```kotlin
import android.app.Application
import android.os.Build
import io.caliper.apm.CaliperConfig
import io.caliper.apm.PolicyLease
import io.caliper.apm.ResourceIdentity
import io.caliper.apm.android.CaliperAndroidAgent

class MyApp : Application() {
    override fun onCreate() {
        super.onCreate()
        val config = CaliperConfig(
            endpointUrl = "https://apm.example.com",
            apiKey = BuildConfig.CALIPER_INGEST_KEY,
            resource = ResourceIdentity(
                appId = packageName,
                platform = "android",
                env = if (BuildConfig.DEBUG) "dev" else "prod",
                appVersion = BuildConfig.VERSION_NAME,
                buildNumber = BuildConfig.VERSION_CODE.toString(),
                gitSha = BuildConfig.GIT_SHA,
                adapter = "native",
                sdkName = "caliper-android",
                sdkVersion = "1.4.1",
                installHash = InstallationHash.stableHex16(this), // Non-PII 16-64 hex
                osVersion = "Android ${Build.VERSION.RELEASE}",
                deviceModel = Build.MODEL,
            ),
        )
        // Optional bootstrap lease until first GET /v1/policy completes (fail-closed if omitted)
        CaliperAndroidAgent.install(this, config)
    }
}
```

Mark the first meaningful content frame in your launch Activity:
```kotlin
window.decorView.post {
    CaliperAndroidAgent.markFirstContentFrame()
}
```

---

## 2. What `CaliperAndroidAgent` Instruments Automatically

| Domain | Mechanism | Zero-Lie Guarantee |
|---|---|---|
| **Cold Start** | `Process.getStartElapsedRealtime()` -> `Application.onCreate` -> `markFirstContentFrame()` | Single monotonic anchor; `native_init + runtime + first_frame <= total` |
| **Fluency** | `Window.OnFrameMetricsAvailableListener` on background `HandlerThread` | API 31+ `FrameMetrics.DEADLINE` strictly separated from API 26–30 `heuristic`; zero-frame windows isolated |
| **Crashes & Abnormal Exits** | Chained `UncaughtExceptionHandler` + **NDK C++ Signal Handler** (`libcaliper_native.so`: `sigaltstack`, `SIGSEGV`/`SIGABRT`/`SIGBUS`/`SIGFPE`/`SIGILL`, `_Unwind_Backtrace` + `/proc/self/maps` relative offsets) + API 30+ `ApplicationExitInfo` | Never swallows existing handlers; zero-malloc POSIX tombstone feeds directly into server `native_elf` DWARF symbolicator |
| **ANR / Watchdog / Looper** | **NDK `SIGQUIT` ANR Interceptor** (forwards to ART Signal Catcher) + **Main-Thread `Looper` Message-Dispatch Latency & History Tracer** (`CaliperLooperMonitor`) + `CaliperWatchdog` (1000 ms poll) | Captures blocking `Handler`/`Runnable` + 20-message `MessageQueue` history without reflection or blocking UI |
| **Memory & CPU** | `Debug.MemoryInfo.totalPss` (`android_pss_kib`) + process CPU sampling | Explicit `memory_kind` tag prevents cross-platform confusion |
| **Offline Durability** | Bounded FIFO `CaliperDiskQueue` + exponential backoff circuit breaker | Drops oldest when byte cap exceeded; never OOMs or loops forever |
| **Remote Logs** | `CaliperDiagnosticRingLog` (automatic email/phone/token PII scrubbing) + `CaliperRemoteLogAgent` | Pulls bounded logs only when authorized task exists |

---

## 3. Custom Multi-Stage Business Journeys

Instrument critical user flows (e.g., feed load, checkout, article open) with ordered stages:

```kotlin
CaliperAndroidAgent.client().recordCustomJourney(
    journeyName = "article_open_to_readable",
    status = "success",
    durationMs = 312.0,
    stages = listOf(
        "cache_lookup" to 18.0,
        "api_fetch" to 184.0,
        "layout_render" to 110.0,
    ),
    scene = "streaming_reader",
)
```

---

## 4. Zero-Boilerplate ASM Auto-Instrumentation & R8/NDK Symbol Upload

Apply both Gradle plugins from [`packages/gradle-plugin`](../packages/gradle-plugin) in your `app/build.gradle.kts`:

```kotlin
plugins {
    id("io.caliper.apm.symbols")
    id("io.caliper.apm.autoinstrument")
}

caliperAutoInstrument {
    enabled.set(true)
    instrumentOkHttp.set(true)       // Auto-hooks OkHttpClient.Builder.build() across all modules
    instrumentCaliperTrace.set(true) // Auto-wraps @CaliperTrace methods (normal + exception exits)
    includePackages.set(listOf("com.example.app"))
}

caliperSymbols {
    serverUrl.set("https://apm.example.com")
    apiKey.set(providers.environmentVariable("CALIPER_API_KEY"))
    appId.set("com.example.app")
    env.set("prod")
}
```

Annotate critical business methods with `@CaliperTrace("checkout_flow", scene = "checkout")` for zero-boilerplate `custom_scene` telemetry, and attach `CaliperWebViewBridge.attach(webView, client)` + `CaliperThermalMonitor.captureAndRecord(ctx, client)` as needed (`CaliperDiskMonitor.scheduleDailyAuditAsync` is already wired inside `CaliperAndroidAgent.install`).

For apps shipping C/C++ JNI libraries, upload unstripped NDK ELF `.so` binaries directly (`--kind native_elf`) — Caliper extracts `.note.gnu.build-id`, parses DWARF line/function/inline tables + ELF symtabs in pure Go, and demangles Itanium C++ frames automatically:

```bash
caliper-symbols upload \
  --server https://apm.example.com --api-key "$CALIPER_API_KEY" \
  --app-id com.example.app --platform android --version 1.6.0 --build 160 \
  --kind native_elf --file app/build/intermediates/merged_native_libs/release/out/lib/arm64-v8a/libengine.so
```

---

## 5. Verified Physical-Device Release Overhead

Measured on **`vivo V2343A` (Snapdragon 6 Gen 1, Android 14 / API 34, Release AOT `-m speed`)** — full report in [`benchmarks/REPORT.md`](../benchmarks/REPORT.md):
- Cold Start Overhead (`n=20`): **`+6.0 ms` P50 / `-1 ms` P95** (gate ≤ 15 ms)
- 15-Fling Scroll Fluency (`gfxinfo`): **`+0.00%` jank** (0 / 518 jank frames)
- Resident Memory (`TOTAL PSS`): **`+2.67 MiB`** (gate ≤ 4 MiB)
- Release APK Binary Size (pre-R8): **`+28.0 KiB`** (gate < 300 KB)
- Self-Induced Crashes: **`0 / 26`**
