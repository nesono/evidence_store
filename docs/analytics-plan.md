# Analytics — Implementation Plan

Closes #58.

## Problem

The evidence store accumulates every test verdict from CI, Bazel, and manual runs, but the only way to read it is the Search tab — one record at a time. The value of the archive is in the aggregate: which tests never fail, which are permanently broken, which drown in infrastructure errors, and which fail together.

## Requirements (from issue)

- Tests that **never fail** — candidates for reduced execution frequency.
- Tests that are **almost always failing** — chronically broken, candidates for quarantine.
- Tests with the **most infrastructure issues** (`ERROR` rather than `FAIL`/`PASS`).
- Tests that **fail in clusters** — so a subset can be chosen that covers most failures with fewer tests.
- "And potentially more."

The last one is addressed by treating the above as four views over one metrics table, so new questions are new sort keys rather than new endpoints.

## Existing State

- `evidence` holds one row per test verdict: `repo`, `branch`, `rcs_ref`, `procedure_ref`, `evidence_type`, `source`, `result` (`PASS`/`FAIL`/`ERROR`/`SKIPPED`), `finished_at`, `metadata` JSONB.
- There is no run/invocation identity in the schema. The Bazel adapter writes an optional `metadata.invocation_id` (`adapters/bazel/cmd/evidence-bazel/record.go:106`), and `rcs_ref` groups everything tested at one commit.
- `EvidenceStore.List` (`internal/store/evidence.go`) already implements the filter vocabulary — exact match, `~` regex, `*` prefix on `procedure_ref`, tag containment. Analytics should accept the same vocabulary.
- Filter parsing lives inline in `EvidenceHandler.List` (`internal/api/evidence.go:166`). It needs extracting before a second endpoint family can reuse it.
- The web UI is vanilla ES modules + Pico CSS, no build step. Tabs are switched by `data-tab` in `web/static/index.html`, and the Search tab reads its filters from the URL — so analytics can deep-link into it for free.

## Design

### Identity: what is a "test", what is a "run"

**Test** = `(repo, procedure_ref)`. Optionally widened to `(repo, procedure_ref, evidence_type)` via `group_by=evidence_type`, for the case where the same procedure runs under several harnesses and their reliability differs.

**Run** = the set of tests executed together, which is what co-failure clustering needs. The schema has no explicit run column, so the run key is derived and selectable:

| `run_key=` | expression | when to use |
|---|---|---|
| `auto` (default) | `COALESCE(metadata->>'invocation_id', rcs_ref)` | mixed sources |
| `invocation` | `metadata->>'invocation_id'` | Bazel adapter with invocation IDs |
| `commit` | `rcs_ref` | one test pass per commit |

Always scoped by `repo`. Documented explicitly because clustering quality depends entirely on this choice, and getting it silently wrong produces plausible-looking nonsense.

### Metrics, per test, over a filtered window

Counts come straight from `count(*) FILTER (WHERE result = …)`.

- `runs`, `pass`, `fail`, `error`, `skipped`
- `verdict_runs` = `pass + fail` — runs that produced an actual verdict
- **`fail_rate`** = `fail / verdict_runs`. Deliberately excludes `ERROR` and `SKIPPED`: an infrastructure crash is not a statement about the test's correctness, and folding it in makes a flaky-infra test look broken.
- **`error_rate`** = `error / (pass + fail + error)`. The infrastructure-issue ranking.
- **`flip_rate`** = fraction of consecutive verdict pairs (ordered by `finished_at, id`) that differ. This is the key separator the raw fail rate cannot give you: a permanently broken test has `fail_rate = 1.0` and `flip_rate = 0`, a flaky one has a middling fail rate and a high flip rate. Computed with `lag()` over the partition.
- **`flaky_commits`** = number of distinct `rcs_ref` values where the test both passed and failed. Same code, different outcome — the strongest available flakiness evidence, and immune to the run-key question.
- **`pass_rate_lower`** = Wilson score lower bound (95%) on the pass rate. Used to rank "never fails" so that 500/500 outranks 8/8 instead of tying at 1.0.
- `first_seen`, `last_seen`, `last_pass_at`, `last_fail_at`, `last_result`

Everything except `pass_rate_lower` is one SQL pass; the Wilson bound is a pure Go function over the counts.

### Labels

The API returns a **list** of labels per test, not one category — a test can legitimately be both flaky and infra-heavy, and forcing a precedence order hides that.

