#!/usr/bin/env bash
set -euo pipefail

# Reproducible Host Overhead Benchmark Suite (Go Server + RN Adapter + Swift SDK).
# Note: Host micro-benchmarks validate algorithmic efficiency and allocation bounds;
# North Star physical-device Release gates require dedicated mid-tier Android/iOS runs.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "=== 1. Go Server Ingestion Benchmark ==="
(cd "$ROOT_DIR/server" && go test -bench=BenchmarkEmbeddedIngest100Batch -benchmem -run=^$ ./internal/storage)

echo ""
echo "=== 2. React Native Adapter OFF vs ON (100k API + 10k LongTask) ==="
node "$ROOT_DIR/benchmarks/rn-adapter-bench.ts"

echo ""
echo "=== 3. Swift Native SDK Release Test & Footprint Check ==="
(cd "$ROOT_DIR/packages/sdk-ios" && swift test -c release 2>&1 | tail -n 12)
