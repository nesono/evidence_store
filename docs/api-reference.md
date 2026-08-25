# HTTP API Reference

Full reference for `/api/v1`. For how to get a server running and how to
authenticate, see the [README](../README.md#setup-and-administration); for the
same operations from the web UI, see [Web interface](../README.md#web-interface)
in the README.

Base URL: `/api/v1`

## Endpoints

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
| `GET` | `/api/v1/weather` | Conditions at a point and an hour (see [Weather while a test ran](#weather-while-a-test-ran)) |
| `GET` | `/healthz` | Health check |

## Creating evidence

```bash
curl -X POST http://localhost:8000/api/v1/evidence \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "myorg/myrepo",
    "rcs_ref": "abc123",
    "procedure_ref": "//pkg:my_test",
    "evidence_type": "ci",
    "source": "ci",
    "result": "PASS",
    "finished_at": "2026-01-01T00:00:00Z"
  }'
```

Result must be one of: `PASS`, `FAIL`, `ERROR`, `SKIPPED`.

`evidence_type` must be one of three, and says **how** the evidence was collected — which is what tells a reader what its metadata means:

| Value | Meaning |
|---|---|
| `ci` | A machine ran it unattended: a pipeline, a watch mode, a developer's `bazel test`. |
| `manual_test` | A person carried out a procedure and reported what they saw. |
| `demonstration` | The thing was shown working — to a customer, an auditor, a room — rather than tested. |

Anything else is refused with `422` and an error naming the three, and the column
carries a `CHECK` so a bulk load or a seeding script cannot get around that. The
field was free text at first, which is how `bazel`, `pytest`, `gotest` and
`junit` ended up in it: four spellings of "a machine ran it" that no query could
treat as one thing. *Which* runner produced a record is a separate question with
its own answer in `metadata.collector`, which the Bazel adapter sets.

`finished_at` accepts RFC3339 (`2026-01-01T00:00:00Z`, `2026-01-01T12:00:00+02:00`) as well as shorter forms (`2026-01-01 14:00`, `2026-01-01`). Values without a timezone are interpreted as **UTC**. All timestamps are normalized to UTC on storage.

### Sending the same record twice

`client_record_id` is an optional UUID a client chooses for a **submission**.
It is not the record's `id` — the store still mints that — and a client that
does not send one is unaffected in every way.

It exists for one failure: the post that **succeeds while the response is
lost**. The store has the record, the client cannot tell, and sending again
files the event twice. Afterwards the two rows differ only in `id` and
`ingested_at`, which nothing can distinguish from a test that genuinely ran
twice and passed twice. So the client says instead:

```bash
curl -X POST http://localhost:8000/api/v1/evidence \
  -H 'Content-Type: application/json' \
  -d '{
    "client_record_id": "9f1c8b3e-2d4a-4f6b-8c1d-0e2f3a4b5c6d",
    "repo": "myorg/myrepo",
    "rcs_ref": "abc123",
    "procedure_ref": "//pkg:my_test",
    "evidence_type": "ci",
    "source": "ci",
    "result": "PASS",
    "finished_at": "2026-01-01T00:00:00Z"
  }'
```

| Outcome | Response |
|---|---|
| The store did not have it | `201 Created` with the new record |
| The store already had this `client_record_id` | `200 OK` with the record it became — nothing is created, and the stored record is not modified |
| Not a UUID | `422` naming the field |

The record comes back either way, `client_record_id` included, so a client
reconciling a queue can match what it sent against what the store holds.
Retrying is therefore safe to do indefinitely: it either files the record or
tells you which record you already filed.

In a batch, the field works per record and appears in each result:

```json
{"results": [
  {"index": 0, "id": "…", "status": "created"},
  {"index": 1, "id": "…", "status": "duplicate"},
  {"index": 2, "status": "error", "error": "…"}
]}
```

A batch of records the store already has is `201` with every result marked
`duplicate` — it is not a failed batch. `207 Multi-Status` still means at least
one record was rejected. A token repeated **within** one batch resolves the
same way as one repeated across two calls: the first is `created`, the rest
`duplicate`, and all of them name the same record.

Uniqueness is global, not per client. Two clients must not share a token, which
is why it has to be a UUID rather than a build number or a filename.

This is what makes offline collection safe to sync over a bad link — see
[docs/offline-support-plan.md](offline-support-plan.md).

## Test logs

A manual result is only as useful as the account of what the tester saw. That
account goes in `metadata.observations` as markdown:

```bash
curl -X POST http://localhost:8000/api/v1/evidence \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "myorg/myrepo",
    "branch": "main",
    "rcs_ref": "abc123",
    "procedure_ref": "manual/brake-check",
    "evidence_type": "manual_test",
    "source": "j.tester",
    "result": "PASS",
    "finished_at": "2026-01-01 14:00",
    "metadata": {
      "observations": "## Run 1\n\n1. Powered on the rig — all lights green\n2. Pressed the brake pedal — firm, no travel\n\nSaw `ERR_42` on screen 2, cleared after a restart."
    }
  }'
```

The web UI's **Add Result** tab has a **Test log** box for it, with a preview,
and the record dialog in **Search** renders the log alongside the record's
fields rather than leaving it as one long line inside the metadata dump.

Supported markdown: headings, ordered and unordered lists, blockquotes,
horizontal rules, fenced and inline code, bold, italic, links, and images of
blobs this store holds. Links are restricted to `http`, `https`, `mailto`, and
same-site targets; everything else in a log is escaped, never interpreted as
HTML.

`observations` is plain metadata, so it needs no special handling from any
client — the Bazel adapter's `--metadata` flag can carry one just as well.

## Images in test logs

Paste or drop an image into the **Test log** box and it is uploaded, referenced
from the log, and rendered in the record dialog. Images larger than about 1.5 MB
are downscaled in the browser first.

Images are stored by content: the name of a blob is the SHA-256 of its bytes.
The photo attached to a FAIL is therefore verifiable against its own name, the
same image uploaded twice costs one object, and moving the data between backends
is a copy rather than a migration — a reference names content, not a location.

```bash
# Upload. The body is the raw image; the Content-Type header is not trusted.
curl -X POST http://localhost:8000/api/v1/blobs \
  --data-binary @screenshot.png
# {"ref":"/api/v1/blobs/sha256:9f2b….png","digest":"sha256:9f2b…",
#  "content_type":"image/png","size":51231}

# Reference it from a log, and it renders as an image:
#   ![rig after step 3](/api/v1/blobs/sha256:9f2b….png)

curl http://localhost:8000/api/v1/blobs/sha256:9f2b….png
```

The references found in a log are also listed under `metadata.photo_uris`, so a
client can find a record's images without parsing markdown.

A log may embed PNG, JPEG, WebP and GIF, decided by sniffing the bytes rather
than by what the uploader claims. SVG is refused: it is markup, and markup can
carry script. An image pointing anywhere but this store renders as a link, never
as an embed, so reading a log never fetches from a third party.

**Where the bytes live.** `fs` (the default) keeps them in a directory, which is
enough to run the server from a checkout. `s3` stores them in any S3-compatible
object store; `docker compose up` runs MinIO and points the app at it. Select the
backend with `EVIDENCE_BLOB_BACKEND` — see
[Configuration](../README.md#configuration) in the README.

**Lifetime.** Because blobs are deduplicated they have no single owner, so they
are kept by reachability: an image is deleted once no record's log references it
any more. Retention deleting a record releases its references, and the sweep on
the next retention pass collects what has become unreachable. Images uploaded
but never filed — a form someone abandoned — are collected the same way, once
they are older than `EVIDENCE_BLOB_ORPHAN_GRACE_HOURS`.

Deleting evidence therefore removes its images unless another record still shows
them, and deletion is not immediate: it happens on the next sweep after the
grace period.

## Where a test was run

A manual result is made somewhere, and for a test on a rig, a vehicle or a
proving ground the place is part of what the record proves. It goes in
`metadata.location`:

```bash
curl -X POST http://localhost:8000/api/v1/evidence \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "myorg/myrepo",
    "branch": "main",
    "rcs_ref": "abc123",
    "procedure_ref": "manual/brake-check",
    "evidence_type": "manual_test",
    "source": "j.tester",
    "result": "PASS",
    "finished_at": "2026-01-01 14:00",
    "metadata": {
      "location": "52.51631, 13.37771",
      "location_accuracy_m": 12.5
    }
  }'
```

One field takes both a coordinate pair and a place name — `Lab 2, bay 4` is as
valid a value as `52.51631, 13.37771` — because a tester at a bench has the one
and a tester in a field has the other, and a field that insists on coordinates
is a field that gets left empty. The value is stored as written; the store never
parses, rounds or reorders it.

In the web UI's **Add Result** tab, **Locate** next to the field fills it with
this device's position, to five decimals (about a metre) and with the receiver's
own margin recorded alongside in `location_accuracy_m`. The margin is only filed
while the text is still the device's: correcting the field by hand makes it the
tester's account of the place, and a metre count from a reading nobody can see
any more would say more than the record knows. The button needs a secure context
(HTTPS, or localhost) and the browser's permission; without either, the field is
still typed in as normal.

The record dialog in **Search** shows the location with the record's own fields
rather than inside the metadata dump, and links a coordinate pair to a map. The
link is only followed when a reader clicks it — opening a record tells no map
service what is being read.

## Weather while a test ran

Braking distance on a wet surface is a different measurement from braking
distance on a dry one, and a record that does not say which cannot be compared
with the next one. What the sky was doing goes in
`metadata.weather_conditions`:

```bash
curl -X POST http://localhost:8000/api/v1/evidence \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "myorg/myrepo",
    "branch": "main",
    "rcs_ref": "abc123",
    "procedure_ref": "manual/brake-check",
    "evidence_type": "manual",
    "source": "j.tester",
    "result": "PASS",
    "finished_at": "2026-03-30 14:00",
    "metadata": {
      "location": "52.51631, 13.37771",
      "weather_conditions": "Overcast, 8.4 °C, wind 22 km/h, humidity 81%, precipitation 1.2 mm",
      "weather_observed_at": "2026-03-30T14:00:00Z"
    }
  }'
```

The store can look the conditions up rather than leaving them to be written from
memory hours later:

```bash
curl 'http://localhost:8000/api/v1/weather?lat=52.51631&lon=13.37771&at=2026-03-30T14:05:00Z'
```

```json
{
  "observed_at": "2026-03-30T14:00:00Z",
  "description": "Overcast",
  "temperature_c": 8.4,
  "relative_humidity": 81,
  "precipitation_mm": 1.2,
  "wind_speed_kph": 22,
  "summary": "Overcast, 8.4 °C, wind 22 km/h, humidity 81%, precipitation 1.2 mm"
}
```

`summary` is the line meant for the field. The lookup stores nothing: what ends
up on a record is what the tester accepted or wrote over. `observed_at` is the
hour the reading is for — hourly is the resolution weather models publish at, so
a reading is never the minute of the test — and it goes on the record as
`weather_observed_at` only while the line is still the service's. Editing the
line makes it the tester's account of the weather, which is the point of leaving
it editable: someone standing in a hailstorm the model put two valleys away has
to be able to say so, and an hour attached to their sentence would dress it up
as a reading nobody can go back and check.

In the web UI's **Add Result** tab, **Look up** next to the Weather field asks
about the coordinates in the Location field, or — when that holds a place name
like `Lab 2, bay 4`, or nothing — about this device's position. The time it asks
about is the record's own **Finished at**, so filing yesterday evening's run
gets yesterday evening's weather. The line is retained by **Submit & Add
Another**, since a burst of records is made under one sky, and cleared by
**Submit**, since the next record filled in on that form could be days later.

The request goes out from the server, never from the page: a form fill must not
hand a tester's position to a third party, and an operator needs one place to
see it, point it elsewhere, or stop it. The coordinates are coarsened to two
decimals (about a kilometre, well under the resolution of any weather model)
before they are passed on, so the service is not told which bench in the
building a test ran at. Setting `EVIDENCE_WEATHER_ENDPOINT` to an internal
service redirects the lookup; setting it to an empty value switches it off, and
the button then says so rather than doing nothing.

The default service is [Open-Meteo](https://open-meteo.com/), which needs no
account and no key — a feature that only works once somebody has signed up for
something is a feature most deployments never turn on. It keeps a window of a
few months around the present, so a lookup for a run outside that window comes
back saying so, and the conditions are typed in as normal.

## Querying evidence

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

**Query parameters:** `repo`, `branch`, `rcs_ref`, `ref`, `evidence_type`, `source`, `procedure_ref`, `result`, `finished_after`, `finished_before`, `tags`, `notes`, `limit`, `cursor`, `offset`, `sort`, `order`, `include_total`, `include_inherited`.

## Pagination and sorting

Two pagination modes are available, and they cannot be combined — passing both `cursor` and `offset` (or both `cursor` and `sort`) returns `400`.

- **Cursor** — keyset pagination over the default `ingested_at` ordering. Stable while records are being inserted and cheap at any depth, so it is the right choice for streaming an entire result set. `next_cursor` is returned while more records remain; `total` is never returned for cursor requests.
- **Offset** — `offset` skips N matching rows, giving addressable, shareable windows (`?offset=1000&limit=50`) and backwards navigation. Because a keyset cursor only describes a position in the default ordering, `sort` requires offset mode and suppresses `next_cursor`.

`sort` accepts `repo`, `branch`, `rcs_ref`, `procedure_ref`, `evidence_type`, `source`, `result`, `finished_at` and `ingested_at`; any other value returns `400`. `order` is `asc` (default) or `desc`. Results are always tie-broken by `id`, so consecutive offset windows neither repeat nor skip a record.

`total` (a `COUNT(*)` of all matching rows) is returned by default for non-cursor requests. Pass `include_total=false` on subsequent windows to skip the count once you have it.

## Inherited records

When `include_inherited` is on (the default) and both `repo` and `rcs_ref` are given, evidence resolved through an inheritance declaration is returned in a separate `inherited_records` field rather than in `records`. Inherited evidence is resolved outside the paginated window, so mixing it into `records` would make the window's length disagree with `limit` and its position disagree with `total`.

See [Inheriting test results between commits](../README.md#inheriting-test-results-between-commits) in the README for how to create a declaration.

## Regex filtering

Text filter fields support regex matching via a `~` prefix. Without the prefix, filters use exact matching (backwards-compatible).

```bash
# Exact match (default)
curl "http://localhost:8000/api/v1/evidence?branch=main"

# Regex match — all release branches
curl "http://localhost:8000/api/v1/evidence?branch=~^release/.*"

# Regex on multiple fields — everything a person filed, on org repos
curl "http://localhost:8000/api/v1/evidence?evidence_type=~^(manual_test|demonstration)$&repo=~^myorg/"

# Regex on tags — match any tag starting with "nightly-"
curl "http://localhost:8000/api/v1/evidence?tags=~^nightly-"

# Regex on notes
curl "http://localhost:8000/api/v1/evidence?notes=~device.*XYZ"
```

**Supported fields:** `repo`, `branch`, `rcs_ref`, `ref`, `evidence_type`, `source`, `procedure_ref`, `tags`, `notes`.

## Matching a branch, tag or commit with one filter

`ref` matches a value against *either* identity column, so a caller who has "the thing they are looking at" does not have to say first whether it is a branch, a tag or a commit:

```bash
# Any of these work
curl "http://localhost:8000/api/v1/analytics/tests?ref=main"
curl "http://localhost:8000/api/v1/analytics/tests?ref=v2.0.0"
curl "http://localhost:8000/api/v1/analytics/tests?ref=aaaa111"     # abbreviated SHA
curl "http://localhost:8000/api/v1/analytics/tests?ref=~^release/"  # regex, both columns
```

Commits match by prefix, so a pasted short SHA finds the full one it abbreviates. Branches and tags match whole — a prefix there would answer a request for `release/1.1` with `release/1.10` as well. `ref` is available on every endpoint that takes filters, including `/api/v1/evidence`.

It is what both the **Search** and **Analytics** tabs of the web UI put in their filter bar: one **Branch, tag or commit** box rather than separate fields, since a record has exactly one of each and filtering on a combination of them narrowed to nothing. `branch` and `rcs_ref` remain available to API callers who do want to name the column.

The regex engine is [PostgreSQL POSIX regular expressions](https://www.postgresql.org/docs/current/functions-matching.html#FUNCTIONS-POSIX-REGEXP) (the `~` operator). This supports the POSIX Extended Regular Expression syntax including character classes (`[a-z]`, `[[:digit:]]`), alternation (`a|b`), quantifiers (`*`, `+`, `?`, `{n,m}`), and anchors (`^`, `$`). Matching is case-sensitive.

## Creating an inheritance declaration

```bash
curl -X POST http://localhost:8000/api/v1/inheritance \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "myorg/firmware",
    "source_rcs_ref": "abc123def",
    "target_rcs_ref": "def456abc",
    "scope": ["//pkg:*"],
    "justification": "Impact analysis JIRA-1234: no changes in pkg/",
    "created_by": "ci-bot"
  }'
```

```bash
curl "http://localhost:8000/api/v1/inheritance?repo=myorg/firmware&target_rcs_ref=def456abc"
```

`repo`, `source_rcs_ref`, `target_rcs_ref`, `justification` and `created_by` are
required. Once the declaration exists, every record filed against
`source_rcs_ref` also shows up — marked `inherited: true`, with an
`inheritance_declaration_id` — when querying `target_rcs_ref` (see
[Inherited records](#inherited-records) above). `scope` and `justification` are
kept as the audit trail for *why*; the store does not currently use `scope` to
narrow which of the source commit's records are inherited, so querying the
target returns everything filed against the source.

Creating a declaration needs `inheritance:write`, which only the `admin` role
holds — see [Authentication and authorization](../README.md#authentication-and-authorization)
in the README.

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
| `group_by` | *(none)* | `evidence_type` splits a procedure into one row per collection method, for a procedure that is both run by CI and checked by hand |

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
A query that cannot finish within `EVIDENCE_ANALYTICS_QUERY_TIMEOUT_SECONDS` is refused the same way. Analytics scales with the rows scanned, so an unfiltered query over a long window on a large corpus can take tens of seconds; without a budget it runs until the server's request timeout and the caller gets a dropped connection rather than an answer. Scoping to a repo is what keeps these queries fast — a full year of one repo's history aggregates in about two seconds at six million rows.

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