| label | rule |
|---|---|
| `stable` | `verdict_runs ≥ min_runs` and `fail = 0` and `error = 0` |
| `always_failing` | `verdict_runs ≥ min_runs` and `fail_rate ≥ 0.9` |
| `flaky` | `flaky_commits > 0` or `flip_rate ≥ 0.2` |
| `infra_heavy` | `error_rate ≥ 0.1` and `error ≥ 3` |
| `sparse` | `verdict_runs < min_runs` — too little data to judge |

Thresholds are query parameters with these defaults, not constants. `min_runs` defaults to 10.

### Clustering and minimal test selection

Fetch only failing rows, projected to two columns:

```sql
SELECT DISTINCT <run_key> AS run, repo, procedure_ref
FROM evidence
WHERE <filter> AND result IN ('FAIL', 'ERROR')
```

That set is far smaller than the table, and the rest is pure Go over bitsets (one bit per run, per test):

1. **Co-failure similarity** — Jaccard `|fail(a) ∩ fail(b)| / |fail(a) ∪ fail(b)|` over all test pairs. `O(T²)` with cheap bitset ops; at the 2,000-test cap that is 2M pairs and ~13 MB of bitsets.
2. **Clusters** — union-find over pairs above `threshold` (default 0.6), i.e. single-link agglomerative. Each cluster reports its members, the runs it covers, and its tightest pair.
3. **Minimal covering set** — greedy set cover over failing runs: repeatedly take the test that covers the most still-uncovered failing runs. Returns an *ordered* list with cumulative coverage, so the answer reads as "these 12 tests catch 91% of failing runs, the next 40 add 4%." Greedy is the standard `ln n` approximation and is the right tool here; exact set cover is NP-hard and no more useful for this decision.

Guardrails: hard caps on rows scanned (200k) and tests considered (2,000, by failure count). Exceeding either returns `422` telling the caller to narrow the window, rather than quietly truncating and reporting a wrong coverage percentage.

### Where the work happens

Aggregation in Postgres, correlation in Go, nothing precomputed in phase 1. The per-test query is a single grouped scan over a time window; the cluster query returns a projection of failing rows only. If measurement shows this is too slow at real volumes, the escape hatch is a nightly rollup table keyed by `(repo, procedure_ref, day)` — deferred until there is a number that justifies it.

One new index, to be confirmed by `EXPLAIN ANALYZE` against seeded data before being committed:

```sql
-- migrations/000004_add_analytics_index.up.sql
CREATE INDEX idx_evidence_analytics
    ON evidence (repo, finished_at DESC)
    INCLUDE (procedure_ref, result, rcs_ref, branch, evidence_type);
```

Today `repo` and `finished_at` are separate single-column indexes, so the planner bitmap-ANDs them and then hits the heap for every row. The composite with `INCLUDE` allows an index-only scan for the whole aggregation. The cost is index size — measure both before merging.

## API

Four endpoints under `/api/v1/analytics`, all accepting the **full existing evidence filter vocabulary** (`repo`, `branch`, `evidence_type`, `source`, `procedure_ref`, `tags`, `notes`, `finished_after`, `finished_before`, including `~` regex and `*` prefix forms) via the extracted shared parser.

```
GET /api/v1/analytics/summary    headline counts for the window
GET /api/v1/analytics/tests      the per-test metrics table
GET /api/v1/analytics/clusters   co-failure clusters + minimal covering set
GET /api/v1/analytics/timeline   one test's verdict history
```

Extra parameters: `min_runs`, `group_by`, `run_key`, `sort`, `order`, `limit`, `offset`, and the label thresholds.

`GET /analytics/tests?repo=org/repo&finished_after=2026-06-01&sort=error_rate&order=desc`:

```json
{
  "window": { "from": "2026-06-01T00:00:00Z", "to": "2026-07-26T00:00:00Z", "runs": 1240 },
  "tests": [
    {
      "repo": "org/repo",
      "procedure_ref": "//pkg:integration_test",
      "runs": 210, "pass": 168, "fail": 20, "error": 22, "skipped": 0,
      "verdict_runs": 188,
      "fail_rate": 0.106, "error_rate": 0.117, "flip_rate": 0.14,
      "flaky_commits": 3,
      "pass_rate_lower": 0.837,
      "last_result": "PASS",
      "last_fail_at": "2026-07-24T09:12:00Z",
      "labels": ["flaky", "infra_heavy"]
    }
  ],
  "total": 843
}
```

`GET /analytics/clusters`:

