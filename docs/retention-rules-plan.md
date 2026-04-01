# Retention Rules — Implementation Plan

Closes #10.

## Problem

Not all evidence requires the same retention. Release branches should be retained indefinitely, while PR/feature branch evidence is only useful for weeks. Today there is no mechanism to automatically evict stale records.

## Requirements (from issue)

- Configurable retention rules via a **configuration file**
- Rules use **regex matching** on evidence fields to select records for eviction
- Evict matching records after **configurable time intervals**

## Existing State

- The DB schema already has a `retention_policy` table (`evidence_type`, `max_age_days`, `keep_failures`, `priority`). This table is too limited for the issue's requirements (no regex, no field-level matching). We will **replace it** with a config-file-driven approach.
- DESIGN.md Section 6 outlines a retention worker with pinning (`metadata.retain = true`) and inheritance protection. We will honour both.
- No background workers exist yet in the server.
- Config is currently env-var-only (`internal/config/config.go`).

## Design

### Retention config file

A YAML file pointed to by `EVIDENCE_RETENTION_CONFIG` (env var). If unset, no retention runs.

```yaml
# retention.yaml
interval: 24h          # how often the worker runs (default: 24h)

rules:
  - name: keep-releases-forever
    match:
      branch: "^(main|release/.*)$"
    max_age: 0                        # 0 = never delete
    priority: 100                     # higher = evaluated first

  - name: keep-failures-1y
    match:
      result: "^FAIL$"
    max_age: 8760h                    # 1 year
    priority: 90

  - name: pr-branches-short
    match:
      branch: "^(pr/|dependabot/|renovate/).*"
    max_age: 336h                     # 14 days
    priority: 50

  - name: default
    match: {}                         # matches everything
    max_age: 2160h                    # 90 days
    priority: 0
```

**Rule semantics:**
- `match` is a map of field name to regex pattern. All specified fields must match (AND logic). Supported fields: `repo`, `branch`, `rcs_ref`, `procedure_ref`, `evidence_type`, `source`, `result`.
- `max_age` is a Go `time.Duration`. `0` means never delete.
- `priority` determines evaluation order (highest first). First matching rule wins.
- Records with `metadata.retain = true` are always exempt (pinned).
- Records referenced by an active `inheritance_declaration` are always exempt.

### Components

#### 1. Config file loader — `internal/retention/config.go`

New package `internal/retention`.

```go
type RetentionConfig struct {
    Interval time.Duration   `yaml:"interval"`
    Rules    []RetentionRule `yaml:"rules"`
}

type RetentionRule struct {
    Name     string            `yaml:"name"`
    Match    map[string]string `yaml:"match"`    // field -> regex
    MaxAge   time.Duration     `yaml:"max_age"`
    Priority int               `yaml:"priority"`
}
```

- Loaded once at startup.
- Validates that all regexes compile and field names are valid.
- Sorts rules by priority descending.

#### 2. Rule evaluator — `internal/retention/evaluator.go`

```go
type Evaluator struct {
    rules []compiledRule // pre-compiled regexes, sorted by priority desc
}

func (e *Evaluator) MaxAge(ev *model.Evidence) time.Duration
```

- `MaxAge` returns the `max_age` of the first matching rule, or `-1` if no rule matches (no deletion).
- Pure function, no DB access — easy to unit test.

#### 3. Retention worker — `internal/retention/worker.go`

```go
type Worker struct {
    evaluator *Evaluator
    store     *store.EvidenceStore
    inherit   *store.InheritanceStore
    interval  time.Duration
    logger    *slog.Logger
}

func (w *Worker) Start(ctx context.Context)
func (w *Worker) RunOnce(ctx context.Context) (deleted int, err error)
```

