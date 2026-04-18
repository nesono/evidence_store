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

The adapter lives in `adapters/bazel/` as its own Bzlmod module named `evidence_store_bazel` so other Bazel workspaces can consume it without pulling in the server's dependencies.

### Consume from another Bazel workspace

Add this to the consuming repo's `MODULE.bazel`:

```starlark
bazel_dep(name = "evidence_store_bazel", version = "0.0.1")

git_override(
    module_name = "evidence_store_bazel",
    remote = "https://github.com/nesono/evidence_store.git",
    commit = "<pinned-sha>",
    strip_prefix = "adapters/bazel",
)
```

Then from the consumer workspace:

```bash
bazel run @evidence_store_bazel//cmd/evidence-bazel -- watch start
```

For local development against a checkout:

```starlark
local_path_override(
    module_name = "evidence_store_bazel",
    path = "/path/to/evidence_store/adapters/bazel",
)
```

### Build (inside this repo)

```bash
cd adapters/bazel
bazel build //cmd/evidence-bazel
```

### Usage

```bash
# Run tests, then ingest results (from the adapter workspace)
bazel test //...
bazel run //cmd/evidence-bazel -- \
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

### Recording ad-hoc results (`record` subcommand)

Not all test workflows produce a JUnit `test.xml`. Failure tests (where a `bazel build` is *expected* to fail with a specific stderr pattern) and shell-driven integration tests determine pass/fail outside Bazel's test runner. For these, use the `record` subcommand to emit a single evidence record with an externally-determined verdict.

```bash
# Manual pass/fail decision
evidence-bazel record \
    --procedure-ref "//fire/starlark/failure_test:version_too_old_basic" \
    --result PASS \
    --notes "expected 'static_assert' pattern found in stderr" \
    --tags failure_test,version_too_old \
    --evidence-type bazel-failure-test
```

#### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--procedure-ref` | | Target label / test identifier (required) |
| `--result` | | `PASS`, `FAIL`, `ERROR`, or `SKIPPED` (required, case-insensitive) |
| `--evidence-type` | `bazel-manual` | Evidence type string |
| `--notes` | | Free-text stored under `metadata.notes` |
| `--tags` | | Comma-separated tags stored under `metadata.tags` |
| `--duration-ms` | | Duration in milliseconds (optional) |
| `--metadata` | | JSON object to merge into metadata (e.g. `'{"pattern":"static_assert"}'`) |
| `--invocation-id` | | Group multiple records from the same run |
| `--finished-at` | now (UTC) | RFC3339 timestamp |
| `--repo`, `--branch`, `--rcs-ref`, `--source` | auto-detected | Same as ingest path |
| `--api-url`, `--api-key` | `.evidence/config.yaml` → env vars | See below |
| `--dry-run` | `false` | Print the record as JSON instead of posting |

Config resolution order (highest priority first): command-line flag → `EVIDENCE_STORE_URL` / `EVIDENCE_STORE_API_KEY` env vars → `.evidence/config.yaml` (searched upward from `BUILD_WORKSPACE_DIRECTORY` or cwd). This matches the watcher's behavior so the same config works for both.

#### Example: driving failure tests from a shell script

```bash
INVOCATION_ID=$(uuidgen)
for tgt in $(discover_failure_tests); do
    if bazel build "$tgt" 2> stderr.log; then
        evidence-bazel record --procedure-ref "$tgt" --result FAIL \
            --notes "expected build failure but build succeeded" \
            --invocation-id "$INVOCATION_ID"
    elif grep -q "static_assert" stderr.log; then
        evidence-bazel record --procedure-ref "$tgt" --result PASS \
            --invocation-id "$INVOCATION_ID"
    else
        evidence-bazel record --procedure-ref "$tgt" --result ERROR \
            --notes "build failed without expected pattern" \
            --invocation-id "$INVOCATION_ID"
    fi
done
```

### Watch Mode (Automatic Ingestion)

The adapter can run as a background watcher that automatically uploads test results after every `bazel test` — no changes to your build workflow needed. The commands below assume a consumer workspace with `evidence_store_bazel` added as a `bazel_dep`; replace `@evidence_store_bazel//cmd/evidence-bazel` with `//cmd/evidence-bazel` when running from inside `adapters/bazel/` in this repo.

```bash
# One-time setup: create .evidence/config.yaml in your workspace
mkdir -p .evidence
cat > .evidence/config.yaml <<EOF
api_url: https://evidence.mycompany.com
tags: [local, dev]
EOF

# Start the watcher (runs in background)
bazel run @evidence_store_bazel//cmd/evidence-bazel -- watch start

# Check status
bazel run @evidence_store_bazel//cmd/evidence-bazel -- watch status

# Stop
bazel run @evidence_store_bazel//cmd/evidence-bazel -- watch stop
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
