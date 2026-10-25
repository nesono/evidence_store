# Test campaigns — first implementation proposal

Status: implementation in progress; full campaign workflow remains proposed. Companion to the
[campaign design](test-campaign-packages-design.md). This defines a v1 to
exercise with V&V before freezing a schema or implementing the full workflow.

Implemented: the [procedure validator](campaign-procedure-validation.md) and
[package builder/validator](campaign-package-format.md). Server import and the
execution workflow remain planned. The package format document is the implemented
v1 contract; following [#200](https://github.com/nesono/evidence_store/issues/200),
product revision associations live outside the content-addressed archive.

## Recommendation

Keep the boundary agreed in [#127](https://github.com/nesono/evidence_store/issues/127)
and [#199](https://github.com/nesono/evidence_store/issues/199): the source
repository produces an immutable, self-contained package; Evidence Store
imports it and owns execution. Start with manual import, procedure selection,
and one durable attempt, then add the campaign session and offline workflow.

Use **tar.gz + YAML + Markdown with typed fences**. YAML is small, readable
metadata; Markdown preserves the team's briefing style; typed fields allow
reliable capture and reporting without interpreting prose. Do not introduce a
second `procedures.yaml` catalogue or `observations.yaml` declaration file.
An API representation parsed from the package is a derived cache, not another
authoring format. This is a manual-execution interchange format, not a new
simulation language or a replacement for the source's requirements format.

## Evidence informing the proposal

Initial review on 2026-10-18; daily-patrol sample reviewed on 2026-10-25:

- [#199](https://github.com/nesono/evidence_store/issues/199): package-backed
  picker, reproducible source-side generation, editable execution copies,
  inline inputs, and tester-only notes.
- [#201](https://github.com/nesono/evidence_store/issues/201): server-side,
  stable multi-column evidence-search sorting, independent of campaign order.
  Implemented separately in [#202](https://github.com/nesono/evidence_store/pull/202),
  now on the base branch for this proposal.
- [#198](https://github.com/nesono/evidence_store/issues/198): keep the four
  core verdicts; report individual check outcomes instead of a core PARTIAL.
- A supplied field-test briefing: eight planned scenarios,
  an improvised reverse-driving variant, repeated setup metadata, blank
  per-check outcomes, a skipped test, and a disputed PARTIAL/possible-new-test
  assessment. Summary claims are not enough to reconstruct check outcomes.
- The daily-patrol sample discussed in [#197](https://github.com/nesono/evidence_store/issues/197):
  grouped procedures, per-check outcomes and references, per-day roles and
  operational measurements, and explicit reasons for not running a patrol.
  This document retains generic design implications rather than the attachment.

The reviewed examples are workflow samples, not an approved full catalogue.
Package authors remain responsible for the authoritative definitions and
reviewed acceptance criteria. Internal repository links, revisions, and source
documents are not reproduced in this proposal.

There are source discrepancies to resolve during package authoring:

- Use the reviewed baseline when older documentation and newer specifications
  disagree. A convenient link does not establish authority.
- Source docs disagree about JSON/YAML interchange and some vehicle-role
  conventions. Preserve explicit `vehicle_constellation`, `sut_control`,
  `leader_control`, and role assignments; do not infer these from a generic
  "mode" or bake product-specific rules into Evidence Store.
- Conflicting trigger conditions or reused scenario identifiers need an authoring
  decision, not an automatic scenario rewrite or merge based on a shared ID.
- Resolve unspecified tolerances and ensure checks actually assess the named
  behavior. Do not invent thresholds or claim requirement coverage from a title.
- Legacy V&V reporting treats PASS/PARTIAL differently from Evidence Store's
  proposed check model. Existing milestone calculations need an explicit
  reporting-policy decision; a check pass fraction is not capability coverage.

## V1 package contract

### Files and identity

```text
campaign.tar.gz
  manifest.yaml
  scenarios/reverse-up-nominal/
    scenario.yaml
    procedure.md
    assets/setup.png            # optional, packaged dependency
```

The following is an illustrative format example, not an approved test or an
import of historical results.

```yaml
format_version: 1
campaign:
  id: vehicle-patrol-example
  name: Vehicle patrol example
  planned_location: Proving ground
  planned_start: "2026-10-05"
  planned_end: "2026-10-05"
source:
  repository: https://example.org/team/scenarios
scenarios:
  - id: reverse-up-nominal
    path: scenarios/reverse-up-nominal/scenario.yaml
```

- `format_version` is required at the root. Reject unsupported versions.
- `campaign.id` and name are required; planned location/dates are optional.
  Dates are quoted ISO calendar dates; actual attempt times use RFC3339 UTC.
- `source.repository` is a canonical URL including host. Product revision
  provenance is associated with the package digest in an external build/release
  record, following #200. The package SHA-256 is computed from archive bytes by
  the importer; it is not embedded in the manifest it would hash.
- `scenarios` is a nonempty ordered list with unique IDs and definition paths.
  This is the complete selected test set and default order.
- Definition identity is `(source repository, scenario id)`; exact version is
  `(package digest, scenario id)`. Check/field IDs are scoped to that definition.
  Campaign IDs, scenario IDs, human titles, and execution IDs are distinct.
- Multiple packages may share a campaign ID. New bytes create a new immutable
  import; no "replace existing campaign" operation. Identical bytes deduplicate.

External generation inputs and generator/toolchain provenance should be retained
with the build/release record. V1 accepts only `source.repository` in the archive;
it contains no instructions for Evidence Store to fetch or execute anything.

### Scenario metadata

```yaml
id: reverse-up-nominal
title: Reverse uphill from standstill
procedure_ref: scenario/reverse-up-nominal
description: procedure.md
parameters:
  initial_speed_kph: 10
  follow_distance_m: 50
defaults:
  evidence_type: manual_test
  tags: [vehicle, patrol]
  metadata:
    cluster: Autonomous Convoying
    challenge_level: feature
    vehicle_constellation: convoy_manual_leader
    sut_control: autonomous
    leader_control: manual
traceability: []
```

`id`, `title`, `procedure_ref`, `description`, and `defaults.evidence_type` are
required. Parameters, tags, custom metadata, and traceability may be empty.
Description is relative to `scenario.yaml`; Markdown asset paths are relative
to the description. All paths must resolve inside the archive.

`parameters` records fixed, resolved configuration, not ranges or tester
responses. Instructions already contain concrete values; the importer performs
no templating. Authors keep parameters and instructions consistent in generation.

Prefer a stable source Bazel target for `procedure_ref` once one exists; a
stable `scenario/<id>` works for the initial manually prepared package. Do not
present proposed Bazel labels as targets already present in the source repo.
Preserve existing `scenario/LSC-*` references where they still uniquely identify
a concrete procedure. When expanding an LSC, give each variant a stable suffix
or content-derived parameter key and retain the parent reference. Never use its
position in a Cartesian product as identity.

`traceability` entries have `kind` (`functional_scenario`, `logical_scenario`,
or `requirement`), `id`, `repository`, `path`, and optional
`entry_version`. They are explicit source-authored links; source-checkout revision
provenance belongs to the external build record. A path may include a
fragment. Evidence Store preserves and displays them, without resolving a
requirements graph. Omit unknown links. An `lsc_id` metadata value is useful for
multiple concrete children; omit it if it merely duplicates `procedure_ref`.
Do not require FSC/LSC parents for patrol checks or one-off controllability tests.

### Inline fields and checks

One `procedure.md` contains setup, steps, acceptance criteria, and input
declarations. This abbreviated example demonstrates the format, not a complete
driving procedure:

````markdown
# Reverse uphill from standstill

## Setup

Establish the convoy at 10 km/h with a target follow distance of 50 m.

```test-field
id: truck_leader
type: text
label: Leader vehicle
required: true
```

```test-field
id: surface_condition
type: choice
label: Actual surface condition
options: [dry, damp, wet]
required: true
```

## Acceptance

Observe the follower at standstill.

```test-check
id: no_roll_at_stop
label: Follower does not roll at standstill
behavior: expect
```

Record the observed lateral deviation; the complete source procedure specifies
the measurement method and acceptance criterion.

```test-field
id: lateral_deviation
type: measurement
label: Observed lateral deviation
unit: m
```

## Assessment

```test-field
id: supporting_evidence
type: evidence
label: Photos or recording references
```

```test-field
id: result
type: choice
label: Overall result
bind: execution.result
options: [PASS, FAIL, ERROR, SKIPPED]
required: true
```
````

V1 parsing and value rules:

| Declaration | Stored answer value | Rules |
|---|---|---|
| `text` | string | Optional `multiline: true`; no executable template expressions |
| `choice` | string | Required nonempty, unique string `options`; answer must be a member |
| `measurement` | finite JSON number | Required fixed `unit`; no implicit unit conversion |
| `evidence` | array of `{uri, label}` | Local supported media references or external HTTPS links; no remote fetching |
| `test-check` | `{outcome, reason?}` | `PASS`, `FAIL`, or `NOT_EXECUTED`; reason required for NOT_EXECUTED |

- Declarations are top-level triple-backtick fences with exact info strings
  `test-field` or `test-check`. Bodies are YAML mappings; no aliases, custom
  tags, merge keys, or duplicate keys. Other fenced code remains ordinary text.
  Reject unknown `test-*` declarations rather than silently dropping controls.
- IDs match `[a-z][a-z0-9_]*` and are unique across fields and checks in one
  procedure. Labels are required. Stable semantic IDs survive reordering;
  changed check meaning needs a new ID. Preserve source PC numbering in labels
  if useful; never derive identity from rendered line numbers.
- Optional `default` is allowed for text, choice, and measurement inputs and
  must validate as an answer. Checks and the overall result have no default.
  Unanswered means absent from the answer map, not false, zero, or failed.
- `required` defaults to false on ordinary fields; all checks must have an
  explicit outcome at finalization. Drafts may contain missing responses.
  Finalizing ERROR/SKIPPED, or a concluded FAIL with unreached steps, may omit
  otherwise required acquisition inputs, but requires an assessment reason
  identifying the missing inputs and explicit NOT_EXECUTED for unreached checks.
  Do not force testers to invent a measurement for an unperformed step.
- Only `bind: execution.result` is initially supported, on one choice field
  with exactly the four core options. It renders the existing result control;
  its value is stored only in the canonical result, not a second answer. If
  absent, the application supplies the same unselected result control.
- First implementation accepts only `behavior: expect`. No automatic stop,
  branches, scripts, or computed pass/fail expressions. Reject `assert` until
  its interaction and reporting semantics have been agreed and implemented.
- Parse on the server into ordered Markdown/control blocks; keep original
  bytes too. The browser renders that validated representation. Shared fixture
  tests enforce identical validation when editing or working offline. Field
  IDs and labels are data, never inserted as trusted HTML.

### Results and editing

An attempt retains a procedure snapshot (metadata, parameters, field
declarations, Markdown and asset mapping) and answers. Example fragment:

```json
{
  "procedure_modified": false,
  "answers": {
    "truck_leader": "example-vehicle-A",
    "surface_condition": "wet",
    "lateral_deviation": 0.7,
    "no_roll_at_stop": {"outcome": "PASS"},
    "supporting_evidence": []
  }
}
```

Ordinary answers, actual vehicles/builds, weather, location and tester notes
are execution context. Filling them does not modify the procedure. Package
defaults and instructions are prescribed content: changing them uses **Edit
procedure**, retains the effective copy, and sets `procedure_modified: true`.
The flag is sticky for that attempt after a saved edit. Detailed diffs and
deviation categorization are deferred. Changing field declarations must never
silently reinterpret prior answers: require clearing incompatible answers or
keep them explicitly as inactive historical responses.

The picker initializes only a new/empty attempt. It must not overwrite typed
responses or carry answers from another scenario. Remember session context
(vehicles, location, SUT build) separately from procedure defaults. Apply
validated source defaults, then compatible session input defaults, then explicit
attempt inputs; later changes to session defaults affect future attempts only.
Never prefill Notes, an overall PASS, a check result, or a historical timestamp.

Finalization requires a deliberate overall assessment:

- PASS requires all checks PASS and required inputs filled.
- FAIL records a concluded negative assessment, including a failed check;
  progress remains visible. An early stop because of observed product failure
  may conclude the test as FAIL with later checks NOT_EXECUTED and an explanation.
- ERROR means the existing infrastructure/precondition-failure semantics,
  not merely "some checks are unfinished". SKIPPED means deliberately not run.
- A partly executed test without a defensible overall assessment remains a
  draft. UI must not round that state to PASS or FAIL. Source definitions should
  split meaningful increments into separate procedures where appropriate.
- A finalized attempt is immutable. Corrections and reruns create new attempts,
  optionally referring to the earlier one; retrying a submission is not a rerun.

Present `passed / failed / not executed / unanswered` counts and individual
checks. Only compare unmodified, matching definition versions automatically.
Changed-package comparisons need an explicit compatible-check mapping later.
These counts do not define requirement fulfillment or an overall campaign score.

## Package production and import

Source-side process (proposed targets, not existing commands):

1. V&V selects a good case and a small number of relevant edge cases, considering
   site, equipment and team readiness. A campaign can mix scenario and patrol
   procedures. Record nominal date/location; actual values are collected later.
2. Export the authoritative catalogue at an explicit version and digest. Review
   definitions, resolve acceptance criteria, and commit concrete outputs plus
   any Bazel declarations. Until expansion exists, author reviewed concrete
   files directly. Do not hand-edit generator-owned files without changing the
   source inputs or maintained authoring overlays.
3. CI regenerates and checks no diff. Build individual targets and a package
   target consuming only declared files with a pinned toolchain. Keep acquisition
   of external catalogue inputs separate from the hermetic package build.
4. Archive entries use sorted POSIX paths, fixed file modes (0644 files/0755
   directories), uid/gid 0, empty owner names, fixed mtime 0, and no host paths.
   Pin compression settings and gzip header time/name. Serialize generated YAML
   deterministically; forbid wall-clock timestamps/random IDs in generated
   content. Record the product revision and resulting package digest outside the
   archive, not through ambient workspace stamping. Verify identical bytes across
   two clean builds.
5. Generate a printable Markdown/HTML briefing from the same definitions.
   Field forms and the paper handout must not become independently maintained
   test specifications. Retain package and digest alongside the relevant release
   artifacts; automatic release ingestion can follow manual upload later.

Package authors should agree the source-repository layout and update any
structure rules before adding campaign files. Packaged images can be
deterministic renders of source-controlled diagrams or pinned external inputs,
consistent with the source repository's policy for binary documents.

Importer requirements for a usable v1:

- Validate before publishing any campaign. Reject duplicate IDs/paths, unknown
  schema fields outside explicitly open metadata/provenance, invalid defaults,
  missing description/assets, escaping paths, links in tar entries, and malformed
  field declarations. Enforce limits on compressed/decompressed bytes, file
  count, and individual documents. No unpack-to-arbitrary-filesystem operation.
- Embedded images must be package-local; external HTTPS links may be references
  but cannot be necessary to perform the test offline. Do not fetch them during
  import. Serve assets with the same authenticated media restrictions as evidence.
- Store original archive bytes and parsed definitions. Assign identity only after
  digest verification; publish DB records transactionally. Retried imports of the
  same digest return the existing package, including concurrent uploads.
- Store archive bytes through the internal blob store with a dedicated import/
  download API. The current public blob upload is media-specific: do not route a
  tarball through it or assume arbitrary-file upload already works.
- Retain packages and assets while campaign/execution references exist. Current
  `blob_ref` tracks evidence only, so add campaign-owned references and include
  them in reachability checks before exposing imports. A package must not lose
  setup images to the orphan sweep before its first test is run.

## Implementation in Evidence Store

### Minimal persistent model

Use ordinary relational identities for lifecycle relationships and JSONB for
variable definitions/answers. Avoid representing the whole campaign as a fake
evidence record: it has neither a test result nor a finish time.

| Entity | Minimum content |
|---|---|
| Campaign package | UUID, unique archive digest, manifest, source, imported by/at, original blob |
| Packaged scenario | Package FK + scenario ID, procedure ref, order, definition, asset map |
| Campaign execution (session) | UUID, package FK, label, owner, actual context defaults, scenario order, revision |
| Attempt | UUID, optional session FK, package/scenario FK, draft/finalized, effective snapshot, answers, actual context, submission UUID, optional prior-attempt link, revision |
| Final evidence link | Unique attempt-to-evidence relation, committed with the evidence insert |

Sessions reference one exact package. Validate an order update as a permutation
of its scenario IDs. Attempt IDs provide history without defining "latest wins".
Use optimistic revision checks for mutable drafts/order; report conflicts rather
than overwriting another device. Start with one owner per session and no shared
real-time editing. Packages are readable with evidence-read rights; import,
session/attempt creation and finalization require evidence-write rights (plus
existing blob permissions for uploads). Enforce ownership for draft mutations
and the existing source-identity rules when producing evidence.

Client-chosen attempt IDs make attempt creation idempotent too. Draft writes
carry a revision and operation ID so a lost response can be reconciled by reading
back the accepted operation rather than creating an artificial edit conflict.

Finalization uses the existing evidence validation and insertion path within
the same transaction as the finalized-attempt link. Allocate one
`client_record_id` for each attempt submission and persist it before sending.
A retry returns the same evidence; a different payload cannot mutate it.
Do not file evidence and mark completion through unrelated requests. Shared
ingestion validation must cover campaign records on single and batch endpoints
so the offline queue has the same guarantees as the online path.

Keep `repo`, `branch`, `rcs_ref`, `source`, `result`, and `finished_at` as existing
evidence core fields. `repo`/`rcs_ref` identify the primary SUT; the explicit
package source identifies the procedure repository if different. For mixed
vehicle builds, keep role-specific build context in metadata as well. Source
revision is never a substitute for the deployed SUT revision.

Following #200, a session normally targets one product revision linked to its
package through an external build/release record. Changing the product revision
starts another session. A procedure/software mismatch is an explicit override
with a reason, not an ordinary per-attempt default change. Per-vehicle component
context supplements that product identity rather than silently replacing it.

Reserve `metadata.campaign` for a versioned execution snapshot: package digest,
scenario ID, session/attempt IDs, source provenance, effective procedure,
answers, and actual role context. Store `metadata.procedure_modified` as the
#199 flag, verified against the effective definition. Validate reserved fields
and prevent custom-metadata rows from overriding them. Ordinary metadata remains
open. `metadata.notes` and existing free-text `metadata.observations` keep their
current meanings; answers are not overloaded into either string field.

Keep a self-contained finalized snapshot with the evidence so normal evidence
reads can explain the test. Define the link's deletion behavior explicitly:
evidence retention removes that attempt's result snapshot and attachment references
too; shared attachment bytes remain while another owner references them. Leave
only a tombstone/expired indicator in session history. Do not retain
a second unlimited copy of expired evidence in an attempt row. Package definitions
and their setup assets have a separate lifetime. Draft expiry is a separate policy.

### Proposed API surface

All routes below are proposals, not current API documentation:

| Route | Purpose |
|---|---|
| `POST /api/v1/campaigns/import` | Upload/validate a tar.gz and return package identity |
| `GET /api/v1/campaigns` | List exact imported packages with source and planned context |
| `GET /api/v1/campaigns/{id}` | Manifest, scenario list and readiness/download information |
| `GET /api/v1/campaigns/{id}/package` | Download the retained immutable archive |
| `GET /api/v1/procedures?campaign_id=...&q=...` | Search imported procedures; no runtime source fetch |
| `GET /api/v1/campaigns/{id}/scenarios/{scenario_id}` | Definition, typed blocks and asset mapping |
| `POST /api/v1/campaign-executions` | Start a session from an exact package |
| `GET/PATCH /api/v1/campaign-executions/{id}` | Read/update context and order with revision checks |
| `POST /api/v1/campaign-attempts` | Start a standalone or session attempt |
| `GET/PATCH /api/v1/campaign-attempts/{id}` | Read/save the effective copy and responses |
| Existing `POST /api/v1/evidence` and `/evidence/batch` | Finalize a validated campaign attempt atomically with evidence |

Imported definitions and historical evidence remain separate lookup sources.
Search can offer both, and free text remains valid even when no package contains
the historical reference. Scope picker labels by package/source to avoid showing
two different definitions as indistinguishable entries.

### Offline and attachments

The current service worker and IndexedDB outbox solve app-shell availability
and evidence retries, not package availability or unfinished campaign drafts.

- Import and create the session while connected. An explicit **Prepare offline**
  downloads definitions and all required assets, verifies digests, stores session
  context, and reports ready only when complete. No offline package import or
  new-session creation is required for v1.
- Store attempt drafts, snapshots, answers, order edits and submission UUIDs
  durably in IndexedDB before displaying "saved". Do not depend on page memory
  or the last-used-fields helper. New attempts within a prepared session use
  client-generated UUIDs and sync before their final evidence submissions.
- Keep evidence outbox states distinct from execution state: "completed locally,
  waiting to send" is not a server-confirmed result. Synchronize assets, mutable
  drafts/order with revision checks, then final evidence; same-token retry is
  idempotent. A conflicting remote edit needs resolution, not a silent merge.
- Preserve the current authentication/subject check after reconnecting. Reauth
  must not attribute offline work to another tester. Scope local package/draft
  storage to the authenticated principal and the deployment.
- Extend `web/static/blobref.js`, the outbox's local reachability sweep, and
  `internal/store/blobref.go` for structured evidence-field references. They
  currently examine observations/photo/video URIs, not arbitrary nested answers.
  Package assets and draft uploads also need explicit local/server roots.
- First use the supported image/video upload types plus external log/recording
  links. Arbitrary local file uploads need a separate extension. External links
  are not promised offline; required setup dependencies must be packaged.

### Delivery slices and acceptance

1. **Contract and source pilot.** Agree this format using three representative
   procedures: reverse/slope checks, vibration/object detection with a measured
   value, and the multi-step differential-lock test. Review the entire eight-test
   briefing selection after those fit. Preserve full source acceptance criteria,
   not just these abbreviated examples. Produce a deterministic package and
   a handout from the same inputs. Resolve source ambiguities before adoption.
2. **Import + picker + one attempt (#199).** Add package validation/storage,
   parsed block rendering, input controls, local draft persistence, edit-copy
   flag, the minimal attempt API, and atomic evidence finalization. Reuse the
   Add Result screen and free text entry. First acceptance: a package alone
   supplies setup/steps/criteria;
   a result preserves inputs and source; an edited copy leaves its package intact.
3. **Usable campaign session (#127).** Add context reuse, order, pending/completed
   display, attempt history/reruns, server drafts and prepared-offline operation.
   Acceptance: prepare online, disable networking, run and partially fill tests,
   reload the browser, then reconnect; drafts/order survive and finalized results
   arrive exactly once with their media. This is the first field-ready milestone.
4. **Progress views (#198) and reporting.** Expose individual check outcomes and
   counts, distinguish changed definitions and procedure edits, and generate the
   daily summary from recorded results. Agree legacy/milestone reporting changes.
   Preserve the independent evidence-search sorting delivered in #202 for #201.

Likely code touchpoints: new `internal/campaign` parser/validator, models/stores
and migrations, `internal/api` handlers and route wiring in
`internal/server/server.go`; `web/static/addform.js`, `markdown.js` (reuse its
safe prose rendering), new campaign controls, IndexedDB/outbox/sync, evidence
detail rendering, and both server/client blob-reference extraction. The frontend
is plain ES modules; no new application framework is needed.

Meaningful implementation checks:

- Format fixtures: full briefing-style instructions, stable field identities,
  invalid declarations, modified copies, measurement/choice validation and
  no accidental PASS defaults.
- Determinism across clean builds; malformed/escaping/oversized archives; missing
  assets; concurrent duplicate imports; rollback without visible half-imports.
- Finalization retries and conflicting payloads; distinct reruns; source-versus-SUT
  provenance; notes stay empty on selection; no metadata override of identity.
- Offline reload/reconnect and expired auth; incomplete prefetch; draft conflicts;
  structured attachment upload and reachability; package assets survive retention.
- Keyboard/mobile picker and inputs; historical free-text search. For #201,
  server-side ordering across pages, URL round trips, ID tie-breakers and pagination
  reset are separate tests; sorting a table must not reorder a campaign.

## Applying the design to the field-test briefing

| Observed case | Representation / next authoring step |
|---|---|
| Eight planned scenarios, repeated date/site/tags | One package with ordered entries; session context seeds compatible inputs |
| Opposite slope directions | Separate concrete definitions with stable IDs, complete steps and checks |
| Ad hoc rollback variant | Separate modified attempt based on the original procedure; retain changed instructions and flag; propose a stable new source variant after review |
| PARTIAL assessment with rain-related false positives | Preserve the observation and legacy wording; assess each actual check; no invented PASS values or silent new requirement |
| Multi-step lock controls from different operator stations | Separate definitions; author stable per-stage checks if stage progress matters; do not claim old aggregate results prove each stage |
| Explicitly skipped test | Explicit skipped attempt with reason and NOT_EXECUTED checks, if confirmed by tester |
| Missing verdict and uncertain location | Pending/draft, not inferred SKIPPED; choose actual location at execution |
| Summary PASS with blank detailed criteria/build fields | Preserve as historical narrative until reviewed; no fabricated per-check results, timestamps, deployed commit or vehicle assignment |

Do not import the annotated briefing itself as a clean package: it mixes planned
instructions and results. Prepare definitions from reviewed source material and
retain the annotated original as historical evidence. Any legacy result migration
is separate and preserves its original vocabulary, uncertainty and provenance.
The first campaign pilot can proceed without automating that migration.

## Applying the design to the daily-patrol sample

The additional sample combines several checks in each procedure and stores
verdicts, logical-scenario references, comments, and issue keys as parallel
comma- or pipe-separated strings. This exposes useful requirements, but those
strings should not become the campaign response format: reordering a check or
entering a delimiter in a comment could silently associate the wrong values.

| Sample feature | Proposed representation |
|---|---|
| One procedure contains several related checks | Keep a grouped procedure when it is executed and assessed as one unit; use stable check IDs and Markdown headings. Split independently rerunnable cases into separate concrete procedures. |
| Parallel verdict/comment/reference lists | Store answers by check ID. Put a check's comment in an adjacent text field and its issue links in an adjacent evidence-reference field, each with a stable related ID. Never join answers by array position. |
| Different logical-scenario references per check, including no reference | Preserve explicit traceability beside the relevant check; absent is empty, not a synthetic `none` scenario. V1 may use source-authored Markdown links; structured per-check traceability needs an agreed schema before automated coverage reporting. |
| Legacy per-check partial/blocked/not-run vocabulary | Preserve original wording during any migration; review the actual outcome. New attempts use the agreed check outcomes, reasons, and separate overall assessment. An untouched prefilled not-run value is not an explicit skip decision. |
| Roles and operating measurements entered once per day | One execution session per patrol occurrence; retain shared context and collect operational measurements in a designated setup/summary procedure. Package definitions remain reusable across dates. |
| Whole patrol or selected procedures not run | Record a reason and an explicit set of affected scenario IDs at the session level; expand an explicit whole-patrol action to the selected package's full scenario set. Pending work alone is not evidence that the patrol was skipped. |
| No vehicle, one vehicle, or multiple vehicle roles | Declare the actual inputs needed by each procedure; do not require vehicle fields on administrative/setup-only procedures or derive roles from an ambiguous generic mode. |
| Human-facing evidence type and log template | Normalize the type to `manual_test` during source-side preparation, put instructions in `procedure.md`, and leave tester Notes empty. |

For the first usable session, a **Skip selected** action should create explicit
SKIPPED attempts with the same recorded reason and NOT_EXECUTED checks, using
normal attempt identities and finalization. A partly run patrol selects only
the unexecuted procedures; it must not replace results already captured. This
avoids inventing executable procedures whose only purpose is to encode a reason
for not running other procedures. The existing required evidence context still
applies; if it is unknown, retain the session annotation without fabricating a
SUT revision or producing incomplete evidence. Session annotations can record
the operational reason while the executable test results remain absent.

Extend the pilot review with a grouped commissioning procedure, per-day context,
and whole/partial-patrol skip handling. Verify that reordering checks does not
change answer associations and that recurring patrols start with fresh answers.
These are design additions for review; implementation follows that review.