- `Start` launches a ticker goroutine that calls `RunOnce` at the configured interval. Respects context cancellation for graceful shutdown.
- `RunOnce` (also useful for testing and manual trigger):
  1. Fetch all active inheritance declaration source refs (batch query) to build an exemption set.
  2. Scan evidence in batches (cursor-based, 1000 at a time, oldest first by `finished_at`).
  3. For each record:
     - Skip if `metadata.retain == true`.
     - Skip if `rcs_ref` is in the inheritance exemption set.
     - Evaluate against rules to get `max_age`.
     - If `max_age == 0` or `time.Since(finished_at) < max_age`, skip.
     - Otherwise, mark for deletion.
  4. Batch-delete marked records.
  5. Log summary (records scanned, deleted, skipped-pinned, skipped-inherited).

#### 4. Store additions — `internal/store/evidence.go`

```go
func (s *EvidenceStore) DeleteBatch(ctx context.Context, ids []uuid.UUID) (int64, error)
func (s *EvidenceStore) ScanAll(ctx context.Context, batchSize int, fn func([]model.Evidence) error) error
```

- `DeleteBatch`: `DELETE FROM evidence WHERE id = ANY($1)`, returns rows affected.
- `ScanAll`: cursor-based full-table scan ordered by `finished_at ASC` — calls `fn` for each batch.

#### 5. Inheritance store addition — `internal/store/inheritance.go`

```go
func (s *InheritanceStore) AllSourceRefs(ctx context.Context) (map[string]struct{}, error)
```

Returns the set of all `(repo, source_rcs_ref)` pairs that are currently referenced by inheritance declarations.

#### 6. DB migration — `migrations/000002_drop_retention_policy_table.up.sql`

```sql
DROP TABLE IF EXISTS retention_policy;
```

The table is unused (no code reads from it) and superseded by the config file approach.

#### 7. Server wiring — `cmd/server/main.go`

```go
if retentionPath := os.Getenv("EVIDENCE_RETENTION_CONFIG"); retentionPath != "" {
    retCfg, err := retention.LoadConfig(retentionPath)
    // ... error handling ...
    worker := retention.NewWorker(retCfg, evidenceStore, inheritanceStore, logger)
    go worker.Start(ctx)
}
```

The worker goroutine is cancelled via the existing `ctx` on shutdown.

### File summary

| File | Change |
|------|--------|
| `internal/retention/config.go` | **New** — config file loading and validation |
| `internal/retention/config_test.go` | **New** — YAML parsing, regex validation tests |
| `internal/retention/evaluator.go` | **New** — rule matching logic |
| `internal/retention/evaluator_test.go` | **New** — unit tests for rule priority, regex matching |
| `internal/retention/worker.go` | **New** — background worker with batch scan/delete |
| `internal/retention/worker_test.go` | **New** — unit test with mock store |
| `internal/store/evidence.go` | **Modified** — add `DeleteBatch`, `ScanAll` |
| `internal/store/inheritance.go` | **Modified** — add `AllSourceRefs` |
| `cmd/server/main.go` | **Modified** — wire up retention worker |
| `migrations/000002_drop_retention_policy_table.up.sql` | **New** — drop old table |
| `migrations/000002_drop_retention_policy_table.down.sql` | **New** — recreate old table |
| `tests/integration_test.go` | **Modified** — add retention integration tests |
| `retention.example.yaml` | **New** — example config at repo root |

### Testing strategy

- **Unit tests** for config parsing, evaluator rule matching (priority, regex, AND logic, edge cases).
- **Unit tests** for worker logic with mock store (verify pinning, inheritance exemption, batch delete).
- **Integration tests** using testcontainers: insert evidence with varying ages/branches, run `RunOnce`, verify correct records were deleted and exempt records survive.

### Not in scope

- API endpoints for CRUD on retention rules (config file is the source of truth for now).
- Audit table for deletions (can be added later; worker logs provide traceability).
- Blob/object storage cleanup (no blob store integration exists yet).
- Dry-run mode (good future addition — log what would be deleted without actually deleting).
