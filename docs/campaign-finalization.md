# Finalizing campaign attempts

Saved [attempt drafts](campaign-attempt-drafts.md) can now be finalized as ordinary
Evidence Store records. The evidence is searchable by actual software revision,
procedure reference, type, tags, and result through the existing Search view.

## Web workflow

1. Start or resume an attempt, record answers and actual software context, then
   **Save draft**. Finalization acts on that saved revision, not unsaved form data.
2. Set the **actual finish time** using **Now**, **Draft created** (the attempt's
   original creation time), or the **UTC date/time picker**. The RFC3339 text
   field remains editable, including pasted timezone offsets such as
   `2026-10-25T14:30:00+02:00`; the picker displays that instant in UTC. Nothing
   is selected automatically. Your selection survives saving the same draft
   in the open dialog; it is persisted with the evidence on finalization.
3. Select **Finalize as evidence** and confirm. Validation errors leave the draft
   editable; save corrections and try again.
4. On success the attempt becomes read-only. **Open evidence record** opens its
   record in a separate tab. The attempt list distinguishes drafts, finalized
   attempts, and attempts whose evidence has expired.

Corrections or reruns require a new attempt. They do not replace the old record,
and this version does not aggregate multiple attempts into a campaign verdict.
The original package and procedure remain unchanged.

## Validation

Finalization validates the saved typed answers and requires actual repository,
branch, deployed revision, an explicit canonical result, and a finish timestamp.
Every declared check needs an explicit outcome. NOT_EXECUTED requires its own
reason. Bound result fields use the attempt's result, never a second answer.

- **PASS:** every check passes and every required acquisition input is present.
  Numeric zero is a value; empty strings and empty evidence-link lists are missing.
- **FAIL:** permits failed checks and explicitly unreached checks. Tester notes
  must explain any omitted required inputs or an assessment with no failed check.
- **ERROR:** tester notes explain the infrastructure/precondition failure and
  omitted required inputs. Reached check outcomes are retained.
- **SKIPPED:** all checks are NOT_EXECUTED and tester notes explain why the test
  was deliberately not run. Required measurements need not be invented.

This preserves the existing result enum. Check counts describe observed progress,
not a percentage of capability or automatic requirement fulfillment.

## API and retries

`POST /api/v1/campaign-attempts/{id}/finalize` requires `evidence:write`, ownership
of the attempt, and the existing session CSRF checks. Example body:

```json
{
  "revision": 2,
  "finished_at": "2026-10-25T14:30:00Z"
}
```

The response is the attempt record, with `finalized_at`, `finished_at`,
`finalized_revision`, `evidence_id`, and an incremented revision. `Location`
names the evidence API resource. The timestamp is normalized to UTC and
PostgreSQL microsecond precision before retry comparison.

- **201:** first successful finalization.
- **200:** retry of the same accepted draft revision and normalized finish time.
- **409:** stale revision, different finalization request, or a save to an
  already-finalized attempt. Reload to inspect the server's state.
- **422:** missing or invalid result/context/check/input/finish information.
- **404:** unknown attempt or an attempt owned by another subject.

The attempt row lock serializes finalization against draft edits. The server
inserts evidence through the existing validated evidence insertion path and
updates the attempt's evidence link in one transaction. No independent “mark
complete” request is needed. The attempt identity itself makes finalization
idempotent; the client does not supply a second evidence submission UUID.

This dedicated endpoint is currently online-only. Single/batch evidence ingestion
continues to ingest ordinary evidence; it does not finalize campaign attempts.
Offline attempt synchronization and batch finalization remain follow-up work.

## Evidence snapshot and retention

Core evidence fields come from the saved attempt's actual software context,
permitted source attribution, result, and supplied finish time. Evidence type,
tags, and custom metadata start from the imported procedure and can be overridden
in the manual-result form. `procedure_ref` is immutable. Location, weather,
test log and its attachments, and `metadata.notes` retain the tester's entries.
`metadata.procedure_modified` is false because editing procedures is not yet
supported. `metadata.campaign` contains a versioned snapshot of package digest,
campaign key/UUID, procedure reference, attempt UUID, source repository,
instructions/field definitions, asset mapping and structured answers.

Search renders a check-count summary and expandable executed instructions/answers.
The raw snapshot remains available in technical metadata. This is a captured
definition, not a request to fetch a changing source repository. External evidence
links remain links; test-log photos/videos use the existing CAS media upload path.

Setup assets and test-log attachments gain evidence-owned CAS references in the
finalization transaction. Setup assets also keep their package references. Sweeping is coordinated through the
same shared lock used for campaign publication. Draft-owned log references are
released only after evidence references are established in that transaction.

Migration 19 adds finalization fields and an evidence foreign key. When evidence
retention deletes a record, a database trigger clears the attempt's duplicate
answers, notes and software context, and the foreign key clears its evidence
link. Only attempt identity/finalization timestamps remain. A retry after expiry
returns that expired marker and never resurrects the record. Package content
retains its separate lifetime. Rolling back migration 19 removes finalized
attempt rows rather than turning them back into editable drafts; existing
evidence records remain.

Verification covers finalization validation, atomic publication, retries,
concurrent finalization, ownership, immutable saves, evidence snapshot/assets,
retention expiry, migrations, and the browser's save/finalize/read-only flow.
