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
| Multiple schema types | Different collection methods (Bazel, manual, etc.) carry different metadata |
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
| `branch` | string | Branch name the test was run against (e.g. `main`, `feature/foo`) |
| `rcs_ref` | string | Revision control identifier (commit hash, tag, etc.) within `repo` |
| `procedure_ref` | string | Reference to the test procedure: a Bazel target (e.g. `//pkg:my_test`) or a repo-relative file path (e.g. `tests/integration/smoke.py`) |
| `evidence_type` | enum | How the evidence was collected, which determines the metadata schema: `ci`, `manual_test`, `demonstration` (see 2.2) |
| `source` | string | Provenance of the run: a URL to the CI build logs (e.g. Jenkins build URL) **or** the username of the developer who triggered the test locally |
| `result` | enum | `PASS`, `FAIL`, `ERROR`, `SKIPPED` |
| `finished_at` | datetime (UTC) | When the test finished |
| `ingested_at` | datetime (UTC) | When the record was stored (system-generated) |

**Result enum semantics:**

| Value | Meaning |
|---|---|
| `PASS` | The test executed successfully and all assertions held. Constitutes positive evidence for the predicate under test. |
| `FAIL` | The test executed to completion but one or more assertions did not hold. Constitutes negative evidence — the predicate was not satisfied. |
| `ERROR` | The test infrastructure failed before the test could produce a meaningful result (e.g. build failure, environment setup crash, hardware unavailable). No evidence — positive or negative — was created for the predicate. |
| `SKIPPED` | The test was deliberately not executed (e.g. filtered out by tag, disabled by configuration, not applicable to the current target). No evidence was created. Cached tests should not show up as SKIPPED. |

### 2.2 Extended Fields (optional, type-dependent)

Extended fields live in a semi-structured `metadata` JSONB object. The store does not reject unknown fields — it preserves them opaquely, so new fields can be added at any time without migration.

The `evidence_type` reflects **how the evidence was collected** (which determines the metadata shape), not what kind of test it is. A Bazel-run unit test and a Bazel-run HiL system test produce the same output format — they share the same `evidence_type`. The test category (unit, integration, system, etc.) is a property of the test procedure itself, expressible via `tags` or derivable from `procedure_ref`.

There are exactly three, and the API rejects anything else (a `CHECK` constraint keeps the column from drifting away from that):

| Value | Meaning |
|---|---|
| `ci` | A machine ran it unattended — a pipeline, a watch mode, a developer's `bazel test`. Nobody was watching it happen. |
| `manual_test` | A person carried out a procedure and reported what they saw. |
| `demonstration` | Evidence produced by showing the thing working — to a customer, an auditor, a room — rather than by testing it. |

The field was free text at first, which produced `bazel`, `pytest`, `gotest` and `junit` in the same column: four spellings of "a machine ran it" that no query could treat as one thing, and that a reader could not tell apart from a category of test. **Which** runner produced a record is a different question and keeps its own answer in `metadata.collector`, where it does not have to carry the meaning of the type as well.

Common optional fields (any type):

| Field | Type | Description |
|---|---|---|
| `started_at` | datetime (UTC) | When the test started |
| `duration_s` | float | Duration in seconds |
| `log_uri` | URI | Link to full log in external storage |
| `tags` | string[] | Free-form labels for filtering |
| `collector` | string | What produced the evidence, where the type does not say it: `bazel`, `pytest`, a rig's name. Set by the collecting client |
| `target_hw_type` | string | Hardware target type (e.g. `hil`, `pil`, `vehicle`) |
| `vehicle_id` | string | Vehicle identifier |
| `hw_generation` | string | Hardware generation identifier |

**`ci`** type — additional fields:

| Field | Type | Description |
|---|---|---|
| `invocation_id` | string | Groups all test results from a single run — e.g. one `bazel test` call |
| `result_was_cached` | bool | Whether the result was served from cache rather than executed |

**`manual_test`** and **`demonstration`** types — additional fields:

| Field | Type | Description |
|---|---|---|
| `observations` | string | Free-text observations from the tester |
| `photo_uris` | URI[] | Links to photos or screenshots. Images embedded in `observations` are listed here automatically (see 4.4) |
| `location` | string | Where the test was run, as written: a decimal `lat, lon` pair or a place name |
| `location_accuracy_m` | float | Radius in metres the fix is good to. Present only when `location` came from the device's receiver and was not edited afterwards |
| `weather_conditions` | string | Weather conditions during the test, as written: a line fetched for the record's place and hour, or the tester's own account |
| `weather_observed_at` | timestamp | The hour a fetched reading was for. Present only when `weather_conditions` came from the weather service and was not edited afterwards |
| `video_uris` | URI[] | Links to video recordings |

