# Evidence Store Backend — Design Document

## 1. Problem Statement

Software projects produce two fundamentally different categories of output:

- **Arguments** — code, deployment artifacts, analytical results (the *thing being tested*)
- **Evidence** — test reports, demonstrations, manual observations (proof that the *thing works*)

This document describes the backend for an **Evidence Store**: a system that ingests, stores, queries, and manages test evidence from heterogeneous sources (Bazel test logs, CI pipelines, manual test runs, HiL/PiL/vehicle tests) with a unified API.

### Key Requirements

| Requirement | Rationale |
|---|---|
| Evolving schema | Fields will be added over time without breaking existing records |
| Multiple schema types | Unit tests, integration tests, simulation, target tests, HiL, manual tests each carry different metadata |
| Short-term data at scale | Most evidence is transient; only selected records need long-term retention |
| Result inheritance | Impact analysis can declare that evidence from version A remains valid for version B |

---

## 2. Data Model

### 2.1 Core Fields (required on every evidence record)

Every ingested record MUST contain:

| Field | Type | Description |
|---|---|---|
| `id` | UUID | System-generated unique identifier |
| `repo` | string | Repository identifier (e.g. `myorg/firmware`, `myorg/tools`). Scopes `rcs_ref` and `procedure_ref` to a specific repository. |
| `finished_at` | datetime (UTC) | When the test finished |
| `rcs_ref` | string | Revision control identifier (commit hash, tag, etc.) within `repo` |
| `result` | enum | `PASS`, `FAIL`, `ERROR`, `SKIPPED` |
| `evidence_type` | string | Discriminator for the schema variant (see 2.2) |
| `procedure_ref` | string | Reference to the test procedure: a Bazel target (e.g. `//pkg:my_test`) or a repo-relative file path (e.g. `tests/integration/smoke.py`) |
| `source` | string | Provenance of the run: a URL to the CI build logs (e.g. Jenkins build URL) **or** the username of the developer who triggered the test locally |
| `ingested_at` | datetime (UTC) | When the record was stored (system-generated) |

### 2.2 Extended Fields (optional, type-dependent)

Extended fields live in a semi-structured `metadata` JSONB object. The store does not reject unknown fields — it preserves them opaquely, so new fields can be added at any time without migration.

Common optional fields:

| Field | Type | Description |
|---|---|---|
| `started_at` | datetime (UTC) | When the test started |
| `duration_s` | float | Duration in seconds |
| `log_uri` | URI | Link to full log in external storage |
| `tags` | string[] | Free-form labels for filtering |

Type-specific fields (examples):

**`target_test` / `hil_test`** — `target_hw_type`, `vehicle_id`, `hw_generation`, `weather_conditions`, `video_uris`, `attachments`

**`manual_test`** — `observations`, `photo_uris`

### 2.3 Result Inheritance

When an impact analysis determines that evidence from one RCS reference is still valid for another, the system creates an **inheritance record**:

```
InheritanceDeclaration {
  id:               UUID
  created_at:       datetime (UTC)
  repo:             string      -- which repository this applies to
  source_rcs_ref:   string      -- the version that was actually tested
  target_rcs_ref:   string      -- the version that inherits the results
  scope:            string[]    -- which test names / suites / types are inherited
  justification:    string      -- free-text rationale or link to impact analysis
  created_by:       string      -- user or system that made the declaration
}
```

Querying evidence for `target_rcs_ref` will include inherited results, clearly marked with `inherited: true` and a reference to the declaration.

---

## 3. Architecture Overview

```
                        +-----------------+
                        |   Ingestion API |  (REST / gRPC)
                        |   POST /evidence|
                        +--------+--------+
                                 |
                         validation &
                         normalisation
                                 |
                    +------------+------------+
                    |                         |
              +-----v------+          +------v-------+
              |  Evidence   |          |  Blob Store  |
              |  Database   |          |  (S3 / MinIO)|
              | (Postgres)  |          |  logs, video |
              +-----+------+          +--------------+
                    |
              +-----v------+
              |  Query API  |  (REST / gRPC)
              |  GET /evidence
              |  GET /evidence/{id}
              +-----+------+
                    |
              +-----v------+
              | Retention   |
              | Worker      |  (cron / async)
              +-------------+
```

### Component Responsibilities

| Component | Role |
|---|---|
| **Ingestion API** | Receives evidence records, validates required fields, normalises timestamps to UTC, stores metadata |
| **Evidence Database** | Structured storage for queryable fields + JSONB for extended metadata |
| **Blob Store** | Large artifacts (logs, videos, photos) stored externally; database holds URIs |
| **Query API** | Filtered, paginated access to evidence; supports inheritance resolution |
| **Retention Worker** | Applies retention policies; archives or deletes expired records |

---

## 4. Storage Design

### 4.1 Why PostgreSQL + JSONB

The core tension is **structured queries on required fields** vs. **evolving, type-dependent metadata**. PostgreSQL with JSONB columns handles both:

