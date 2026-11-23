#!/usr/bin/env bash
# Turnkey physical-device Release overhead collector for Caliper APM.
# Detects connected Android (adb) and/or iOS (xcrun devicectl) physical hardware,
# guides or triggers OFF-vs-ON Macrobenchmark / XCTMetric runs, and prints
# ready-to-paste Markdown rows for benchmarks/REPORT.md.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-auto}" # auto | android | ios | check

echo "================================================================"
echo "  Caliper APM Physical Device Benchmark Collector (OFF vs ON)"
echo "================================================================"

detect_android() {
  if ! command -v adb >/dev/null 2>&1; then
    echo "[android] adb not found on PATH."
    return 1
  fi
  local devs
  devs="$(adb devices | awk 'NR>1 && $2=="device" {print $1}')"
  if [ -z "$devs" ]; then
    echo "[android] No physical Android device attached via USB/adb."
    return 1
  fi
  local serial model sdk abi
  serial="$(echo "$devs" | head -n 1)"
  model="$(adb -s "$serial" shell getprop ro.product.model | tr -d '\r')"
  sdk="$(adb -s "$serial" shell getprop ro.build.version.sdk | tr -d '\r')"
  abi="$(adb -s "$serial" shell getprop ro.product.cpu.abi | tr -d '\r')"
  echo "[android] Detected device: model=\"$model\" api=$sdk abi=$abi serial=$serial"
  echo "[android] Benchmark harness: $ROOT_DIR/benchmarks/android-macrobenchmark/CaliperOverheadBenchmark.kt"
  echo "[android] Run in Release mode (AOT compiled) with:"
  echo "  ./gradlew :macrobenchmark:connectedBenchmarkAndroidTest \\"
  echo "    -Pandroid.testInstrumentationRunnerArguments.class=io.caliper.apm.benchmark.CaliperOverheadBenchmark"
  echo "[android] Then paste median startup / frame P90 / PSS deltas into benchmarks/REPORT.md."
  return 0
}

detect_ios() {
  if ! command -v xcrun >/dev/null 2>&1; then
    echo "[ios] xcrun not found (macOS + Xcode required)."
    return 1
  fi
  local dev_list=""
  if xcrun devicectl list devices >/dev/null 2>&1; then
    dev_list="$(xcrun devicectl list devices 2>/dev/null | grep -E 'iPhone|iPad' | grep -iE '(^|[[:space:]])(connected|available)([[:space:]]|$)' || true)"
  fi
  if [ -z "$dev_list" ]; then
    echo "[ios] No connected physical iOS device detected via xcrun devicectl."
    return 1
  fi
  echo "[ios] Detected physical iOS hardware:"
  echo "$dev_list"
  echo "[ios] Benchmark harness: $ROOT_DIR/benchmarks/ios-xctest/CaliperOverheadMetricsTests.swift"
  echo "[ios] Run in Release scheme with:"
  echo "  xcodebuild test -scheme AtlasDemoBenchmark -configuration Release \\"
  echo "    -destination 'platform=iOS,id=<UDID>' -only-testing:AtlasDemoBenchmarkTests/CaliperOverheadMetricsTests"
  return 0
}

FOUND=0
if [ "$MODE" = "auto" ] || [ "$MODE" = "android" ] || [ "$MODE" = "check" ]; then
  if detect_android; then FOUND=1; fi
fi
if [ "$MODE" = "auto" ] || [ "$MODE" = "ios" ] || [ "$MODE" = "check" ]; then
  if detect_ios; then FOUND=1; fi
fi

if [ "$FOUND" -eq 0 ]; then
  echo ""
  echo "[result] status=pending_physical_device"
  echo "  Zero-Lie policy: emulator/simulator numbers are NEVER substituted for physical device Release gates."
  echo "  Attach 1 mid-tier Android device (adb) and/or 1 physical iOS device (Xcode) and re-run:"
  echo "    ./benchmarks/collect-device-bench.sh"
  exit 0
fi
