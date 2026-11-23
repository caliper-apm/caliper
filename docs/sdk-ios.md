# iOS Native SDK Integration Guide (`packages/sdk-ios`)

`CaliperSDK` is a **pure Swift Package Manager (Swift 6 Sendable-clean)** native library with **zero React Native or third-party dependencies**.

---

## 1. Installation (Swift Package Manager)

Add `packages/sdk-ios` to your `Package.swift` or Xcode project:

```swift
dependencies: [
    .package(url: "https://github.com/<your-org>/caliper-apm.git", from: "1.4.1")
]
```

---

## 2. Quickstart (`AppDelegate` / `@main App`)

```swift
import CaliperSDK
import UIKit

final class AppBootstrap {
    let client: CaliperClient
    let watchdog: CaliperMainThreadWatchdog
    let metricKit: CaliperMetricKitBridge?

    init() {
        let config = CaliperConfig(
            endpointURL: URL(string: "https://apm.example.com")!,
            apiKey: ProcessInfo.processInfo.environment["CALIPER_INGEST_KEY"],
            resource: ResourceIdentity(
                appID: Bundle.main.bundleIdentifier ?? "com.example.app",
                appVersion: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "1.0.0",
                buildNumber: Bundle.main.infoDictionary?["CFBundleVersion"] as? String ?? "1",
                adapter: "native",
                sdkName: "caliper-ios",
                sdkVersion: "1.4.1",
                installHash: InstallationHash.stableHex16(), // Non-PII 16-64 hex
                osVersion: "iOS \(UIDevice.current.systemVersion)",
                deviceModel: DeviceHardware.modelName()
            )
        )
        self.client = CaliperClient(config: config)
        self.watchdog = CaliperMainThreadWatchdog(thresholdMS: 3000, pollIntervalMS: 1000) { durMS, stack in
            // Enqueue main-thread hang / Watchdog diagnostic automatically
            print("Main thread hang: \(durMS)ms (\(stack.count) frames)")
        }
        self.watchdog.start()

        #if canImport(MetricKit) && os(iOS)
        if #available(iOS 14.0, *) {
            self.metricKit = CaliperMetricKitBridge(client: client)
        } else {
            self.metricKit = nil
        }
        #else
        self.metricKit = nil
        #endif
    }
}
```

---

## 3. Custom Multi-Stage Business Journeys

```swift
client.recordCustomJourney(
    journeyName: "article_open_to_readable",
    status: "success",
    durationMS: 185.0,
    stages: [
        SceneStage(name: "cache_lookup", durationMS: 14.0),
        SceneStage(name: "api_fetch", durationMS: 96.0),
        SceneStage(name: "promotion_draw", durationMS: 75.0),
    ],
    scene: "streaming_reader"
)
```

---

## 4. Native `.dSYM` / `.dSYM.zip` DWARF Upload (CLI & Fastlane)

Caliper parses **native Mach-O DWARF binaries** (Thin, Universal/Fat arm64, and `.dSYM.zip`) in pure Go with zero external `llvm-symbolizer` dependency — extracting Mach-O `LC_UUID`, resolving both absolute virtual addresses and `__TEXT`-relative offsets, demangling Itanium C++ symbols, and expanding `DW_TAG_inlined_subroutine` inline chains.

Upload a raw `.dSYM` bundle directory directly via CLI (`caliper-symbols` auto-zips directories in memory):
```bash
caliper-symbols upload \
  --server https://apm.example.com --api-key "$CALIPER_API_KEY" \
  --app-id com.example.app --platform ios --version 1.6.0 --build 160 \
  --kind dsym --file build/MyApp.app.dSYM
```

Or use [`packages/fastlane-plugin`](../packages/fastlane-plugin) in your `Fastfile`:

```ruby
lane :release do
  build_app(scheme: "MyApp")
  caliper_upload_symbols(
    server_url: "https://apm.example.com",
    api_key: ENV["CALIPER_API_KEY"],
    app_id: "com.example.app",
    app_version: get_version_number,
    build_number: get_build_number,
    kind: "dsym",
    symbol_file: lane_context[SharedValues::DSYM_OUTPUT_PATH]
  )
end
```

---

## 5. Verified Physical-Device Release Overhead

Measured on **`iPhone 13 Pro Max` (A15 Bionic, 120 Hz ProMotion, iOS `27.2.1`, `-O wholemodule`)** — full report in [`benchmarks/REPORT.md`](../benchmarks/REPORT.md):
- Cold Start Overhead (`n=18`): **`+3.98 ms` P50 / `+1.51 ms` P90** (gate ≤ 15 ms)
- ProMotion Scroll Fluency (`CADisplayLink`): **`+0.00%` aggregate jank** (3/517 vs 3/516 frames)
- Resident Memory (`phys_footprint`): **`+0.34 MiB`** (gate ≤ 4 MiB — **11.8× under**)
- Active Scroll CPU (`getrusage`): **`+0.10%` of 1 core** (gate ≤ 1% — **10× under**)
- Stripped arm64 Mach-O Binary Delta: **`+247.5 KiB`** (gate < 500 KB)
- Self-Induced Crashes: **`0 / 20`**
