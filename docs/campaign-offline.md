# Offline campaign execution

Create the shared campaign and import its package while connected, then open it
and select **Prepare offline**. Wait for **Prepared** before disconnecting.

## Preparation and capture

Preparation downloads the exact archive, procedure definitions, all packaged
assets, and each procedure's current draft and latest result. Archive and asset
bytes are SHA-256 verified before readiness is recorded. It does not create
server attempts or change colleagues' work.

The shell must already be controlled by its service worker. If asked, reload once
while connected and prepare again. Offline operation needs HTTPS (or localhost),
IndexedDB, and Web Locks. Storage failures are surfaced, with no page-memory
fallback. The browser is asked for persistent storage; if refused, the status
explains that storage can be evicted. Deleting site data removes local work.
Online campaign browsing, capture, attachments and lifecycle actions remain usable
when IndexedDB or Web Locks is unavailable; only offline preparation is disabled.

Offline, Campaigns lists prepared campaigns. Their fixed software context,
procedures, instructions and assets survive reloads. Progress is the last server
snapshot; pending local work is reported separately. Select a procedure to resume
a local draft or start one with a client-generated UUID. Prepared drafts autosave
valid inputs after a short typing pause, and save before workspace navigation.
**Save draft** explicitly waits for the disk transaction. Wait for **Saved on this
device** before reloading; invalid input that cannot yet be represented as a
draft remains unsaved and triggers unload protection.

Test-log photos and videos live in campaign IndexedDB storage, outside the
evidence outbox's orphan sweep. Offline videos are limited to 100 MiB per file
because browser SHA-256 buffers the bytes. Structured evidence fields still
accept external links only. External resources and weather lookups need a connection.

**Save and finalize** validates the form and stores a local completion request.
**Completed locally, waiting to synchronize** is not finalized evidence. The
attempt becomes read-only until synchronized or its conflict resolved. Synchronize
that completion before starting another retry for the same procedure.

## Reconnection and conflicts

Sync runs on reconnect, when opening Campaigns, and periodically while the page is
visible and connected. **Synchronize local work** retries immediately. The page
must be open; there is no background service-worker submission.

Storage is partitioned by principal and browser origin (deployment). Offline
reload uses only the last successful identity; online authentication refusal
never falls back to it. Logout/key changes clear the remembered identity. Before
syncing, `/me` must confirm the capturing principal, even when the current caller
can submit on another source's behalf.

The journal creates the attempt, uploads referenced attachments, saves answers
with revision checks, then finalizes with the saved revision and original finish
time. An outgoing save is persisted before sending. Lost responses replay the
same operation ID/payload; later edits wait behind it. Lost finalization responses
replay the same revision/time. Network/server errors retain work; explicit server
refusals stop automatic retries for that attempt. Web Locks serialize writes/sync
across tabs, and stale local editors must reload.
If a creation response is lost, its retry keeps the original UUID. An attempt
edited or finalized in the meantime requires conflict review before local answers
can replace server answers.

Conflicting shared edits, a colleague starting the same procedure, and remote
closure/finalization never silently replace local answers. Open the affected
procedure's **Local work needs review** display to compare local/server JSON:

- **Export local draft** downloads the journal/answers as JSON, not attachment bytes.
- **Reload saved draft** refreshes the server conflict without discarding local work.
- **Use local answers** explicitly replaces the reviewed mutable server answers,
  guarded by its revision; another remote edit requires review again.
- **Use server answers** accepts the server copy. The local journal is archived
  on this device before replacement.

If creation was refused before a server attempt existed, the panel instead offers:

- **Retry creation** checks that the campaign is open and retries the same UUID,
  retaining the queued answers, attachments and original completion time. A
  colleague's concurrent start still requires conflict review.
- **Archive local attempt** retains its journal and attachments on this device
  and removes it from pending synchronization. It needs no server connection.
- **Reload draft** checks the campaign's state rather than fetching a nonexistent
  attempt. A closed campaign can be reopened from this workspace before retrying.

Finalized evidence cannot be overwritten. Export local work, accept the server
version, then start a new attempt if needed. Choosing local answers returns a
draft: review and finalize it again explicitly. Finishing a campaign needs a
connection and blocks until local work has synchronized or been resolved.
Reopening needs a connection but does not publish or discard pending local work.

## Scope and verification

Import, new campaign creation, and full historical attempt browsing remain online.
Preparation includes only current drafts/latest results; prepare again to collect
new remote attachments before disconnecting. Connected history remains paginated
server history, with pending local attempts pinned to the first page. Offline
history is explicitly labeled as the prepared subset. Prepared packages, attachments and
resolution archives are retained locally; this version has no automatic eviction
or per-campaign cleanup. Procedure editing, package/revision associations and
cross-run reporting remain separate.

```sh
node --test web/tests/*_test.mjs
CHROME=/path/to/chrome node --test web/smoke/campaigns_test.mjs
```

Queue tests cover digest failures, reload recovery, attachment ordering, lost
responses, changed principals, conflicts, stale tabs, failed creations and failed
disk writes. Browser tests cover offline reload and completion, recovery after
remote closure, paginated history, bounded IndexedDB queries and online workflows
with unavailable offline capabilities.