- Required fields are regular columns with types, indexes, and constraints.
- Extended metadata is a JSONB column, queryable via GIN indexes, with no schema migration needed when new fields appear.
- No document-database operational overhead.

### 4.2 Schema

```sql
CREATE TYPE evidence_result AS ENUM ('PASS', 'FAIL', 'ERROR', 'SKIPPED');

CREATE TABLE evidence (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo           TEXT NOT NULL,        -- repository identifier
    finished_at    TIMESTAMPTZ NOT NULL,
    rcs_ref        TEXT NOT NULL,
    result         evidence_result NOT NULL,
    evidence_type  TEXT NOT NULL,
    procedure_ref  TEXT NOT NULL,        -- bazel target or repo-relative path
    source         TEXT NOT NULL,        -- CI build URL or username
    ingested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- semi-structured extended fields
    metadata       JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_evidence_repo           ON evidence (repo);
CREATE INDEX idx_evidence_rcs_ref        ON evidence (repo, rcs_ref);
CREATE INDEX idx_evidence_finished_at    ON evidence (finished_at);
CREATE INDEX idx_evidence_type           ON evidence (evidence_type);
CREATE INDEX idx_evidence_result         ON evidence (result);
CREATE INDEX idx_evidence_procedure_ref  ON evidence (procedure_ref);
CREATE INDEX idx_evidence_source         ON evidence (source);
CREATE INDEX idx_evidence_metadata       ON evidence USING GIN (metadata);

CREATE TABLE inheritance_declaration (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    repo            TEXT NOT NULL,
    source_rcs_ref  TEXT NOT NULL,
    target_rcs_ref  TEXT NOT NULL,
    scope           JSONB NOT NULL DEFAULT '[]',
    justification   TEXT NOT NULL,
    created_by      TEXT NOT NULL
);

CREATE INDEX idx_inheritance_target ON inheritance_declaration (repo, target_rcs_ref);
CREATE INDEX idx_inheritance_source ON inheritance_declaration (repo, source_rcs_ref);

-- Retention policy table
CREATE TABLE retention_policy (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    evidence_type   TEXT,          -- NULL = applies to all types
    max_age_days    INT NOT NULL,
    keep_failures   BOOLEAN NOT NULL DEFAULT true,
    priority        INT NOT NULL DEFAULT 0
);
```

### 4.3 Schema Evolution Strategy

Adding new metadata fields requires **no migration** — they are simply included in the JSONB payload. If a new field becomes important enough for direct indexing:

1. Add a generated column or a regular column.
2. Backfill from existing JSONB data.
3. Add an index.

This is a routine `ALTER TABLE`, not a schema redesign.

---

## 5. API Design

### 5.1 Ingestion

```
POST /api/v1/evidence
Content-Type: application/json

{
  "repo": "myorg/firmware",
  "finished_at": "2026-03-08T14:23:00Z",
  "rcs_ref": "abc123def",
  "result": "PASS",
  "evidence_type": "unit_test",
  "procedure_ref": "//pkg:my_test",
  "source": "https://jenkins.example.com/job/nightly/42",
  "metadata": {
    "started_at": "2026-03-08T14:22:50Z",
    "duration_s": 10.0,
    "log_uri": "s3://evidence-logs/abc123/my_test.log",
    "tags": ["nightly", "x86_64"]
  }
}
```

Developer workstation example:

```json
{
  "repo": "myorg/firmware",
  "finished_at": "2026-03-08T16:05:12Z",
  "rcs_ref": "e7f2a91",
  "result": "FAIL",
  "evidence_type": "unit_test",
  "procedure_ref": "//pkg:my_test",
  "source": "jdoe",
  "metadata": {
    "duration_s": 3.2
  }
}
```

**Response:** `201 Created` with the stored record including `id` and `ingested_at`.

**Batch ingestion** (for CI pipelines producing many results at once):

```
POST /api/v1/evidence/batch
Content-Type: application/json

{
  "records": [ ... ]   // array of evidence objects, max 1000
}
```

**Response:** `201 Created` with array of `{id, status}` per record. Partial failures return `207 Multi-Status`.

### 5.2 Query

```
GET /api/v1/evidence?repo=myorg/firmware&rcs_ref=abc123def&result=FAIL
```

Query parameters (all optional, combined with AND):

| Parameter | Type | Description |
|---|---|---|
| `repo` | string | Exact match |
| `rcs_ref` | string | Exact match |
| `evidence_type` | string | Exact match |
| `result` | string | Exact match or comma-separated list |
| `source` | string | Exact match (CI URL or username) |
| `procedure_ref` | string | Exact or prefix match (`//pkg:*`) |
| `finished_after` | datetime | Lower bound |
| `finished_before` | datetime | Upper bound |
| `tags` | string | Comma-separated; all must be present |
| `include_inherited` | bool | Default `true`. Resolve inheritance declarations. |
| `limit` | int | Page size (default 100, max 1000) |
| `cursor` | string | Opaque cursor for pagination |

**Single record:**

```
GET /api/v1/evidence/{id}
```

### 5.3 Inheritance

