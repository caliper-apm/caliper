# Hybrid Demo App & Chaos Lab (`demo-app/`)

A non-trivial showcase and live integration testbed:
- `android-host/` & `ios-host/` — Native core shell (complex media feed, video/gallery, native HTTP stack) + minimal pure-Native samples
- `rn-modules/` — Embedded React Native screens (streaming reader, heavy detail view) demonstrating seamless Native+RN unified telemetry
- `chaos-lab/` — In-app 1-tap fault injection drawer (startup stall, scroll jank, JS LongTask storm, retention leak, crash/ANR, slow TLS/500)
- `mock-api/` — Local controllable fault backend (latency, streaming SSE, TLS/HTTP errors)
- `flows/` — Maestro automated replay flows
