# Evidence Store

A backend service for ingesting, storing, and querying test evidence from heterogeneous sources.

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

**Query parameters:** `repo`, `branch`, `rcs_ref`, `evidence_type`, `source`, `procedure_ref`, `result`, `finished_after`, `finished_before`, `tags`, `limit`, `cursor`, `include_inherited`.

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