```
POST /api/v1/inheritance
{
  "repo": "myorg/firmware",
  "source_rcs_ref": "abc123def",
  "target_rcs_ref": "def456abc",
  "scope": ["//pkg:*", "evidence_type:integration_test"],
  "justification": "Impact analysis JIRA-1234: no changes in pkg/",
  "created_by": "ci-bot"
}
```

```
GET /api/v1/inheritance?repo=myorg/firmware&target_rcs_ref=def456abc
```

### 5.4 Blob Upload (optional convenience endpoint)

For sources that want the evidence store to host artifacts rather than providing pre-existing URIs:

```
POST /api/v1/blobs
Content-Type: multipart/form-data

file: <binary>
```

**Response:** `201 Created` with `{ "uri": "s3://evidence-blobs/..." }`. The returned URI can then be used in evidence metadata fields.

---

## 6. Retention and Lifecycle

### 6.1 Policy Model

Retention policies are stored in the `retention_policy` table and evaluated by the retention worker.

Default policy: **delete evidence older than 90 days**, except:

- Records with `result = FAIL` are kept for 1 year (failures are more valuable for trend analysis).
- Records referenced by an active `inheritance_declaration` are never auto-deleted.
- Records can be explicitly pinned (`metadata.retain = true`).

### 6.2 Retention Worker

Runs as a periodic job (daily). For each policy, in priority order:

1. Select candidate records matching `evidence_type` and age threshold.
2. Exclude pinned records and records with active inheritance references.
3. Delete associated blobs from object storage.
4. Delete database records.

Deletions are logged to an audit table for traceability.

---

## 7. Ingestion Adapters

The API is the single point of entry. Adapters run outside the backend and translate native formats into API calls. They are separate projects — the backend only defines the API contract.

| Adapter | Input | `source` value | Runs where |
|---|---|---|---|
| **Bazel (CI)** | Bazel test XML (`test.xml`) | Jenkins/CI build URL | Post-test CI step |
| **Bazel (local)** | Bazel test XML | Developer username (from env/git config) | Developer workstation, post `bazel test` hook or wrapper script |
| **JUnit (generic)** | JUnit XML | CI build URL | Any CI system |
| **Manual test CLI** | Interactive prompts | Username of tester | Tester workstation |

### Developer Workstation Ingestion

Developer-local test runs are a first-class ingestion source. This enables:

- **Failure rate analysis** — identify flaky tests across CI and local runs.
- **Test set optimisation** — correlate which tests developers run locally vs. what fails in CI.
- **Pre-commit evidence** — capture test results before code even reaches CI.

The local adapter determines `rcs_ref` from the current HEAD commit (or working-tree state hash for uncommitted changes) and sets `source` to the developer's username. It can run as a Bazel `--build_event_json_file` post-processor or a thin wrapper around `bazel test`.

---

## 8. Authentication and Authorization

| Concern | Approach |
|---|---|
| **API authentication** | API keys for CI clients; OAuth2/OIDC for human users and developer workstations |
| **Write access** | CI keys can write any `source` (typically the build URL). Human tokens are bound to the authenticated username — the server enforces `source` matches the token identity. |
| **Read access** | All authenticated clients can read all evidence |
| **Admin operations** | Retention policy changes, inheritance declarations require elevated role |

---

## 9. Deployment Considerations

### 9.1 Minimal Deployment

For initial use:

- Single PostgreSQL instance (managed, e.g. Cloud SQL / RDS).
- Single backend service instance (stateless, horizontally scalable).
- S3-compatible object storage for blobs.
- Retention worker as a cron job or Kubernetes CronJob.

### 9.2 Scaling Path

- **Read-heavy:** Add read replicas for the query API.
- **Write-heavy:** The batch endpoint already minimises round-trips. Partitioning `evidence` by `finished_at` (monthly ranges) keeps indexes fast and simplifies retention (drop old partitions).
- **Blob storage:** S3/MinIO scales independently.

### 9.3 Table Partitioning (when needed)

```sql
CREATE TABLE evidence (
    ...
) PARTITION BY RANGE (finished_at);

CREATE TABLE evidence_2026_q1 PARTITION OF evidence
    FOR VALUES FROM ('2026-01-01') TO ('2026-04-01');
```

Retention becomes `DROP TABLE evidence_2025_q1` — instantaneous, no row-by-row deletion.

---

## 10. Open Questions

| # | Question | Impact |
|---|---|---|
| 1 | Should inheritance declarations expire, or are they permanent until manually revoked? | Affects retention logic |
| 2 | Is there a need for real-time streaming of ingested evidence (e.g. WebSocket/SSE for dashboards)? | Affects architecture (adds event bus) |
| 3 | Should the store enforce a known list of `evidence_type` values, or accept any string? | Strictness vs. flexibility trade-off |
| 4 | What is the expected peak ingestion rate (records/sec)? | Determines whether batch ingestion alone suffices or a queue (Kafka/NATS) is needed in front |
| 5 | Are there compliance requirements for evidence immutability (e.g. records must never be mutated after ingestion)? | Affects update/delete API surface |