`weather_conditions` is text for the same reason `location` is: the tester may correct it. A weather model is a model, and someone standing in a hailstorm it put two valleys away has to be able to say so — a structured reading they cannot argue with would file the model's account over theirs. `weather_observed_at` is what separates the two afterwards: a reading is for an hour, which is the resolution weather models publish at and never the minute of the test, and a line without an hour beside it was written by the person who was there.

The reading is fetched by the server, not by the browser (§4.5), and the client sends only the coordinates and the moment the record already names.

`location` is one string and not a structured point, because the two things a tester has to record are not the same shape: someone on a proving ground has a fix from the device, someone at a bench has "Lab 2, bay 4", and a schema that demands coordinates gets an empty field from the second. Clients that need a point parse the pair back out; a value that is not one is a place name and was never meant to be a point. The store itself does not read the field — a value that came back rounded or reordered would be a claim about the place that the tester never made.

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
| **Blob Store** | Large artifacts (logs, videos, photos) stored outside the database, which holds references. Content-addressed; see 4.4 |
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
    branch         TEXT NOT NULL,
    result         evidence_result NOT NULL,
    -- a closed set, but TEXT with a CHECK rather than an enum type: adding a
    -- fourth collection method should be one migration, not an ALTER TYPE that
    -- every reader of the column has to be redeployed around
    evidence_type  TEXT NOT NULL
                   CHECK (evidence_type IN ('ci','manual_test','demonstration')),
    procedure_ref  TEXT NOT NULL,        -- bazel target or repo-relative path
    source         TEXT NOT NULL,        -- CI build URL or username
    ingested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- a token the client chooses for a submission, so that sending the same
    -- one twice files one record. Optional, and nothing that omits it is
    -- affected: see 5.1
    client_record_id UUID,

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

-- Partial: the column stays free for every client that sends no token.
CREATE UNIQUE INDEX idx_evidence_client_record_id
    ON evidence (client_record_id) WHERE client_record_id IS NOT NULL;

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

### 4.4 Blob Storage

Images embedded in a manual test log — and, later, videos — are stored outside the database and referenced from it. Blobs are **content-addressed**: a blob is named by the SHA-256 of its bytes and by nothing else.

```
POST /api/v1/blobs            -> {"ref": "/api/v1/blobs/sha256:<hex>.png", ...}
GET  /api/v1/blobs/sha256:<hex>[.ext]
```

The reference written into a log is relative and carries no location, only content. Three consequences follow, and they are the reason for the choice:

- **Evidence is verifiable.** The name of a photo attached to a FAIL is a checksum of that photo.
- **Moving the data is a copy.** Backends can be swapped, or a bucket migrated, without rewriting a single stored log, and a half-finished copy is safe to re-run.
- **Deduplication is free.** The same screenshot filed against two records is one object.

