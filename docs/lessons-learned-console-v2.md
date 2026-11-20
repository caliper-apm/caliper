# Postmortem & Engineering Lessons — Console v1 → Console 2.0

**Date**: 2026-11-20  
**Trigger**: Live review of `http://127.0.0.1:8080/` exposed a major product gap between the v0.1–v1.0 Web Console and industry benchmarks (Volcano Engine APM Plus / enterprise mobile APM consoles).

---

## 1. What Went Wrong (Honest Root-Cause Analysis)

1. **Over-indexing on Backend Correctness while Under-investing in Visual UX**
   - 85%+ of implementation effort went into Go ingestion, Zero-Lie statistics, ClickHouse, R8/SourceMap/dSYM symbolication, Gradle/Fastlane plugins, and OTLP.
   - When implementing `console/` in Week 4, the implementation took a "minimal zero-dependency, 143-line CSS, table-only" shortcut to hit build/API checks quickly.
2. **Missing Visualization-Grade Backend Endpoints**
   - The backend stored `day_utc` rollups and 19-bucket histograms internally, but only returned single-point summary numbers in JSON responses.
   - Without `GET /v1/analytics/timeseries`, raw 19-bucket distribution arrays, and `GET /v1/analytics/breakdown` (by version / adapter / scene), the frontend could not render trend lines, sparklines, or latency distribution histograms.
3. **Weak Information Architecture & Filtering**
   - Flat 6-button sidebar instead of grouped APM domain navigation.
   - Only Platform + Version selectors instead of a persistent global multi-dimension filter bar (Time Window, Env, Version, Native vs React Native adapter slice, Scene).
   - English-only labels without Chinese/English bilingual APM ergonomics.
4. **Build-Green ≠ Product-Ready**
   - Passing `tsc --noEmit`, `vite build`, and HTTP 200 API checks created false confidence that the Console was ready for real APM engineers.

---

## 2. Permanent Systemic Safeguards Installed

1. **Codified in [`AGENTS.md` Section 5](../AGENTS.md)**:
   - Hard "Chart-First, Table-Drilldown" rule for every observability module.
   - Backend time-series / distribution / breakdown APIs are now a prerequisite for every new metric domain.
   - Global multi-dimension filter bar + grouped sidebar + bilingual (`中文`/`EN`) baseline locked as permanent Console invariants.
2. **Backend Upgrades Completed in Console 2.0**:
   - `GET /v1/analytics/timeseries` (real daily Zero-Lie trends, missing days kept `null`).
   - `GET /v1/analytics/breakdown?dimension=version|adapter|scene` (real cross-slice comparison).
   - 19-bucket raw histogram arrays + 5-domain network failure breakdown exposed directly in `/v1/analytics/startup`, `/v1/analytics/fluency`, and `/v1/analytics/network`.
   - 14-day realistic release story in `seed-replay` (`v1.0.0` baseline → `v1.1.0` cliff regression → `v1.2.0` recovery, with obfuscated R8/SourceMap/dSYM frames).
3. **Clean-Room ECharts Console 2.0**:
   - Modular tree-shaken `echarts` interactive charts (hover crosshairs, sparklines, stacked bars, dual-axis trends, 19-bucket distribution histograms, donut charts, transport stage waterfalls, zoomable flamegraphs) with 100% original Clean-Room code and zero mock data.
