# Self-Hosting & Production Deployment Guide

Caliper APM supports three deployment tiers using the **same Go binary** (`server/cmd/caliper`):

---

## 1. Tier 1 — Single-Binary Embedded Mode (< 1 Minute)

Ideal for local evaluation, QA labs, and small-to-medium apps (handles 30k–75k events/s bursts with sub-millisecond analytical queries):

```bash
# Build Console 2.4 static assets
(cd console && npm ci && npm run build)

# Build zero-CGO Go binary
(cd server && CGO_ENABLED=0 go build -o ../bin/caliper ./cmd/caliper)

# Start server with WAL persistence & 30s background alert evaluation
./bin/caliper \
  --addr 0.0.0.0:8080 \
  --data-dir /var/lib/caliper \
  --console-dist ./console/dist \
  --alert-interval 30s
```

Or run the turnkey local sandbox (with pre-seeded 14-day release story):
```bash
./scripts/dev-quickstart.sh
```

---

## 2. Tier 2 — Stateless Multi-Replica HA Cluster (ClickHouse + PostgreSQL + S3/MinIO)

For high-volume production fleets (tens of millions of daily events across horizontally scaled collector pods), run **zero local disk state** with ClickHouse for OLAP analytics (`--storage-driver=clickhouse`), PostgreSQL 15+ for shared metadata/triage/audit state (`--meta-driver=postgres`), and S3/MinIO for symbol bundles + remote logs (`--object-store=s3`):

```bash
docker compose --profile cluster up -d --build
```

Manual stateless CLI invocation (zero CGO static binary):
```bash
export CALIPER_POSTGRES_DSN="postgres://caliper:strong_pass@postgres:5432/caliper?sslmode=require"
export CALIPER_S3_ACCESS_KEY="..."
export CALIPER_S3_SECRET_KEY="..."

./bin/caliper \
  --addr 0.0.0.0:8080 \
  --storage-driver clickhouse \
  --clickhouse-endpoint http://clickhouse:8123 \
  --clickhouse-db caliper \
  --clickhouse-migrate=true \
  --meta-driver postgres \
  --postgres-migrate=true \
  --postgres-max-conns 25 \
  --object-store s3 \
  --s3-endpoint https://s3.us-east-1.amazonaws.com \
  --s3-bucket caliper-symbols-prod \
  --s3-region us-east-1 \
  --auth-config /etc/caliper/rbac.json \
  --console-dist /app/console-dist \
  --alert-interval 30s
```

The embedded PostgreSQL schema ([`server/internal/storage/postgres/schema.sql`](../server/internal/storage/postgres/schema.sql)) is applied idempotently on startup when `--postgres-migrate=true`, and `/healthz` performs live PostgreSQL connectivity checks for Kubernetes readiness probes.

---

## 3. Tier 3 — Kubernetes Helm Chart

Deploy directly via [`deploy/helm/caliper-apm`](../deploy/helm/caliper-apm):

```bash
helm upgrade --install caliper ./deploy/helm/caliper-apm \
  --namespace observability --create-namespace \
  --set replicaCount=3 \
  --set storage.driver=clickhouse \
  --set storage.clickhouseEndpoint=http://clickhouse.observability:8123 \
  --set metaStore.driver=postgres \
  --set metaStore.postgresDsn="postgres://caliper:pass@postgres.observability:5432/caliper?sslmode=require" \
  --set objectStore.driver=s3 \
  --set objectStore.s3Endpoint=http://minio.observability:9000 \
  --set objectStore.s3Bucket=caliper-blobs
```

The chart ships with `/healthz` liveness/readiness probes and standard Prometheus scrape annotations (`prometheus.io/scrape: "true"`, `prometheus.io/path: "/metrics"`).

---

## 4. RBAC & Security Configuration (`--auth-config`)

Never expose an unauthenticated production collector. Configure role-scoped tokens (`ingest_only`, `viewer`, `operator`, `admin`) with optional `app_ids` isolation:

```json
{
  "tokens": [
    { "token": "ingest-mobile-key", "actor": "mobile-fleet", "role": "ingest_only", "app_ids": ["com.example.app"] },
    { "token": "ci-release-key",    "actor": "github-ci",    "role": "operator",    "app_ids": ["com.example.app"] },
    { "token": "oncall-admin-key",  "actor": "sre-oncall",   "role": "admin" }
  ]
}
```

- Single-user timeline lookup (`GET /v1/sessions/lookup`), remote log pulls (`POST /v1/diagnostics/log-tasks`), policy updates (`PUT /v1/policy`), and issue triage (`PATCH /v1/stability/issues/{fp}`) all record immutable entries to `audit.wal.jsonl` (`GET /v1/audit/logs`).

---

## 5. CI/CD Zero-Lie Release Quality Gate

Drop [`packages/github-action/release-gate/action.yml`](../packages/github-action/release-gate/action.yml) into your release workflow to automatically gate canary promotions:

```yaml
- uses: ./packages/github-action/release-gate
  with:
    server-url: https://apm.example.com
    api-key: ${{ secrets.CALIPER_API_KEY }}
    platform: android
    baseline-version: "1.0.0"
    candidate-version: "1.1.0"
    require-symbols: "true"
    post-pr-comment: "true"
```

Exit codes: `0` (`PASS`), `2` (`BLOCK` — statistically significant regression), `3` (`GATED` — insufficient canary samples).

---

## 6. Self-Observability

- **Health**: `GET /healthz`
- **Telemetry Data Integrity**: `GET /v1/ops/source-health?platform=android` (contract acceptance rate, symbol coverage %, zero-frame window isolation ratio)
- **Prometheus Metrics**: `GET /metrics` (`caliper_ingest_events_accepted_total`, `caliper_ingest_events_rejected_total`, `caliper_storage_total_events`, etc.)
- **OTLP Export**: `GET /v1/otlp/export?platform=android`