The extension on a reference is a rendering hint only: it tells a client whether to build an image or (with #79) a video element, and the media type actually served is sniffed from the bytes. What may be embedded is an allowlist — PNG, JPEG, WebP, GIF — never the uploader's declared type.

**Backends.** `fs` stores blobs in a directory; `s3` stores them in any S3-compatible object store. Which is in use is invisible above the storage layer.

**Lifetime.** Deduplication means a blob has no owner, so it cannot be deleted with a record. Reachability governs instead:

```sql
CREATE TABLE blob_ref (
    digest      TEXT NOT NULL,
    evidence_id UUID NOT NULL REFERENCES evidence(id) ON DELETE CASCADE,
    PRIMARY KEY (digest, evidence_id)
);
```

References are extracted from `observations` when a record is ingested and written in the same transaction; they are also mirrored into `metadata.photo_uris` so clients need not parse markdown. Retention deleting a record releases its references by cascade, and a sweep on the retention worker's schedule deletes objects no reference names. Objects younger than a grace period are spared: between an upload and the record being filed, a blob is legitimately unreachable.

The alternative — deriving reachability from `metadata` on every sweep — needs no table but scans the evidence table each pass, which is the wrong shape for the volumes this store targets.

### 4.5 Weather Lookup

`weather_conditions` (§2.2) can be filled from a weather service for the place and hour the record already names. Nothing is stored by the lookup: it answers with a line for the field, and what is filed is whatever the tester accepted or wrote over it.

```
GET /api/v1/weather?lat=<deg>&lon=<deg>&at=<RFC 3339>
 -> {"observed_at": "...", "description": "...", "temperature_c": 18.7, ..., "summary": "..."}
```

**The server makes the request, not the browser.** The page asking a weather service directly would hand a tester's position to a third party on every form fill, and would leave an operator with no way to see or sever that. Here there is one host to allow through a firewall, one place to point at an internal service, and one setting — `EVIDENCE_WEATHER_ENDPOINT`, emptied — that turns it off. The deployments that most need weather on a test record are proving grounds behind a firewall, so being able to redirect or refuse the traffic is not a footnote.

**The point is coarsened to two decimals before it is passed on**, about a kilometre. That is well under the resolution of any weather model, and coarser than the record's own five decimals, so the service is not told which bench in the building a test ran at.

**Three outcomes, three answers.** A reading is `200`. No reading for that place and hour — a date outside the window the service keeps — is `404`, carrying the service's own account of why, because "out of allowed range from … to …" tells a tester what to do next in a way "unavailable" cannot. An unreachable or broken upstream is `502`, and its detail goes to the server log: a tester can do nothing with a socket error, and the field is one they can still type into. The lookup switched off is `503` saying so, because a form whose button silently does nothing is worse than one that says the button is off here.

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
  "evidence_type": "ci",
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
  "evidence_type": "ci",
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

**Retrying safely.** A record may carry an optional `client_record_id` — a UUID the client chooses for the *submission*, not for the record. Sending the same one twice files one record: the repeat is answered `200 OK` with the record it became rather than `201`, and in a batch its status is `duplicate` rather than `created`.

This exists for the one failure a bad link actually produces: the post that succeeds while the response is lost. The store has the record, the client cannot tell, and sending again files the event twice — after which the two rows differ only in `id` and `ingested_at`, which is indistinguishable from a test that genuinely ran twice and passed twice. Nothing in the data can separate them, so the client has to say. Clients that send no token are unaffected in every respect.

Offline collection ([docs/offline-support-plan.md](docs/offline-support-plan.md)) makes that the expected case rather than the rare one: a queue drains over whatever link a test campaign can find, and a batch that loses its response leaves every record in it in doubt at once.

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

### Collecting Without a Connection

Test campaigns happen at proving grounds, workshops and tracks, where there is
often no route back to the store. The web UI is therefore an adapter as much as
a front end: it can collect evidence with nothing to send it to, and file it
later.

The pieces that make it work are all decisions taken elsewhere in this document,
used differently rather than added to:

- **A record's identity is a client's to choose** (§5.1). `client_record_id`
  lets a queue be retried over a bad link without filing anything twice.
- **A blob is named by its bytes** (§4.4). The browser can compute a photo's
  final reference offline, so a test log written in a field is finished when it
  is written, and only the bytes are still owed.
- **References are extracted server-side at ingest** (§4.4), so a log authored
  offline needs no special handling on arrival.
- **`weather_conditions` is text, and the hour is filed only for a reading**
  (§2.2). A tester with no connection writes down what they can see; a record
  that named a point and no sky can still gain a reading before it is filed.

Nothing about the archive is available offline — search, analytics and the
suggestion lists all need the store, and a record answered from a browser cache
would be a claim about the archive that a reader could not tell from a live one.

[docs/offline-support-plan.md](docs/offline-support-plan.md) has the full
design, including what is deliberately left out: a portable export bundle for
sites that never get a signal, and an offline path for the Bazel adapter.

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

The table above is the target. [docs/rbac-design.md](docs/rbac-design.md) works
it out in detail — principals, a closed permission set, the four roles, and the
schema — and marks the slot that SSO/SAML (#15) fills in.

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
| 3 | ~~Should the store enforce a known list of `evidence_type` values, or accept any string?~~ **Resolved:** three values — `ci`, `manual_test`, `demonstration` — enforced by the API and a `CHECK` (see 2.2). Which runner produced a record moved to `metadata.collector`. | Settled |
| 4 | What is the expected peak ingestion rate (records/sec)? | Determines whether batch ingestion alone suffices or a queue (Kafka/NATS) is needed in front |
| 5 | Are there compliance requirements for evidence immutability (e.g. records must never be mutated after ingestion)? | Affects update/delete API surface |
