# Offline Support — Implementation Plan

Closes #74.

## Problem

A test campaign happens where the tests are: a proving ground, a workshop, a
vehicle on a track. Those places have bad internet or none. Today the only way
to file a manual test result is the Add Result tab, which posts to the server
the moment the tester presses Create — so on a campaign the store is simply
unavailable, and the evidence is written on paper or in a phone's notes app and
transcribed days later, if at all. Transcribed evidence loses the two things
that made it worth collecting on site: the photos, and the timestamps.

## Requirements (from issue)

- Collect test results **without being connected** to the central backend.
- Get the collected data **into the central backend later**.

Scope agreed before writing this: the **web UI** is the collection path that
goes offline, and the **same device** that captured the records syncs them when
it regains connectivity. The Bazel adapter and a portable export bundle are
out of scope — see [Not in scope](#not-in-scope).

## Existing State

| Piece | Where | Offline today |
|---|---|---|
| Add Result form | `web/static/app.js` `submitEvidence` | Posts once; a network failure prints "Network error" and the record is gone |
| Image attach | `web/static/images.js` | Uploads on paste/drop, waits for the server's `ref`, writes it into the log |
| Blob storage | `internal/blob`, `internal/api/blobs.go` | Content-addressed: a blob is named by the SHA-256 of its bytes and nothing else |
| Ref extraction | `internal/store/blobref.go` | Server-side at ingest — refs are read out of `observations` and mirrored to `metadata.photo_uris` |
| Ingest | `POST /api/v1/evidence`, `/evidence/batch` | No idempotency of any kind; a retried post files a second record |
| Auth | `internal/auth` | Session cookie, `EVIDENCE_SESSION_TTL_HOURS` default 12 |
| Attribution | `internal/auth/source.go` `BindSource` | A human principal may only write evidence whose `source` is their own subject |
| App shell | `web/static/index.html` | Loads Pico from `cdn.jsdelivr.net`; polls `/healthz` every 5 s |

Three of these are load-bearing for the design and worth stating plainly:
content addressing means an image's name can be computed without a server;
ref extraction is server-side, so a log written offline needs no rewriting on
arrival; and there is no idempotency, which a flaky link will find immediately.

## Design

### What goes offline, and what cannot

Offline: the page itself, the whole Add Result form, image attach, the device's
location fix, the local template list, the weather field written down by hand,
and the queue.

Not offline, and honest about it: the **weather lookup** (a server call by
design — DESIGN.md §4.5 — so the browser never hands a tester's coordinates to a
third party; the field itself is still fillable, see below), the datalists that
suggest previously-seen repos and procedures, and Search and Analytics, which
are questions about data that is not here.

### Weather: written down on site, or looked up afterwards

The weather field is not simply lost offline. It gets both halves of what
DESIGN.md §2.2 already distinguishes — a line the tester wrote, and a reading
nobody can argue with — and the difference stays visible afterwards.

**Written down on site.** The tester is standing in the weather; they do not
need a server to know it is 6 °C and blowing. Offline, the **Look up** button is
replaced by **Write it down**, which opens a few small optional inputs —
conditions, temperature, wind, precipitation — that compose into one line and
then get out of the way. The composed text is fully editable, and it is what is
filed.

The format is not invented for this. `Reading.Summary()` in
`internal/weather/weather.go` already defines the canonical shape of the field,
and the composer emits the same order and the same units:

```
Light rain, 6 °C, wind 24 km/h, humidity 81%, precipitation 0.4 mm
```

So a line typed on a hillside and a line fetched from the service read alike,
filter alike, and can be compared across a campaign that had signal on some
days and not others. The composer is a keyboard convenience over a text field,
not a schema: a tester who wants to write "sleet, gusting hard across the
straight" types that instead, as the field has always allowed.

A written line gets **no** `weather_observed_at`. That is the existing rule —
the hour is filed only while the line is still the service's — and it is what
lets a reader tell, months later, which records carry a reading and which carry
a person's account.

**Looked up afterwards.** The lookup takes a point and an hour, and a queued
record already carries both, so a reading fetched later is still a reading for
the hour the test ran in. `weather_observed_at` keeps its meaning, so a record
synced on Friday for a test run on Tuesday is not passed off as having been
checked live.

*Corrected while building this (phase 5).* The plan had the outbox **offer**
this per record, before sending. Building it showed that offer is unreachable:
sync is automatic, so the queue drains the moment a signal appears — usually
with nobody looking at the page — and a filed record is immutable, so the chance
would be gone for good. The lookup therefore happens during the sync itself,
which is the only moment it can still happen.

It is bounded to what cannot misrepresent anybody: a record with no weather line
at all, and a point it already names. A tester's own words are never replaced,
what gets filled in carries `weather_observed_at` so it reads as the fetched
reading it is, and a lookup that fails changes nothing and the record goes
anyway — weather is not worth holding evidence back for. The outbox keeps a
manual **Look up weather** button for records that stay in the queue, which is
where the original offer still applies.

One bound, and the plan does not paper over it: the default endpoint
(`https://api.open-meteo.com/v1/forecast`) keeps a recent window — weeks, not
years — and a request outside it already comes back as a `404` carrying the
service's own "out of allowed range from … to …". The outbox surfaces that
message as-is and falls back to **Write it down**, which is the same answer the
online form already gives. An operator who needs older readings points
`EVIDENCE_WEATHER_ENDPOINT` at an archive service; that setting exists and
nothing here changes it.

The practical guidance for a campaign, which belongs in the README: write the
weather down when you file the record. It costs a few seconds while you are
standing in it, and it is the only version that is certainly available later.

### Sending the same record twice

First, what this is not: it is **not** the record's `id`. The store still mints
that, exactly as it does today, and a client never chooses it. Nothing offline
references a record by id, searches for one, or links to one — the queue is a
list of things not yet filed, and a thing not yet filed has no id to need.

What this is about is a narrower problem, and it is the one a bad link actually
produces. Not "the post failed" — that one is easy, the record stays queued and
goes next time. The awkward one is **the post succeeded and the response never
came back**: the tunnel, the timeout, the lid closing. The server has the
record. The client has no idea. If it drops the record it may have thrown away
the only copy of a FAIL with three photos on it; if it sends again it files the
event twice, which then skews every fail-rate the analytics tab computes.

Patching it up on conflict is the natural next thought, and it does not work,
because **there is no conflict to detect**. The second post is not rejected by
anything — it is a valid record, and the store dutifully files it. Afterwards
the two rows are identical but for `id` and `ingested_at`, and nothing in them
distinguishes "this was sent twice" from "the tester ran the procedure twice and
it passed twice", which on a campaign is an ordinary thing to do. The server
cannot tell, and neither can a human reading it later. A batch makes it worse:
lose one response for twelve records and all twelve are in that state at once.

So the client mints a token for each record when the tester presses Create, and
sends it with every attempt. It means only "this is the same submission as
before, not a second one":

```json
{
  "client_record_id": "9f1c…",
  "repo": "myorg/firmware",
  "…": "…"
}
```

Nullable, with a partial unique index, so every existing client is untouched and
anything that does not send one behaves exactly as it does now:

```sql
ALTER TABLE evidence ADD COLUMN client_record_id UUID;
CREATE UNIQUE INDEX idx_evidence_client_record_id
    ON evidence (client_record_id) WHERE client_record_id IS NOT NULL;
```

Insert becomes `ON CONFLICT (client_record_id) WHERE client_record_id IS NOT
NULL DO NOTHING`, followed by a read of the existing row when nothing came back
— the same shape `internal/store/principal.go` already uses for `external_id`.
A repeated single post answers **200** with the stored record rather than
**201**; a repeated record inside a batch gets status `duplicate` rather than
`created`. Either way the client learns which record its submission turned into
and can drop it from the queue for the right reason.

Uniqueness is global rather than per-principal. A caller who guessed another
client's token would learn that a record exists — which discloses nothing, since
every authenticated caller may already read every record (DESIGN.md §8).

**The alternative, if you would rather not add the column.** The client could
reconcile instead: after a lost response, query
`GET /evidence?repo=…&procedure_ref=…&finished_after=…` and match what came back
against what it sent. That needs no migration and no server change at all. Two
things are worse about it. It is ambiguous in exactly the case above — two
genuine runs of the same procedure, same verdict, same minute, cannot be told
from one duplicate — so it guesses where the token knows. And it puts a
reconciliation query on the wire at the moment the link is least able to carry
one. Against that, the column costs one migration, one index, and about twenty
lines in the store, and it protects every other client too: the Bazel adapter
retrying a flaky upload gets the same guarantee for free. That is why the plan
recommends it, but the reconcile path is real and the phases are ordered so
that dropping phase 1 does not block the rest.

### An image's reference is settled at capture too


This is the part that falls out of a decision already made. A blob is named by
the SHA-256 of its bytes and by nothing else, so the browser can compute the
final reference with `crypto.subtle.digest` and no server:

```
![rear bumper](/api/v1/blobs/sha256:3b1f…c7.jpg)
```

That is not a placeholder to be rewritten later. It is the reference the upload
would have returned, and the log is finished the moment the tester writes it.
The bytes go into local storage keyed by their digest; the sync uploads them;
`POST /blobs` is idempotent by construction, so an upload interrupted halfway
through a campaign's photos costs nothing to repeat. Server-side extraction
then wires up `blob_ref` and `metadata.photo_uris` at ingest exactly as it does
for a record filed online — no client work, no new endpoint.

Two consequences to get right. The media type must be sniffed from the magic
bytes in the browser, mirroring the PNG/JPEG/WebP/GIF allowlist in
`blob.DetectMedia`, because the extension on the ref is written offline and the
server will sniff the same bytes later — the two must not disagree. And the
hash must be taken **after** the existing downscale step, since the downscaled
bytes are what gets uploaded.

`crypto.subtle` requires a secure context, and so does a service worker. **This
feature requires the deployment be served over HTTPS** (localhost excepted).
Worth stating in the README, because the sites most likely to want offline
collection are also the ones most likely to be running plain HTTP on an
internal address.

### The outbox

One IndexedDB database, two stores: `records` (the queued evidence, plus the
subject who captured it and when) and `blobs` (bytes by digest, with a
reference count).

A record enters the outbox when the tester presses Create and the post fails,
or when the browser already knows it is offline. It leaves only when the server
has confirmed it. There is no third state where the page has forgotten a record
the store never received.

The header carries a pending count, which is also the offline indicator — the
`/healthz` poll every 5 seconds becomes an online/offline state rather than a
stream of console errors on a train.

The outbox view lists what is waiting: each record's procedure, result, time,
photo count and age, with **Write down weather** / **Look up weather**,
**Edit**, **Delete** and **Send now**. Editing matters more here than it does
online, because a queued record is the one thing in this system that is not yet
immutable, and a tester who notices a wrong commit hash on the drive home
should be able to fix it before it becomes evidence.

### Syncing

Automatically, on regaining connectivity, on page load, and on demand. It is
automatic rather than waiting for a press because the moment a signal appears
is rarely a moment anyone is looking at the page — a laptop lid opening in a
hotel lobby is the whole opportunity. What the tester sees is the result, not a
prompt beforehand: the pending count falls, and anything that did not go
through stays in the outbox saying why.

The sequence:

1. Upload every pending blob. Content addressing makes each one idempotent.
2. Post the ready records as one `POST /evidence/batch`.
3. Dispose of each per its status.

Blobs first, always — a record must never be filed pointing at bytes the store
does not have. The 24-hour orphan grace period in the blob sweep covers the gap
between step 1 and step 2, including a sync that dies between them.

| Outcome | What happens |
|---|---|
| `created` / `duplicate` | Dropped from the outbox; its blobs' refcounts fall, and bytes nothing else needs are deleted |
| `422` validation | Kept, flagged **needs attention** with the server's message. Never auto-retried — the answer will not change |
| `403` source | Kept, flagged, with the mismatch explained (below) |
| `401` | Nothing is dropped. Sign in again, resume |
| `5xx` / network | Kept, retried with backoff |

### Watching it go

A sync is not instant. A week of manual results is a handful of kilobytes of
JSON behind a few hundred megabytes of photographs, over whatever link a hotel
lobby provides, and a tester who cannot see it moving has no way to tell an
upload in progress from an upload that has quietly wedged — which is precisely
when they close the laptop.

So the outbox shows the work:

- **A progress bar over the whole sync**, weighted by **bytes rather than
  record count**, because the photos are effectively all of the time. Twelve
  records where three carry pictures is not three-quarters done when nine are
  filed.
- **A phase label** — `Uploading photos 7 of 23 (18.4 MB of 62.1 MB)`, then
  `Filing 12 records` — since the two phases fail differently and a tester
  reading a stall deserves to know which one they are in.
- **A state per row**, so a record that is queued, sending, filed or flagged
  looks different at a glance and the flagged one is not buried.
- **A summary when it finishes**: how many were filed, how many were already
  there, how many need attention. This is what replaces a confirmation prompt
  — the sync is automatic, so the honest reporting has to come afterwards.

Progress is driven by the sync loop's own accounting — bytes handed to `fetch`
per blob, records per batch response. Upload progress *inside* a single large
photo is not worth a `XMLHttpRequest` or a streaming request body to get; the
bar advances per photo, and with the existing downscale a photo is a second or
two, not a minute.

### Attribution, and signing in again

A 12-hour session and a three-day campaign means the session **will** have
expired by the time there is signal. That is the normal path, not an edge case:
the queue lives in IndexedDB rather than in the page, so it survives the
expiry, the re-login redirect through the identity provider, and the browser
being closed in between.

Each queued record remembers who was signed in when it was captured. If someone
else is signed in at sync time, those records are held back and say so, rather
than being sent. `BindSource` would refuse them anyway for an ordinary human
principal — but a sender holding `source:any` would succeed, and silently file
one tester's observations under another's name. Refusing is the only answer
that keeps `source` an attribution rather than a label.

### Does closing the browser lose it?

No. That is the reason the queue is IndexedDB and not a variable, `sessionStorage`
or a cache: it is on disk, and it survives closing the tab, quitting the
browser, and rebooting the laptop. A tester can file results on Tuesday, shut
everything down, and sync on Friday.

The honest list of what *does* lose it, because a tester on a campaign should
hear this before the campaign rather than after:

| What | Effect | What the plan does |
|---|---|---|
| **Private / incognito window** | Everything is discarded when the window closes | Detected at capture; queuing in a private window warns before the first record, in the strongest terms the UI has |
| **iOS Safari, site not installed** | Storage for a site that has not been visited in **7 days** is evicted | The manifest, and a README instruction to **Add to Home Screen** before leaving. An installed web app is exempt. This is why the manifest is in phase 2 rather than "polish" |
| **"Clear browsing data"** | Gone, with everything else | Nothing can be done, but the pending count in the header means it is not invisible beforehand |
| **Storage pressure eviction** | Browser reclaims space | `navigator.storage.persist()` requested the first time anything is queued; once granted, eviction needs a deliberate user action |
| **A different browser, profile or device** | Has its own empty outbox | The count is per-browser and says so |

Alongside that: the outbox shows what it is holding in megabytes next to the
count, and warns when the quota estimate is getting close.

**Age warnings.** A record that has sat unsent is the failure this feature can
actually produce, so it is not left to the tester to notice. Each row shows its
age; the header goes from a count to a warning when the oldest queued record
passes **7 days**, and escalates at 30. Seven is not arbitrary — it is the iOS
eviction horizon above, so the warning arrives while the data is still there.

*Corrected while building this (phase 6).* The table above promised to detect a
private window. There is no honest way to do that — every technique that claims
to is fingerprinting, and each one breaks with a browser release. There is a
direct question instead, and the Storage API answers it: *will you keep this?*
A private window says no, and so does a browser about to reclaim space. Those
are the two cases worth warning about and the only two a tester can act on, so
the page asks rather than guesses, and reports what it was told.

None of this makes a browser a safe place to keep evidence for a fortnight. The
README will say so plainly: sync whenever a signal turns up, rather than once
at the end of the trip.

## API changes

| Endpoint | Change |
|---|---|
| `POST /api/v1/evidence` | Accepts optional `client_record_id` (UUID). Returns `201` with the new record, or `200` with the existing one when the id has been seen |
| `POST /api/v1/evidence/batch` | Same field per record; per-record status gains `duplicate` |
| `GET /api/v1/evidence/{id}`, `GET /api/v1/evidence` | Responses include `client_record_id` when set |
| `POST /api/v1/blobs` | Unchanged — already idempotent |

No new endpoints, no new permissions.

## File summary

**New**

| File | Purpose |
|---|---|
| `migrations/000010_add_client_record_id.{up,down}.sql` | The column and its partial unique index |
| `web/static/outbox.js` | The queue: records and blob bytes, behind a thin storage adapter |
| `web/static/sync.js` | Drain the queue, dispose of each result |
| `web/static/sw.js` | Service worker: precache the app shell, network-first for the API |
| `web/static/manifest.webmanifest` | Installable on a phone's home screen |
| `web/static/pico.min.css` | Vendored — a CDN stylesheet is unreachable on a proving ground |
| `tests/idempotency_test.go` | Server-side duplicate handling |
| `web/tests/outbox_test.mjs`, `web/tests/sync_test.mjs` | Queue logic, sync disposition and progress accounting |
| `web/tests/weather_compose_test.mjs` | The written-down line matches `Reading.Summary()`'s shape |

**Changed**

| File | Change |
|---|---|
| `internal/model/evidence.go` | `ClientRecordID *uuid.UUID`; `duplicate` in the batch status |
| `internal/validate/evidence.go` | Reject a malformed UUID |
| `internal/store/evidence.go` | `ON CONFLICT … DO NOTHING`, then read back the existing row |
| `internal/api/evidence.go` | `200` vs `201`; `duplicate` in batch results |
| `web/static/app.js` | Mint `client_record_id` at capture; queue on failure or when offline; the outbox view, its progress bar and its age warnings |
| `web/static/images.js` | Offline path: downscale, hash, sniff, stash, write the final ref |
| `web/static/common.js` | Online/offline state in place of the health poll's error stream |
| `web/static/weather.js` | The **Write it down** composer; say why the lookup is unavailable offline; offer it from the outbox |
| `web/static/index.html` | Local Pico, manifest, service worker registration, outbox indicator |
| `web/embed.go`, `web/BUILD.bazel` | Embed the new static files |
| `DESIGN.md`, `docs/api-reference.md`, `README.md` | `client_record_id`, offline collection, the HTTPS requirement |

## Implementation phases

Each is a PR that stands on its own and leaves the system working. **All six
have landed**; the two corrections the work forced on this plan are recorded in
place, under [Weather](#weather-written-down-on-site-or-looked-up-afterwards)
and below.

1. **Idempotent ingest.** Migration, model, validation, store, API, tests, API
   reference. Useful on its own: any client retrying a post stops risking
   duplicates.
2. **The app shell loads without a server.** Vendored Pico, service worker,
   manifest, offline indicator. After this the page opens on a plane; it just
   cannot do much yet.
3. **The outbox.** Queue on failure or when offline, the outbox view, sync,
   per-status disposition, re-login survival. This is the phase that closes the
   issue's first AC and most of the second.
4. **Images offline.** Client-side hash, sniff, local stash, upload before
   records. Splitting this out keeps phase 3 reviewable; a campaign without
   photos is worth less, so this is not optional, only later. The sync progress
   bar lands here too — it is only worth looking at once there are megabytes
   behind it.
5. **Weather offline.** The **Write it down** composer on the form, **Look up**
   from the outbox for records captured earlier, and the `404`-window fallback
   between them.
6. **Durability and documentation.** Private-window warning, iOS install
   instruction, storage and age warnings, README and DESIGN.md.

## Testing

- **Go integration** (`tests/idempotency_test.go`) — the same `client_record_id`
  posted twice yields one row and a `200` the second time; a batch containing a
  repeat of itself and a repeat of an earlier call reports `duplicate` for
  exactly those; a record without the field behaves as it always did; a
  malformed UUID is a `422`. The existing migration round-trip test covers up
  and down.
- **JS unit** (`node --test web/tests/*_test.mjs`, no npm dependencies, per the
  repo's existing suite) — the queue against an in-memory storage adapter, so
  IndexedDB is not needed in Node; SHA-256 refs against published test vectors;
  media sniffing at each format's magic bytes and on a rejected type; sync
  ordering (blobs before records) and each status's disposition against a fake
  fetch, including that a `422` is kept and a `401` drops nothing; the
  written-down weather line against the field order and units
  `Reading.Summary()` produces, so the two paths cannot drift apart unnoticed;
  and the progress accounting on a mixed set of records, since a bar that
  reaches 100% early is worse than none.
- **By hand** — the honest test for this feature. Load the UI, turn the network
  off in devtools, file three records with photos, close the tab, reopen it,
  turn the network on, watch the queue drain, and confirm the images render in
  the record dialog afterwards. Then do it again with the session expired in
  between.

## Not in scope

- **The Bazel adapter.** A spool directory under `.evidence/` draining on a
  later `push` would cover automated tests on a disconnected rig. It is a
  natural follow-up and would reuse `client_record_id` unchanged, but the issue
  describes people on a campaign, not rigs.
- **A portable export bundle — not now, but expected.** Carrying the queue on
  a USB stick to a connected machine needs an importer able to write another
  principal's `source`: new permission surface, and a way to file evidence
  under a name that is not the sender's. That is its own design, and reconnect
  covers the campaigns we know about. The plan is built so it stays cheap to
  add — the outbox is a serialisable list of records plus blobs named by their
  own digests, which is already the bundle format; what it lacks is a writer, a
  reader, and the import permission.
- **Offline search.** Caching a slice of the archive for reading in the field
  is a different feature with a different cache-invalidation problem.
- **Editing filed evidence.** A queued record is editable because it is not yet
  evidence. Once the store has it, DESIGN.md's open question 5 governs.

## Settled during review

- **Sync is automatic**, on reconnect, rather than waiting for a press. The
  moment a signal appears is rarely a moment anyone is looking at the page.
  The honest reporting comes afterwards, in the summary, not before in a
  prompt.
- **A portable export bundle comes later**, not now. Reconnect covers the
  campaigns we know about, and the outbox is shaped so the bundle stays cheap
  to add.
- **The weather field gets both halves** — written down on site, or looked up
  afterwards — sharing the one format `Reading.Summary()` already defines.
- **A stale record is warned about, never refused.** Age on every row, the
  header turning to a warning at 7 days and escalating at 30. Nothing expires
  and nothing is rejected for age: a record queued in March and synced in June
  is still a true account of a test that happened in March, and refusing it at
  the door would destroy the only copy to punish the delay. The record carries
  a truthful `finished_at` and a three-month `ingested_at` gap, and the gap
  says everything a reader needs — which is the same way this store already
  treats evidence that arrives late from anywhere else.
- **`client_record_id` is not a search filter.** It is a client's own
  bookkeeping for deciding whether to send again, not something a reader of the
  archive asks about.

Nothing is left open. What is deliberately deferred is listed under
[Not in scope](#not-in-scope).
