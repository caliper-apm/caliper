# Reproducible Device Overhead Benchmarks (`benchmarks/`)

Houses automated Release-build OFF-vs-ON comparison harnesses measuring:
- Cold start P95 delta (Target: ≤ 15 ms or ≤ 1.5%)
- Resident memory delta (Android PSS / iOS `phys_footprint`, Target: ≤ 4 MiB)
- Active CPU delta (Target: ≤ 1% of one core)
- Binary size delta (Target: Android < 300 KB, iOS < 500 KB)