```json
{
  "run_key": "invocation",
  "failing_runs": 96,
  "clusters": [
    {
      "id": 1, "size": 7, "covers_runs": 41, "cohesion": 0.78,
      "members": ["//pkg/net:a_test", "//pkg/net:b_test", "..."]
    }
  ],
  "minimal_set": [
    { "procedure_ref": "//pkg/net:a_test", "new_runs": 41, "cumulative": 41, "coverage": 0.427 },
    { "procedure_ref": "//pkg/db:c_test",  "new_runs": 22, "cumulative": 63, "coverage": 0.656 }
  ]
}
```

`sort` is whitelisted exactly as `store.sortColumns` already does for the list endpoint, since the column name is interpolated.

## UI

A fourth nav tab, `Analytics`, with three sub-views sharing the existing filter bar (repo, branch, time range) so the filter code is reused rather than duplicated:

- **Overview** — headline cards (runs, distinct tests, pass rate, infra-error rate) and a stacked PASS/FAIL/ERROR bar over time.
- **Tests** — the metrics table, sortable by every column, with label chips and quick presets that are just sort+filter combinations: *Never fails*, *Always fails*, *Most infra errors*, *Flakiest*. Clicking a row deep-links into the Search tab with `?repo=…&procedure_ref=…&result=FAIL` — the Search page already restores filters from the URL, so this needs no new plumbing.
- **Clusters** — the cluster list, and the minimal covering set as a table with a cumulative-coverage bar.

**Charts**: the page needs a sparkline and a stacked bar, nothing more. I recommend hand-rolled inline SVG (~100 lines in a small `chart.js` module) over pulling a charting library from a CDN. `index.html` does already load Pico from jsdelivr, so a CDN dependency is not unprecedented — but a full chart library is a large surface for two chart types, and inline SVG keeps the page working offline and in air-gapped deployments. Flagged as the one call worth your input before I start on phase 3.

## Implementation Phases

Tests first at each step, per the project's practice — the metric math lands as pure functions with table-driven unit tests before any SQL or HTTP exists.

**Phase 0 — groundwork**
- Extract `parseEvidenceFilter(url.Values) (model.EvidenceFilter, error)` from `EvidenceHandler.List` into `internal/api/filter.go`. Pure refactor; the existing integration tests in `tests/` are the regression net.

**Phase 1 — per-test metrics** (answers "never fails", "always fails", "most infra issues")
- `internal/analytics/`: `WilsonLower`, `FlipRate`, `Labels` as pure functions + unit tests.
- `internal/store/analytics.go`: the grouped aggregation query.
- `internal/api/analytics.go`: `/summary`, `/tests`.
- `migrations/000004_add_analytics_index` (after measuring).
- `tests/analytics_integration_test.go` against the testcontainers Postgres, seeding a known fixture so every metric has an asserted exact value.

**Phase 2 — clustering** (answers "failing in clusters")
- `internal/analytics/cluster.go`: bitset Jaccard, union-find, greedy cover — all pure, all unit-tested against hand-computed examples including the adversarial case where greedy is known to be suboptimal.
- `/clusters` endpoint with the row/test caps and their `422`.

**Phase 3 — UI**
- `web/static/analytics.js`, the nav tab, the three sub-views, deep-linking into Search.

**Phase 4 — follow-ons, only if measurement demands**
- Short-TTL in-memory response cache keyed by filter hash (dashboards re-request identical windows).
- Nightly rollup table if the live aggregation is too slow.
- CSV export of the tests table.

## Testing

- **Unit** (`internal/analytics`) — no database. Wilson bounds against published values; flip rate on hand-built sequences; Jaccard and set cover on small graphs with known answers; label boundaries at exactly the threshold.
- **Integration** (`tests/`) — seed a deterministic fixture: one never-failing test, one always-failing, one flaky (passes and fails at the same `rcs_ref`), one error-heavy, and two that always fail together. Assert every metric and that the minimal set collapses the co-failing pair to one entry.
- **Performance** — extend `scripts/seed-demo` to generate a few hundred thousand rows, and record `EXPLAIN ANALYZE` timings before and after the new index in the PR.

## Open Questions

1. **Chart rendering** — hand-rolled SVG (recommended) or a CDN chart library?
2. **Default run key** — `auto` assumes `invocation_id` and `rcs_ref` are never mixed within one repo in a way that would fragment runs. If your CI does both, `commit` may be the safer default.
3. **`SKIPPED` handling** — currently excluded from every rate. A test that is skipped 90% of the time is arguably its own problem worth surfacing; say the word and I will add `skip_rate` and a `mostly_skipped` label.
4. **Cross-repo view** — the design scopes everything to one repo. Aggregating across repos is a one-line change to the grouping, but the resulting table is usually too noisy to act on. Left out unless you want it.
