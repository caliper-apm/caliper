# Security Policy

## Privacy & Security Architecture

Caliper APM is engineered with **Zero-PII by default**:
- Client identifiers (`install_hash`, `user_hash`) are strictly validated as 16–64 lowercase hex hashes — raw emails, phone numbers, and usernames are rejected at schema validation.
- Diagnostic ring logs scrub emails and Bearer tokens on-device prior to gzip upload.
- Trimmed heap snapshots transmit class-level retained sizes only — never raw heap object values or string contents.
- Production deployments should run with `--auth-config <rbac.json>` (four least-privilege roles: `ingest_only`, `viewer`, `operator`, `admin`) with workspace `app_ids` scoping.
- Single-user timeline lookups (`GET /v1/sessions/lookup`) and remote log tasks (`POST /v1/diagnostics/log-tasks`) enforce **fail-closed append-only security auditing** (`audit.wal.jsonl` / PostgreSQL `caliper_audit_logs`).

## Reporting a Vulnerability

Please do **not** open public GitHub issues for security vulnerabilities. Report suspected vulnerabilities privately via GitHub Security Advisories in this repository. We aim to acknowledge reports within 48 hours.
