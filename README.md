# Evidence Store

A backend service for ingesting, storing, and querying test evidence from heterogeneous sources (Bazel test logs, CI pipelines, manual test runs, HiL/PiL/vehicle tests).

Evidence Store provides a unified API to collect and query test results across different tools and workflows. It supports batch ingestion, cursor-based pagination, evidence inheritance across commits, configurable retention policies, and a web UI for manual test entry and search with regex filtering.

## Quick Start

```bash
docker compose up -d
curl http://localhost:8000/healthz
```

This starts PostgreSQL 16 and the Evidence Store server on port 8000.

### Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `EVIDENCE_DATABASE_URL` | `postgres://evidence:evidence@localhost:5432/evidence_store?sslmode=disable` | PostgreSQL connection string |
| `EVIDENCE_LISTEN_ADDR` | `:8000` | Listen address |
| `EVIDENCE_LOG_LEVEL` | `INFO` | Log level |
| `EVIDENCE_DEFAULT_PAGE_SIZE` | `100` | Default page size |
| `EVIDENCE_MAX_PAGE_SIZE` | `1000` | Max page size |
| `EVIDENCE_MAX_BATCH_SIZE` | `1000` | Max records per batch |
| `EVIDENCE_API_KEYS` | *(empty — auth disabled)* | Comma-separated API keys (see [Authentication](#authentication)) |

### Authentication

Set `EVIDENCE_API_KEYS` to enable API key authentication for all `/api/v1/*` endpoints. The `/healthz` endpoint and static web UI files are always public.

Each key entry has the format `role:key` where role is `rw` (read-write) or `ro` (read-only):

```bash
# Single read-write key
export EVIDENCE_API_KEYS="rw:my-secret-key"

# Multiple keys with different roles
export EVIDENCE_API_KEYS="rw:ingest-key-for-ci,ro:dashboard-viewer-key"
```

- **`rw`** keys can read and write (GET + POST).
- **`ro`** keys can only read (GET). POST requests return `403 Forbidden`.
- Requests without a valid key return `401 Unauthorized`.
- When `EVIDENCE_API_KEYS` is empty or unset, authentication is disabled (open access).

Clients authenticate by sending the key as a Bearer token:

```bash
curl -H "Authorization: Bearer my-secret-key" \
  http://localhost:8000/api/v1/evidence
```

The Bazel adapter supports this via `--api-key` or `EVIDENCE_STORE_API_KEY`. The web UI prompts for a key on first 401 and stores it in `localStorage`.

## Bazel Adapter

Scans `bazel-testlogs/` after a test run and uploads results to the Evidence Store.

### Build

```bash
bazel build //adapters/bazel/cmd/evidence-bazel
```

### Usage

```bash
# Run tests, then ingest results
bazel test //...
bazel run //adapters/bazel/cmd/evidence-bazel -- \
    --api-url http://localhost:8000 \
    --testlogs-dir "$(bazel info bazel-testlogs)"
```

Or use the dogfood script that does both:

```bash
./scripts/dogfood.sh
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--api-url` | `$EVIDENCE_STORE_URL` | API base URL (required) |
| `--testlogs-dir` | `bazel-testlogs` | Path to testlogs directory |
| `--repo` | auto-detected | Repository (from git remote) |
| `--branch` | auto-detected | Branch (from git) |
| `--rcs-ref` | auto-detected | Commit hash (from git HEAD) |
| `--source` | auto-detected | CI build URL or username |
| `--invocation-id` | | Bazel invocation ID |
| `--tags` | | Comma-separated tags |
| `--api-key` | `$EVIDENCE_STORE_API_KEY` | API key |
| `--dry-run` | `false` | Print records instead of posting |

### Watch Mode (Automatic Ingestion)

The adapter can run as a background watcher that automatically uploads test results after every `bazel test` — no changes to your build workflow needed.

```bash
# One-time setup: create .evidence/config.yaml in your workspace
mkdir -p .evidence
cat > .evidence/config.yaml <<EOF
api_url: https://evidence.mycompany.com
tags: [local, dev]
EOF

# Start the watcher (runs in background)
bazel run //adapters/bazel/cmd/evidence-bazel -- watch start

# Check status
bazel run //adapters/bazel/cmd/evidence-bazel -- watch status

# Stop
bazel run //adapters/bazel/cmd/evidence-bazel -- watch stop
```

The watcher polls `bazel-testlogs/` every 5 seconds, waits for Bazel to finish (lock released), then uploads only new/changed results. It reads config from `.evidence/config.yaml` and environment variables (`EVIDENCE_STORE_URL`, `EVIDENCE_STORE_API_KEY`). Logs go to `.evidence/watch.log`.

Use `--foreground` with `watch start` to run in the foreground (useful for debugging).

## API

Base URL: `/api/v1`

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/evidence` | Create a single record |
| `POST` | `/api/v1/evidence/batch` | Create records in batch |
| `GET` | `/api/v1/evidence` | List records (filtered, paginated) |
| `GET` | `/api/v1/evidence/{id}` | Get record by ID |
| `POST` | `/api/v1/inheritance` | Create an inheritance declaration |
| `GET` | `/api/v1/inheritance` | List inheritance declarations |
| `GET` | `/healthz` | Health check |

### Creating evidence

```bash
curl -X POST http://localhost:8000/api/v1/evidence \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "myorg/myrepo",
    "rcs_ref": "abc123",
    "procedure_ref": "//pkg:my_test",
    "evidence_type": "bazel",
    "source": "ci",
    "result": "PASS",
    "finished_at": "2026-01-01T00:00:00Z"
  }'
```

Result must be one of: `PASS`, `FAIL`, `ERROR`, `SKIPPED`.

`finished_at` accepts RFC3339 (`2026-01-01T00:00:00Z`, `2026-01-01T12:00:00+02:00`) as well as shorter forms (`2026-01-01 14:00`, `2026-01-01`). Values without a timezone are interpreted as **UTC**. All timestamps are normalized to UTC on storage.

### Querying evidence

```bash
# List all
curl "http://localhost:8000/api/v1/evidence"

# Filter by repo and branch
curl "http://localhost:8000/api/v1/evidence?repo=myorg/myrepo&branch=main"

# Filter by result
curl "http://localhost:8000/api/v1/evidence?result=FAIL,ERROR"

# Filter by time range
curl "http://localhost:8000/api/v1/evidence?finished_after=2026-01-01T00:00:00Z"

# Paginate
curl "http://localhost:8000/api/v1/evidence?limit=10&cursor=<next_cursor>"
```

**Query parameters:** `repo`, `branch`, `rcs_ref`, `evidence_type`, `source`, `procedure_ref`, `result`, `finished_after`, `finished_before`, `tags`, `notes`, `limit`, `cursor`, `include_inherited`.

### Regex filtering

Text filter fields support regex matching via a `~` prefix. Without the prefix, filters use exact matching (backwards-compatible).

```bash
# Exact match (default)
curl "http://localhost:8000/api/v1/evidence?branch=main"

# Regex match — all release branches
curl "http://localhost:8000/api/v1/evidence?branch=~^release/.*"

# Regex on multiple fields — bazel-* types on org repos
curl "http://localhost:8000/api/v1/evidence?evidence_type=~^bazel&repo=~^myorg/"

# Regex on tags — match any tag starting with "nightly-"
curl "http://localhost:8000/api/v1/evidence?tags=~^nightly-"

# Regex on notes
curl "http://localhost:8000/api/v1/evidence?notes=~device.*XYZ"
```

**Supported fields:** `repo`, `branch`, `rcs_ref`, `evidence_type`, `source`, `procedure_ref`, `tags`, `notes`.

The regex engine is [PostgreSQL POSIX regular expressions](https://www.postgresql.org/docs/current/functions-matching.html#FUNCTIONS-POSIX-REGEXP) (the `~` operator). This supports the POSIX Extended Regular Expression syntax including character classes (`[a-z]`, `[[:digit:]]`), alternation (`a|b`), quantifiers (`*`, `+`, `?`, `{n,m}`), and anchors (`^`, `$`). Matching is case-sensitive.

## Development

### Build with Bazel

```bash
bazel build //...                    # build everything
bazel test //...                     # run all tests
bazel run //cmd/server               # start the server
```

### Run the smoke test

```bash
docker compose up -d
./scripts/smoke-test.sh
```
