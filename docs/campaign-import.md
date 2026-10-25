# Importing campaign packages

Campaign archives can now be imported into the server, listed, inspected, and
downloaded, including through the [Campaigns tab](campaign-browser.md). The
server reuses the [package validator](campaign-package-format.md) before publishing
anything. Execution capture and product-revision associations follow separately.

## Import

Build a package using `campaign-package`, then upload its raw bytes:

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $EVIDENCE_STORE_API_KEY" \
  -H 'Content-Type: application/gzip' \
  --data-binary @campaign.tar.gz \
  "$EVIDENCE_STORE_URL/api/v1/campaigns/import"
```

The endpoint accepts `application/gzip` and `application/octet-stream`, not
multipart form data. The compressed-body limit is 16 MiB; the package validator's
expanded/file/control limits also apply. The existing API authentication,
rate limiting, and session CSRF checks apply. Import requires both
`evidence:write` and `blob:write` permissions.

- **201 Created:** new archive digest imported.
- **200 OK:** that exact archive was already imported, including concurrent
  uploads. The response returns the original ID, import time, and importer.
- **400:** interrupted/unreadable request body.
- **413:** compressed request exceeds 16 MiB.
- **415:** unsupported request Content-Type.
- **422:** archive or definition validation failure, with a diagnostic.
- **503:** blob storage unavailable.
- **500:** database or CAS failure. Retry the same archive safely.

The response has `id` (UUID), `digest`, `archive_size`, `imported_at`,
`imported_by`, and `definition` (the package validator's structured output,
including manifest, ordered scenarios, typed procedure blocks, and asset digest
maps). `Location` names `/api/v1/campaigns/{id}`. The authenticated subject is
recorded as importer; with authentication disabled it is `anonymous`. A caller
cannot supply an importer name through the archive or query parameters.

The original archive is stored byte-for-byte in the existing CAS backend.

Procedure selection uses `procedure_ref`, unique within a package. Format v2
has no separate scenario IDs. Legacy v1 imports remain accepted, with their
parsed definitions normalized to reference keys; migration 17 updates existing
rows likewise. Original archives, digests, and imported UUIDs stay unchanged.
Declared setup images are also stored individually by digest, so a shared image
occupies one CAS object even across campaigns and evidence. The parsed
definition is stored in PostgreSQL. IDs/grouping names inside the manifest do
not deduplicate imports: new bytes produce a new immutable package row.

## Read and list

`GET /api/v1/campaigns/{id}` returns the same persisted record as import and
requires `evidence:read`. An invalid UUID returns 400; an unknown ID returns 404.
There is no mutation or deletion endpoint in this slice.

`GET /api/v1/campaigns?limit=50&offset=0` returns:

```json
{
  "campaigns": [],
  "limit": 50,
  "offset": 0,
  "has_more": false
}
```

List rows contain `id`, `digest`, `archive_size`, `imported_at`, `imported_by`,
`manifest`, and `scenario_count`, omitting full procedure bodies. The default
limit is 50, maximum 100; offset is an integer from 0 to 1,000,000. Invalid or
repeated pagination values return 400. Ordering is by import time and then UUID,
ascending. Use `has_more` and advance the offset by the page limit. There is no
total-count query. Reads require `evidence:read`.

## Download and setup assets

`GET` or `HEAD /api/v1/campaigns/{id}/package` requires `evidence:read` and
`blob:read`. It serves the original bytes as `application/gzip` with attachment
filename `campaign.tar.gz`, `nosniff`, a digest ETag, and private immutable
caching. Conditional requests and byte ranges use normal HTTP semantics. A
package row whose archive cannot be read returns 500 rather than pretending the
campaign never existed.

Scenario `assets` maps in the parsed definition map archive-relative paths to
CAS digests. Fetch each setup image through the existing
`GET /api/v1/blobs/{digest}` endpoint, which enforces `blob:read` and detects its
media type from the bytes. Procedure Markdown keeps its original relative paths;
a future renderer will resolve them through that asset map. The existing media
upload endpoint remains media-only; archive download uses the dedicated route.

## Persistence and retention

Migration 16 adds `campaign_package` and `campaign_blob_ref`. A unique digest
constraint arbitrates duplicate imports. All CAS writes finish before one
transaction commits the package metadata and references to the archive and each
setup asset. Failed imports publish no package. Objects left behind by a failed
write/commit are ordinary unreferenced blobs and become eligible for sweeping
after the configured grace period. Do not delete them immediately: another
package or evidence record may use the same bytes.

Blob reachability is the union of evidence and campaign references. Imported
packages remain retained independently of evidence retention, including before
their first execution. No automatic campaign-expiry policy is introduced.

An import holds a shared PostgreSQL advisory transaction lock through CAS writes
and reference publication. Each sweep batch holds the matching exclusive lock
through its reachability check and deletions. This prevents a previously uploaded
orphan from being deleted as it becomes a campaign dependency, including across
multiple server/worker processes. Imports may run concurrently with each other.
All sweep workers must run the updated code before enabling campaign uploads;
older workers do not know about campaign references or this lock.

The package digest remains separate from the product revision as agreed in #200.
This API persists package content and import attribution; it does not claim to
establish build provenance or software deployment identity.
