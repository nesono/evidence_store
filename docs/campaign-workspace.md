# Shared campaigns

Implemented workflow for [#213](https://github.com/nesono/evidence_store/issues/213)
and [#214](https://github.com/nesono/evidence_store/issues/214).

- **Campaign package:** immutable reusable procedures, fields, defaults and assets.
- **Campaign:** one shared execution of a package against a fixed product repository,
  branch and deployed revision defaults.
- **Attempt:** one try at one procedure. Results and previous tries are preserved.

## Workflow

The Campaigns tab opens on the shared **Open** campaign list. Search filters the
whole server-side collection incrementally, using case-insensitive subsequence
matching (abbreviations are supported). Select Completed or All to find past work.
Executions appear in a table with Name, Repo, Branch, Commit, Exercised, Passed
and Skipped columns. Each count is relative to the total number of procedures.
A yellow outline circle identifies an open execution; a solid green circle means
the execution was finalized (not that all procedures passed). Repository URLs
show just the repository name, and clicking the abbreviated commit copies it in full.

**New campaign** offers the same searchable listing of available packages. Select
a package, name the campaign and enter the actual product repository, branch and
revision. V3 packages prefill repository, branch and commit from their product
build; legacy packages need those fields entered manually. Operators can override
the deployed revision. Multiple executions of the same package are allowed.
Import (including a duplicate import) offers **Start an execution** but never starts
one automatically. Use the [Vehicle feature tour](testing-campaign.md) for testing.

Opening a campaign shows one row per procedure in package order, with the latest
state shown by a circle: grey outline for not started, yellow outline for in
progress, solid green for PASS, solid red for FAIL, solid yellow for SKIPPED,
and solid purple for ERROR. A pending retry takes precedence over the previous
result; local completions remain in progress until synchronized. Hover or assistive
technology exposes the state label. Titles and references are left-aligned on at
most two lines; attempt history is available in the selected procedure. Select any procedure
or use **Previous / Next procedure**. Instructions and the attempt form are inline
in the workspace. Navigation saves changed drafts first; failed/conflicting saves
retain the form and prevent navigation. Finish-time selections are saved with drafts.
Online-only drafts require Save draft or workspace navigation before leaving.
[Prepared offline campaigns](campaign-offline.md) also autosave valid inputs on
this device after a short typing pause. Browser unload protection warns about unsaved changes.

The finish-time input has Now, Created and Pick Date shortcuts on its right.
Save draft, Reload draft and Finalize share one action row, with the current
procedure state underneath. Finalize saves current entries before publishing evidence.

Opening a procedure resumes its shared draft or displays its latest finalized
attempt. An untouched procedure starts a draft for a writer. **Attempts and retry**
contains history and the explicit retry action. One unfinished draft per procedure
per campaign is shared by collaborators; concurrent updates use revision checks.
Attempts record their creator, last editor and finalizer. Shared saves attribute
the source to the saving caller; finalization preserves that attribution and also
records who finalized. Standalone legacy drafts remain owner-scoped under package
management. Readers can inspect shared attempts but cannot edit, close or reopen.
Reader access includes the full attempt history. Each New campaign flow starts a
fresh selection and creation identity, even when its name and product context match
an earlier campaign.

## Finish and reopen

**Finish campaign** first saves the current editor, refreshes progress, and shows
the count of unfinished procedures/retries affected. Closing atomically:

1. Finalizes existing unfinished drafts as SKIPPED.
2. Creates SKIPPED attempts for procedures with no attempts.
3. Preserves entered notes, measurements, attachments and performed check outcomes.
4. Supplies NOT_EXECUTED and a closure reason only for unanswered checks.
5. Records the actor/time/reason in evidence and a campaign lifecycle event.

Closure is a specific disposition of unfinished work. It can retain PASS/FAIL
checks inside a SKIPPED attempt, unlike explicitly finalizing a wholly unexecuted
procedure. Existing finalized evidence is never rewritten. Closure-generated
timestamps describe the closure, not an invented time at which an unrun test ran.

Closed campaigns are read-only. **Reopen campaign** preserves all history and the
default product context. Running a skipped/finalized procedure again creates another
attempt. An unfinished retry following a PASS becomes the latest SKIPPED result
on closure; the previous PASS remains in history.

The overview uses the newest created finalized attempt. It reports exercised
(PASS/FAIL/ERROR), passed, skipped and in-progress counts separately. SKIPPED does
not count as exercised or passed. Expired evidence is shown as an expired result,
not a new unrun procedure; its removed contents are not reconstructed.

## API

Existing `/campaigns` routes continue to represent packages for compatibility.
New shared executions use:

| Method and path | Purpose |
|---|---|
| `GET /api/v1/campaign-executions?state=open&q=&limit=25&offset=0` | Shared listing with progress; state is open/completed/all |
| `POST /api/v1/campaign-executions` | Start with client UUID `id`, `package_id`, `name`, `repo`, `branch`, `rcs_ref` |
| `GET /api/v1/campaign-executions/{id}` | Identity, revision, lifecycle and ordered procedure progress |
| `PUT /api/v1/campaign-executions/{id}/state` | `{revision, closed}`; stale requests return 409 |
| `POST /api/v1/campaign-attempts` | `{id, execution_id, procedure_ref}`; returns/resumes the shared draft |
| `GET /api/v1/campaign-attempts?execution_id=…&procedure_ref=…` | Paginated shared attempt history |

Attempt save/finalize routes remain the same. Package listing also accepts `q`.
Shared reads require `evidence:read`; mutations require `evidence:write` and normal
session CSRF checks. Product context is copied to new attempts as defaults;
opening **Details** allows attempt-specific overrides. Overridden fields are
highlighted in light yellow, including after saving and reopening. Restoring a
default removes the highlight. Overrides do not change the campaign or subsequent
attempts. Procedure identity and source permissions remain enforced. Location and
Weather appear first; other default metadata is grouped inside Details.
Every attempt mutation locks its execution before the attempt; close cannot race
with a new draft or silently discard another collaborator's saved work. Refresh
after a lost close/reopen response to determine the authoritative state.
The detail endpoint reads lifecycle revision and procedure progress from one
database snapshot. If another collaborator has already performed the requested
close/reopen action, the UI refreshes instead of applying the opposite action.

Migration 21 adds executions, optional attempt execution links and lifecycle events.
It does not infer campaigns from old standalone attempts. Its down migration removes
execution links/history but retains attempts and evidence.

## Remaining follow-ups

[Offline preparation/synchronization](campaign-offline.md) is available through
**Prepare offline** and **Synchronize local work**. Procedure edits, release-to-package automatic
associations, repository RBAC, and historical check comparison/report exports remain
separate features. Package import/download is available through **Campaign packages**.

## Verification

Frontend tests: `node --test web/tests/*_test.mjs`.
Browser workflow: `node --test web/smoke/campaigns_test.mjs` with `CHROME` configured.
Backend integration: `go test ./tests` normally starts its own PostgreSQL container.
Without Docker, set `EVIDENCE_TEST_DATABASE_URL` to a **fresh disposable database**;
the tests apply migrations and create fixtures. Use a new empty database for each run.
