# Android AGP 8.7 + OkHttp 4.12 + R8 Full Minification Verification Sample

This standalone multi-module Android project verifies the entire Caliper Android production pipeline end-to-end:

- **AGP `8.7.3`** (`com.android.application` + `com.android.library`) & **Kotlin `2.1.20`**
- **Caliper ASM9 Auto-Instrumentation Plugin** (`io.caliper.apm.autoinstrument`) composite-built from [`../../packages/gradle-plugin`](../../packages/gradle-plugin)
- **Real OkHttp `4.12.0`** (`OkHttpClient.Builder.build()` woven automatically to attach `CaliperOkHttpInterceptor` W3C `traceparent` + `CaliperOkHttpEventListener` while preserving host `EventListener.Factory`)
- **Real R8 Full Minification** (`isMinifyEnabled = true`) using `caliper-android/consumer-rules.pro`
- **Real NDK / CMake C++ Build** compiling `libcaliper_native.so` (`arm64-v8a`)

## Run Locally

```bash
export ANDROID_HOME="$HOME/Library/Android/sdk" # or path to your Android SDK
gradle :app:assembleRelease :app:testReleaseUnitTest
```

### Verify Woven Bytecode Survived R8 Minification

```bash
unzip -p app/build/outputs/apk/release/app-release.apk classes.dex > /tmp/sample-classes.dex
"$ANDROID_HOME/build-tools/35.0.0/dexdump" -d /tmp/sample-classes.dex | \
  grep -E "onOkHttpBuilder|beginTrace|endTrace|eventListenerFactory"
rm -f /tmp/sample-classes.dex
```
