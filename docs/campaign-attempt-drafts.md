# Campaign attempt drafts

After selecting a procedure in **Campaigns**, contributors can **Start new
attempt** or resume one of **their own saved drafts**. Procedure inspection stays
read-only; capture opens in a separate dialog clearly labeled as a draft.

## Using a draft

1. Select a procedure and choose **Start new attempt**. This immediately creates
   a server-owned draft with a new UUID. Only declared ordinary input defaults
   are copied. Checks, notes, actual software context, and overall assessment
   start empty.
2. Enter the actual software repository, branch and deployed revision. These are
   editable draft context, never inferred from the package source. Release/package
   associations and session-wide revision policy will be implemented separately.
3. Fill fields and checks alongside the instructions. Measurements are numbers
   in the declared unit. A NOT_EXECUTED check needs a reason. Evidence fields
   accept one external HTTPS link per line; file uploads and CAS attachments
   require draft-owned blob references and follow later.
4. Choose **Save draft**. Only a confirmed server response shows “Saved on server”
   and the current revision. Required acquisition inputs can remain unanswered.
   The overall assessment is provisional; even PASS is not published as evidence
   until [finalization](campaign-finalization.md).
5. Close the dialog, reload, and select the same procedure to resume a saved draft.
   The list is newest first, 25 entries per page, scoped to the current owner,
   imported package UUID, and procedure reference. Refresh retrieves saved drafts.

Edits are held in page memory until an explicit save. Closing a dirty dialog asks
before discarding them; browser navigation gets the standard unsaved-work warning.
This is **online capture**, not the planned IndexedDB/offline workflow. Network
failure or expired authentication leaves inputs in place and reports “Not saved”.
A conflict likewise preserves edits; copy anything needed before using **Reload
saved draft**, which explicitly discards local changes after confirmation.

## API

All routes require `evidence:write` and existing authentication/session-CSRF
checks. Ownership is the authenticated subject; it cannot be supplied or changed
by the request. Another subject, including an administrator, cannot read or edit
the draft (404). With authentication disabled all requests share `anonymous`;
use authentication for separate users' drafts.

- `POST /api/v1/campaign-attempts`: `{id, campaign_id, procedure_ref}`. Both IDs
  are nonzero UUIDs, `id` chosen by the client. The procedure must exist in the
  immutable imported package. Returns 201, or 200 for an identical identity retry
  without resetting existing answers. Reusing the UUID for a different owner,
  package, or procedure returns 409.
- `GET /api/v1/campaign-attempts/{id}`: read the owned draft.
- `GET /api/v1/campaign-attempts?campaign_id=...&procedure_ref=...&offset=0`:
  `{attempts, has_more, offset}`. References are URL-encoded query values.
- `PUT /api/v1/campaign-attempts/{id}`: replace the draft data with
  `{revision, operation_id, data}`. `revision` is the last server revision and
  `operation_id` a nonzero client UUID. A successful edit increments the revision.

`data` contains `repo`, `branch`, `rcs_ref`, `notes`, `result` and `answers` (an
object keyed by field/check IDs). Result is empty or one of PASS/FAIL/ERROR/SKIPPED.
Text/choices are strings, measurements finite numbers, checks
`{outcome, reason?}`, and evidence links arrays of `{uri, label}`. Unknown control
IDs, bound results inside `answers`, unsupported values and malformed payloads
are rejected. Missing answers are allowed; use omission rather than null.
Definition changes and `procedure_modified` are not accepted in this draft slice.

Responses include `id`, `campaign_id`, `procedure_ref`, `owner`, `revision`,
`created_at`, `updated_at`, and `data`. Package content is not copied or mutated:
the draft refers to a fixed immutable package/procedure pair. Its database foreign
key prevents deleting that package while drafts reference it.

## Concurrency and lifecycle

Saves lock one attempt row and check the revision. Retrying the last accepted
operation with the same base revision and data returns the saved row without
incrementing it again. The same operation ID with changed data, or a new edit
based on a stale revision, returns 409. This prevents silent last-writer-wins
behavior across devices. Older retries after a newer edit also conflict.

Migration 18 adds the attempt table and owner/package/procedure lookup index.
There is no automatic draft expiry or deletion API in this slice. Unfinalized drafts
do not insert evidence records, appear in evidence search, compute campaign
progress, or claim requirement coverage. [Finalization](campaign-finalization.md)
now publishes saved attempts as immutable evidence; execution sessions, procedure
editing, file attachment lifetime, and offline persistence follow in subsequent slices.

Verification covers typed validation, default initialization, API ownership,
creation/save retries, stale and simultaneous saves, migration rollback/reapply,
and real-browser start/save/reload/resume/conflict behavior.
