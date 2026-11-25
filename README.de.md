<div align="center">

# Caliper APM

**Native-First, React-Native-kompatibles, evidenzbasiertes (Zero-Lie) Open-Source Mobile APM**

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/caliper-apm/caliper?color=165dff)](https://github.com/caliper-apm/caliper/releases)
[![CI](https://github.com/caliper-apm/caliper/actions/workflows/ci.yml/badge.svg)](https://github.com/caliper-apm/caliper/actions/workflows/ci.yml)
[![Docker E2E](https://github.com/caliper-apm/caliper/actions/workflows/docker-e2e.yml/badge.svg)](https://github.com/caliper-apm/caliper/actions/workflows/docker-e2e.yml)
[![Go: Zero-CGO](https://img.shields.io/badge/Go-1.23%2B%20Zero--CGO-00ADD8.svg)](server/go.mod)

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · **Deutsch**

[Architektur](docs/architecture.md) · [Self-Hosting](docs/self-hosting.md) · [Android SDK](docs/sdk-android.md) · [iOS SDK](docs/sdk-ios.md) · [RN-Adapter](docs/sdk-react-native.md) · [Benchmarks](benchmarks/REPORT.md) · [Changelog](CHANGELOG.md)

<br />

<img src="docs/assets/console-overview-dark.png" alt="Caliper APM Console 2.4 — App-Übersicht (Dark Mode)" width="100%" />

*Console 2.4 mit Echtdaten auf einer einzigen Zero-CGO-Go-Binary (~15 MiB im Leerlauf / ~69 MiB bei 30.000 Events): DDSketch (±2%) P50/P90/P95/P99-Trends, Release-Rollout-Marker, Native vs. React Native Vergleich, Dark/Light-Theme und 6-sprachige Oberfläche.*

</div>

---

## Console 2.4 Vorschau (100 % echte Telemetriedaten)

| **3-Phasen-Kaltstart & 19-Bucket-Latenzverteilung** | **Netzwerk & Ressourcen (5 Fehlerdomänen + Latenz)** |
| :---: | :---: |
| <img src="docs/assets/console-fluency-dark.png" alt="3-Phasen-Kaltstart und 19-Bucket-Histogramm" width="100%" /> | <img src="docs/assets/console-network-dark.png" alt="API-Erfolgsrate, 5 Fehlerdomänen und Latenzverteilung" width="100%" /> |
| **Tiefendiagnose (CPU-Flamegraph & Heap-Leckvergleich)** | **Abstürze, ANR & Auto-Symbolisierung (Light Mode)** |
| <img src="docs/assets/console-diagnostics-dark.png" alt="CPU-Flamegraph und Heap-Dominator-Vergleich zwischen Releases" width="100%" /> | <img src="docs/assets/console-stability-light.png" alt="Täglicher Crash-/ANR-Trend und Issue-Explorer im Light Mode" width="100%" /> |

---

## Warum Caliper APM?

Viele mittlere und große mobile Apps setzen **nativen Code für leistungskritische Kernpfade (Feeds, Startzeit, Netzwerk, Medien)** und **React Native für schnell iterierende Produkt-Screens** ein. Caliper APM schließt diese Lücke mit fünf Kernprinzipien:

1. **Native-First-Kern + Erstklassiger RN-Adapter**: `sdk-android` (Kotlin + NDK) und `sdk-ios` (Swift SPM + C/Mach) haben **null React-Native-Abhängigkeiten**. Hybride Apps binden `sdk-react-native` nahtlos in denselben nativen Lebenszyklus, W3C-`traceparent` und dieselbe Symbolisierung ein.
2. **Zero-Lie-Statistik-Engine**: Abhängigkeitsfreies Pure-Go **DDSketch ($\alpha = 2\%$)** für zusammenführbare `P50 / P90 / P95 / P99`-Quantile, **`DDSketchKSTest`**, fenstergeclusterte Z-Tests für Fluency sowie **Benjamini-Hochberg-FDR**-Korrektur. Keine Null-Auffüllung fehlender Tage, strikte Zero-Frame-Isolation und explizite Stichprobengrenzen (`n >= 100`).
3. **Integrierte Zero-CGO-Auto-Symbolisierung**: Löst **R8/ProGuard**, **Source Map v3 (JS/Hermes)**, **Mach-O `.dSYM` / ELF `.so` DWARF (inkl. Inline-Expansion, Swift 5+ & Itanium-C++-Demangler)** und **Breakpad `.sym`** direkt im Go-Prozess auf — ohne externe Symbolicator-Dienste.
4. **CI/CD-Release-Gate & AI-Nativer MCP-Server**: `caliper-gate` CLI / GitHub Action blockiert statistisch signifikante Regressionen automatisch (`PASS` / `BLOCK` / `GATED` + artefaktreiner Binary-Size-Vergleich), und der Pure-Go-MCP-Server (`POST /v1/mcp` + `caliper-mcp`) verbindet Cursor, Claude Code, Codex und Antigravity mit RBAC-Mandantentrennung und **ohne serverseitige LLM-Keys**.
5. **Single-Binary in 60s → Horizontaler Cluster**: Lokal in Sekunden mit einer Go-Binary (Embedded WAL mit automatischer Kompaktierung) starten und in Produktion ohne SDK-Änderung auf **ClickHouse + PostgreSQL HA + S3/MinIO + Kubernetes Helm** skalieren.

---

## Systemarchitektur

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture-dark.png" />
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/architecture-light.png" />
    <img src="docs/assets/architecture-light.png" alt="Caliper APM — End-to-End Systemarchitektur" width="100%" />
  </picture>
</p>

- **Datenebene vs. Kontrollschleife**: Mobile SDKs senden gzip-komprimierte Telemetrie (`POST /v1/ingest`, W3C `traceparent`) und beziehen im Rückkanal deterministische FNV-1a-Sampling-Leases (`GET /v1/policy`) sowie auditierte Remote-Log-Aufträge; die CI-Pipeline lädt Symbole und `binary_size_bytes` hoch.
- **Zero-CGO Pure-Go-Kernplattform**: `server/cmd/caliper` (`CGO_ENABLED=0`) bündelt Fail-Closed-Mandanten-RBAC, Zero-Lie-Statistik (`DDSketch ±2%`, geclusterter Z-Test, KS, FDR) sowie In-Process-Symbolisierung für alle 5 Formate (R8, SourceMap v3, Mach-O `.dSYM`/ELF `.so` DWARF, Swift 5+ / C++, Breakpad) ohne externe Sidecars.
- **Austauschbare Dual-Mode-Persistenz**: Starte in < 60 s mit Embedded WAL (~50–100 MiB RAM) oder skaliere ohne SDK-Änderung horizontal auf **ClickHouse + PostgreSQL + S3/MinIO** — als einheitliche Basis für Console 2.4, `caliper-gate`, MCP (`2025-06-18`), Webhooks und OTLP/HTTP-Push.

---

## Schnellstart (< 1 Minute)

### Docker Compose (Empfohlen)
```bash
git clone https://github.com/caliper-apm/caliper.git
cd caliper
docker compose up --build
# Öffne http://localhost:8080 — vorgeladen mit v1.0.0 -> v1.1.0 (Regression) -> v1.2.0 (Behoben)
```

### Lokaler Start ohne Docker (Go 1.23+ & Node 22+)
```bash
./scripts/dev-quickstart.sh
```

### KI-Coding-Agent-Anbindung (Cursor / Claude Code / Codex / Antigravity)
```json
{
  "mcpServers": {
    "caliper-apm": {
      "command": "caliper-mcp",
      "args": ["--server", "http://127.0.0.1:8080"],
      "env": { "CALIPER_API_KEY": "${CALIPER_VIEWER_KEY}" }
    }
  }
}
```

---

## Gemessene Hardware-Benchmarks (Release OFF vs. ON)

Vollständige Methodik und Rohdaten in [`benchmarks/REPORT.md`](benchmarks/REPORT.md).

| Plattform / Komponente | Testszenario (Release OFF vs. ON) | Gemessenes Ergebnis |
|---|---|---|
| **Android-Gerät (`vivo V2343A`, API 34, AOT)** | 20 Kaltstarts + 15 Scrolls + PSS + APK | Kaltstart **`+6.0 ms` P50** · Jank **`+0.00%`** · PSS **`+2.67 MiB`** · APK **`+28.0 KiB`** · Crashes **`0`** |
| **iOS-Gerät (`iPhone 13 Pro Max`, `-O`)** | 18 Kaltstarts + ProMotion + `phys_footprint` | Kaltstart **`+3.98 ms` P50** · Jank **`+0.00%`** · Speicher **`+0.34 MiB`** · CPU **`+0.10%`** · Binary **`+247.5 KiB`** |
| **Go-Server (Single-Binary + WAL)** | 30.002 Events Gzip-Replay + Vergleich | **40.489 Events/s** · Vergleich **1,55–2,47 ms** · RSS **15,2 → 69,0 MiB** |
| **RN-Adapter (`sdk-react-native`)** | 100k API-Aufrufe + 10k LongTasks | **42 ns / API** · **550× weniger Bridge-Traffic** |

---

## Lizenz

Veröffentlicht unter der [Apache License 2.0](LICENSE).
