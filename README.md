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
| `EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS` | `30` | How long an analytics aggregation is reused for an identical filter (`0` disables) |
| `EVIDENCE_API_KEYS` | *(empty — auth disabled)* | Comma-separated API keys (see [Authentication](#authentication)) |
| `EVIDENCE_RATE_LIMIT_READ_RPS` | `0` (disabled) | Sustained reads per second per caller (see [Rate limiting](#rate-limiting)) |
| `EVIDENCE_RATE_LIMIT_WRITE_RPS` | `0` (disabled) | Sustained writes per second per caller |
| `EVIDENCE_RATE_LIMIT_READ_BURST` | `2 × read RPS` | Token-bucket burst capacity for reads |
| `EVIDENCE_RATE_LIMIT_WRITE_BURST` | `2 × write RPS` | Token-bucket burst capacity for writes |

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

### Rate limiting

Set `EVIDENCE_RATE_LIMIT_READ_RPS` and/or `EVIDENCE_RATE_LIMIT_WRITE_RPS` to enforce per-caller token-bucket limits on `/api/v1/*` endpoints. Reads (GET/HEAD/OPTIONS) and writes (POST/PUT/PATCH/DELETE) use separate buckets so a flood of reads cannot starve writes.

```bash
# 50 reads/sec sustained (burst 100), 10 writes/sec sustained (burst 20).
export EVIDENCE_RATE_LIMIT_READ_RPS=50
export EVIDENCE_RATE_LIMIT_WRITE_RPS=10
```

- Authenticated callers are bucketed by API key; unauthenticated callers by IP (`X-Forwarded-For` honored).
- When a limit is exceeded the server returns `429 Too Many Requests` with a `Retry-After` header (seconds).
- Limits are in-memory and per process — deployments running multiple replicas should add a shared limiter (e.g. Redis) if precise global enforcement is required.

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

From a consumer workspace that has `evidence_store_bazel` as a `bazel_dep`:

```bash
bazel run @evidence_store_bazel//cmd/evidence-bazel -- record \
    --procedure-ref "//fire/starlark/failure_test:version_too_old_basic" \
    --result PASS \
    --notes "expected 'static_assert' pattern found in stderr" \
    --tags failure_test,version_too_old \
    --evidence-type bazel_failure_test
```

`--evidence-type` must match `^[a-z][a-z0-9_]{0,63}$` (lowercase, underscores, no hyphens).

For calls in tight loops, avoid `bazel run` in the inner loop — its per-invocation configuration (and any differing `--action_env` flags on your other builds) churns the analysis cache. Build once up-front and exec the binary directly:

```bash
bazel build @evidence_store_bazel//cmd/evidence-bazel
BIN="$(bazel info workspace)/$(bazel cquery --output=files @evidence_store_bazel//cmd/evidence-bazel)"

for tgt in $targets; do
    "$BIN" record --procedure-ref "$tgt" --result PASS ...
done
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
| `--api-url`, `--api-key` | `.evidence/config.yaml` | See below |
| `--dry-run` | `false` | Print the record as JSON instead of posting |

Config resolution order (highest priority first): command-line flag → `.evidence/config.yaml` (searched upward from `BUILD_WORKSPACE_DIRECTORY` or cwd). Environment variables are deliberately not consulted by `record`, so the binary's behavior is stable across shell-env changes and Bazel's analysis cache is not invalidated by invocations that happen to have different env.

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

### Releasing to the Bazel Central Registry

The adapter module (`evidence_store_bazel`) is published to the [Bazel Central Registry](https://registry.bazel.build/) so consumers can pin it via `bazel_dep` without a `git_override`.

**Release flow:**

1. Bump `version` in `adapters/bazel/MODULE.bazel`.
2. Commit and merge to `main`.
3. Tag the commit with `evidence_store_bazel-v<version>` (e.g., `evidence_store_bazel-v0.0.1`) and push the tag.
4. The `Release evidence_store_bazel` workflow (`.github/workflows/release-bazel-adapter.yml`) builds a source tarball of `adapters/bazel/`, creates a GitHub release, then calls the [`bazel-contrib/publish-to-bcr`](https://github.com/bazel-contrib/publish-to-bcr) reusable workflow to open a PR against [`bazelbuild/bazel-central-registry`](https://github.com/bazelbuild/bazel-central-registry) from the fork configured in the workflow.

**One-time setup (before the first release):**

- Fork `bazelbuild/bazel-central-registry` to `nesono/bazel-central-registry`.
- Create a classic Personal Access Token with `public_repo` scope and save it as the `BCR_PUBLISH_TOKEN` repository secret.
- BCR templates live in `.bcr/` at the repo root; `.bcr/config.yml` declares `adapters/bazel` as the module root.

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
| `GET` | `/api/v1/analytics/summary` | Headline counts for a filter window |
| `GET` | `/api/v1/analytics/tests` | Per-test reliability metrics (see [Analytics](#analytics)) |
| `GET` | `/api/v1/analytics/clusters` | Co-failure clusters and the minimal covering set |
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

# Paginate with a keyset cursor (streaming through a whole result set)
curl "http://localhost:8000/api/v1/evidence?limit=10&cursor=<next_cursor>"

# Paginate by offset (addressable windows — what the web UI uses)
curl "http://localhost:8000/api/v1/evidence?limit=50&offset=1000"

# Sort
curl "http://localhost:8000/api/v1/evidence?sort=finished_at&order=desc"

# Skip the COUNT(*) when paging (the total was already fetched with window 1)
curl "http://localhost:8000/api/v1/evidence?limit=50&offset=50&include_total=false"
```

**Query parameters:** `repo`, `branch`, `rcs_ref`, `evidence_type`, `source`, `procedure_ref`, `result`, `finished_after`, `finished_before`, `tags`, `notes`, `limit`, `cursor`, `offset`, `sort`, `order`, `include_total`, `include_inherited`.

### Pagination and sorting

Two pagination modes are available, and they cannot be combined — passing both `cursor` and `offset` (or both `cursor` and `sort`) returns `400`.

- **Cursor** — keyset pagination over the default `ingested_at` ordering. Stable while records are being inserted and cheap at any depth, so it is the right choice for streaming an entire result set. `next_cursor` is returned while more records remain; `total` is never returned for cursor requests.
- **Offset** — `offset` skips N matching rows, giving addressable, shareable windows (`?offset=1000&limit=50`) and backwards navigation. Because a keyset cursor only describes a position in the default ordering, `sort` requires offset mode and suppresses `next_cursor`.

`sort` accepts `repo`, `branch`, `rcs_ref`, `procedure_ref`, `evidence_type`, `source`, `result`, `finished_at` and `ingested_at`; any other value returns `400`. `order` is `asc` (default) or `desc`. Results are always tie-broken by `id`, so consecutive offset windows neither repeat nor skip a record.

`total` (a `COUNT(*)` of all matching rows) is returned by default for non-cursor requests. Pass `include_total=false` on subsequent windows to skip the count once you have it.

### Inherited records

When `include_inherited` is on (the default) and both `repo` and `rcs_ref` are given, evidence resolved through an inheritance declaration is returned in a separate `inherited_records` field rather than in `records`. Inherited evidence is resolved outside the paginated window, so mixing it into `records` would make the window's length disagree with `limit` and its position disagree with `total`.

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

**Supported fields:** `repo`, `branch`, `rcs_ref`, `ref`, `evidence_type`, `source`, `procedure_ref`, `tags`, `notes`.

### Matching a branch, tag or commit with one filter

`ref` matches a value against *either* identity column, so a caller who has "the thing they are looking at" does not have to say first whether it is a branch, a tag or a commit:

```bash
# Any of these work
curl "http://localhost:8000/api/v1/analytics/tests?ref=main"
curl "http://localhost:8000/api/v1/analytics/tests?ref=v2.0.0"
curl "http://localhost:8000/api/v1/analytics/tests?ref=aaaa111"     # abbreviated SHA
curl "http://localhost:8000/api/v1/analytics/tests?ref=~^release/"  # regex, both columns
```

Commits match by prefix, so a pasted short SHA finds the full one it abbreviates. Branches and tags match whole — a prefix there would answer a request for `release/1.1` with `release/1.10` as well. `ref` is available on every endpoint that takes filters, including `/api/v1/evidence`.

The regex engine is [PostgreSQL POSIX regular expressions](https://www.postgresql.org/docs/current/functions-matching.html#FUNCTIONS-POSIX-REGEXP) (the `~` operator). This supports the POSIX Extended Regular Expression syntax including character classes (`[a-z]`, `[[:digit:]]`), alternation (`a|b`), quantifiers (`*`, `+`, `?`, `{n,m}`), and anchors (`^`, `$`). Matching is case-sensitive.

## Analytics

The web UI's **Analytics** tab is the front end for everything below: an overview
of the filtered window, a sortable per-test table, and the co-failure clusters.
Selecting a row opens the matching raw records in Search.

Every column in the table is a sort key — click a header to rank by it, click
again to reverse. Each of the questions this feature exists to answer is one of
those columns, so ranking by fail rate, infra errors, flip rate or reliability is
a single click.

The tab does not query until **Apply** is pressed — these aggregations scan far
more evidence than a search does, so the page waits to be asked rather than
running the widest possible query on arrival. The time range defaults to the last
three hours and expands from there; relative ranges are resolved at query time,
so "last 3 hours" still means the last three hours an hour later.

`/api/v1/analytics/tests` collapses the evidence into one row per test — identified by `(repo, procedure_ref)` — so a suite can be judged rather than read record by record. It accepts every filter the list endpoint does, including the `~` regex and `*` prefix forms.

```bash
# Tests that never fail, best-evidenced first
curl "http://localhost:8000/api/v1/analytics/tests?repo=org/repo&sort=pass_rate_lower&order=desc"

# Chronically broken tests
curl "http://localhost:8000/api/v1/analytics/tests?repo=org/repo&sort=fail_rate&order=desc"

# Tests losing the most runs to infrastructure
curl "http://localhost:8000/api/v1/analytics/tests?repo=org/repo&sort=error_rate&order=desc"

# Flakiest tests in the last 30 days
curl "http://localhost:8000/api/v1/analytics/tests?repo=org/repo&finished_after=2026-06-27&sort=flip_rate&order=desc"
```

### Metrics

| Field | Meaning |
|-------|---------|
| `fail_rate` | `fail / (pass + fail)`. `ERROR` and `SKIPPED` are excluded — an infrastructure crash says nothing about whether the test is broken. |
| `error_rate` | `error / (pass + fail + error)`. The infrastructure-trouble ranking. |
| `flip_rate` | Share of consecutive verdicts that changed outcome. A permanently broken test has `fail_rate` 1.0 and `flip_rate` 0; a flaky one has a middling fail rate and a high flip rate. |
| `flaky_commits` | Commits where the test both passed and failed. Same code, different outcome. |
| `pass_rate_lower` | Wilson 95% lower bound on the pass rate. Ranks a clean 500-run record above a clean 8-run one, where the raw rate ties them at 1.0. |

### Labels

Each test carries a list of labels, not one category — a test can be both flaky and infrastructure-heavy.

| Label | Rule |
|-------|------|
| `stable` | At least `min_runs` verdicts, no failures and no errors |
| `always_failing` | At least `min_runs` verdicts and `fail_rate` ≥ `always_failing_rate` |
| `flaky` | Any `flaky_commits`, or `flip_rate` ≥ the `flip_rate` threshold |
| `infra_heavy` | At least `min_errors` errors and `error_rate` ≥ the `error_rate` threshold |
| `sparse` | Fewer than `min_runs` verdicts — too little history to judge |

Thresholds are query parameters (`min_runs`, `always_failing_rate`, `flip_rate`, `error_rate`, `min_errors`), because what counts as "almost always failing" is a judgement about a particular suite. The defaults are echoed back in every response.

Use `label=` to *filter* by one, which is a different question from sorting. Ranking by fail rate shows the worst tests, but on a healthy suite the worst may still fail only one run in ten; `label=always_failing` returns the ones that actually meet the threshold, and `label=stable` the ones that genuinely never fail:

```bash
curl "http://localhost:8000/api/v1/analytics/tests?repo=org/repo&label=always_failing"
curl "http://localhost:8000/api/v1/analytics/tests?repo=org/repo&label=stable&sort=pass_rate_lower&order=desc"
```

### Other parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `sort` | `procedure_ref` | `fail_rate`, `error_rate`, `flip_rate`, `pass_rate_lower`, `runs`, `pass`, `fail`, `error`, `flaky_commits`, `last_seen`, … |
| `label` | *(none)* | Keep only tests carrying a label: `stable`, `always_failing`, `flaky`, `infra_heavy`, `sparse` |
| `order` | `asc` | `asc` or `desc` |
| `limit`, `offset` | page size config | Windows the sorted set; `total` always describes the whole set |
| `format` | `json` | `csv` streams every matching row as a spreadsheet, ignoring `limit` and `offset` |
| `group_by` | *(none)* | `evidence_type` splits a procedure into one row per harness |

A query matching more than 50,000 distinct tests returns `422` rather than truncating, since a truncated set yields confident-looking numbers computed from part of the data.

### Caching

Sorting and paging are applied after the aggregation, so clicking a column header re-asks for a result whose inputs did not change. Aggregations are therefore reused for `EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS` (default 30) against an identical filter, which makes re-sorting and paging instant.

The cache is keyed by the filter and grouping only. Sort key, direction, paging and the labelling thresholds are applied to a fresh copy on every request, so two callers asking the same question with different thresholds still get different answers. The cost is that a window can lag newly ingested evidence by up to the TTL; set it to `0` to disable.
### CSV export

```bash
curl -o tests.csv "http://localhost:8000/api/v1/analytics/tests?repo=org/repo&sort=fail_rate&order=desc&format=csv"
```

The export covers the whole filtered set rather than the requested page — filters and sorting apply, `limit` and `offset` do not. Rates are written as plain decimals (`0.106`) rather than percentages so a spreadsheet reads them as numbers, and labels come through space-separated in one column. The **Export CSV** button on the Analytics tab downloads exactly what the table is currently showing.

Column order is a contract: new columns are appended, existing ones are not reordered or renamed.

### Co-failure clusters and test selection

`/api/v1/analytics/clusters` answers "which tests fail for the same reason, and how few of them still catch most failures".

```bash
curl "http://localhost:8000/api/v1/analytics/clusters?repo=org/repo&finished_after=2026-06-01"
```

```json
{
  "run_key": "auto",
  "include_errors": false,
  "threshold": 0.6,
  "tests": 34,
  "failing_runs": 96,
  "clusters": [
    { "id": 1, "size": 7, "covers_runs": 41, "cohesion": 0.78,
      "members": [{ "repo": "org/repo", "procedure_ref": "//pkg/net:a_test" }] }
  ],
  "minimal_set": [
    { "test": { "repo": "org/repo", "procedure_ref": "//pkg/net:a_test" },
      "new_runs": 41, "cumulative": 41, "coverage": 0.427 }
  ]
}
```

**`clusters`** groups tests whose failure sets are at least `threshold` similar (Jaccard), using single-link clustering — A and C group together if each is similar to B, even if not to each other. `cohesion` is the mean pairwise similarity inside the cluster, so a long chain scores lower than a group that genuinely all fail together. Solitary tests are omitted.

**`minimal_set`** is a greedy set cover, ordered so it reads as "these N tests catch X% of failing runs". Every entry contributes something no earlier entry did. It is an approximation, not a proven minimum — set cover is NP-hard, and greedy can be beaten on constructed inputs — but it is within a `ln n` factor and gives the ranked list the decision needs.

#### What counts as a "run"

The schema has no run column, so the grouping is derived, and clustering quality depends entirely on it:

| `run_key=` | Grouping | Use when |
|-----------|----------|----------|
| `auto` *(default)* | `metadata.invocation_id`, falling back to the commit | Mixed sources |
| `invocation` | `metadata.invocation_id`; rows without one are dropped | Every source sets it |
| `commit` | `rcs_ref` | A commit is tested once |

Runs are namespaced by repo, so a commit hash or invocation id reused across repos is never treated as one run.

#### Errors are excluded by default

`ERROR` rows are not counted as failures unless `include_errors=true`. An infrastructure outage fails every test in the same run simultaneously, which makes every pair of them look perfectly correlated — enough of those and the entire suite collapses into a single meaningless cluster that buries the real signal.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `threshold` | `0.6` | Jaccard similarity at which two tests are considered to fail together |
| `run_key` | `auto` | See above |
| `include_errors` | `false` | Count `ERROR` alongside `FAIL` |

Queries matching more than 200,000 failing rows, 2,000 distinct failing tests, or 20,000 failing runs return `422`.

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

### Seed demo data

`scripts/seed-demo` fills the database with synthetic evidence for demos and for
exercising the UI at realistic scale.

```bash
docker compose up -d db
go run ./scripts/seed-demo                 # 2,000,000 records (~1 minute)
go run ./scripts/seed-demo --count 50000   # a smaller set
go run ./scripts/seed-demo --truncate      # replace existing evidence
```

It writes to Postgres with `COPY` rather than through the API — the batch
endpoint inserts one row per round trip, which takes tens of minutes at this
size. API-level validation is therefore bypassed, so the generator is written to
produce records that satisfy it anyway.

Records are clustered onto a limited set of repositories, branches and commits so
that filtering returns meaningful groups, with a realistic verdict distribution
(88% `PASS`) and timestamps biased towards the recent past. `--seed` makes a run
reproducible. Two million records occupy roughly 900 MB including indexes.
