# Caliper APM — Engineering & Anti-Drift Operating Rules (`AGENTS.md`)

This repository is engineered for long-horizon human + AI collaboration. Every contributor and AI agent MUST follow these rules strictly.

---

## 1. Mandatory Session Start Protocol (Anti-Drift Check)

Before writing code, proposing architecture changes, or resuming after context compaction:
1. Read [`NORTH_STAR.md`](NORTH_STAR.md) in full.
2. Read the active milestone section of [`docs/roadmap-v0.1.md`](docs/roadmap-v0.1.md).
3. Explicitly verify before starting work:
   - Which specific North Star outcome and milestone exit criterion does this task advance?
   - Does this task violate any of the 5 Hard Non-Goals in `NORTH_STAR.md`?
   - If the answer is unclear or drifts from the current stage, **stop and ask the owner**.

---

## 2. Clean-Room & Security Redlines

- **100% Clean-Room code**: All source code, schemas, UI components, documentation, and test fixtures in this repo must be authored from scratch using public APIs and general industry practices.
- **Zero proprietary references**: Never commit private/employer brand names, internal domains, private RFC1918 host IPs, absolute personal filesystem paths (`/Users/...`), cloud project IDs, or API keys.
- **Mandatory guard check**: Run `./scripts/cleanroom-guard.sh` before every commit (and install it as `.git/hooks/pre-commit`). Keep personal/employer keywords only in gitignored `.cleanroom.denylist.local`, never in tracked files.

---

## 3. Core Architecture Invariants

1. **Native-First Separation**:
   - `packages/sdk-android` and `packages/sdk-ios` MUST have **zero dependency** on React Native, Hermes, or JavaScript tooling.
   - `packages/sdk-react-native` is a strictly optional adapter layer built on top of the Native SDK public APIs.
2. **Golden Contract Parity**:
   - Deterministic sampling (FNV-1a), histogram bucketing, sliding 24h quota, lease expiration, and zero-frame classification MUST pass the shared language-neutral vectors in `packages/sdk-core/fixtures/contract-vectors.json` across Android, iOS, TypeScript, and Go.
3. **Zero-Lie Statistics**:
   - Follow [`spec/metrics-contract.md`](spec/metrics-contract.md) without exception.
   - Never zero-fill missing days/buckets; never average daily P50/P90; never treat `frame_count == 0` as 0% jank; always return explicit `missing_reason` / `sample_gate_status`.
4. **Real-Data-Only Console**:
   - Every view in `console/` must query live endpoints backed by `server/storage`.
   - Seed/demo data must enter through the exact same ingestion pipeline (`POST /v1/ingest`) as real SDK traffic—never bypass ingestion with frontend mock data.
5. **Fail-Closed Client Safety**:
   - All SDK collection, persistence, compression, and upload must run off the main thread.
   - Any unexpected SDK error must be caught, logged silently in debug builds, and trip a local circuit breaker so the host app is never crashed or stalled.

---

## 4. Truthful Status & Verification Standards

Distinguish states honestly—one never substitutes for another:
- **Implemented**: Code compiles and unit tests pass.
- **Contract-Verified**: Passes shared golden contract tests and schema validation.
- **E2E-Verified**: Verified end-to-end from real client/Demo App → Collector → DuckDB/SQLite → Symbolicator → Console/Webhook.
- **Benchmark-Verified**: Measured on physical Release builds (SDK OFF vs ON) under reproducible scripts in `benchmarks/`.

Never claim a milestone is complete based on unit tests alone when E2E or benchmark gates are required.
