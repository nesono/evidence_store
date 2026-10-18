# Test Campaign Packages — Design for #127 and #199

Related issue: [#127 — Add test campaign support](https://github.com/nesono/evidence_store/issues/127).

Updated proposal following [#199](https://github.com/nesono/evidence_store/issues/199)
(procedure selection and inputs), [#198](https://github.com/nesono/evidence_store/issues/198)
(check progress), and [#201](https://github.com/nesono/evidence_store/issues/201)
(independent evidence-search sorting). Campaign support is not implemented yet.
The [first implementation plan](test-campaign-implementation-plan.md) specifies
the proposed v1 format, field contract, delivery slices, and a worked example.
These refinements supersede the earlier separate `observations.yaml` proposal.

## Goal

Introduce **Test Campaign Packages** for in-vehicle testing.

A test campaign is an immutable, portable collection of already-expanded
**Concrete Scenarios** that can be imported into the Evidence Store and
executed by a tester.

The initial implementation for #127 should deliberately remain small.
Start with package import and procedure selection, then add durable campaign
sessions. Basic input validation, definition provenance, and per-check outcomes
belong in this first usable workflow; advanced execution semantics can follow.

---

## Architectural Boundary

Scenario development remains outside the Evidence Store.

The source repository owns:

- Functional Scenarios
- Logical Scenarios
- Concrete Scenarios
- parameter definitions
- logical-to-concrete parameter expansion
- modality-specific reduction strategies
- selection of scenarios for a campaign
- creation of the Test Campaign Package

The Evidence Store owns:

- importing Test Campaign Packages
- storing imported campaigns
- executing campaigns
- execution progress
- execution order
- observations and measurements
- evidence
- reruns

Conceptually:

    Scenario Repository                  Evidence Store
    ───────────────────                  ──────────────

    Functional Scenario
            │
            ▼
    Logical Scenario
            │
            │ parameter expansion
            ▼
    Concrete Scenario
            │
            │ selection / packaging
            ▼
    Test Campaign Package ──────────────► Campaign
                                             │
                                             ▼
                                      Campaign Execution
                                             │
                                             ▼
                                  observations / measurements
                                             │
                                             ▼
                                           evidence

The Evidence Store does **not** need to understand how Concrete Scenarios
were generated.

---

## Scenario Abstraction Levels

Scenario definitions have three abstraction levels:

    Functional Scenario
            │
            │ decomposition
            ▼
    Logical Scenario
            │
            │ parameter expansion
            ▼
    Concrete Scenario

### Functional Scenario

Describes functionality or a group of related behavior at a relatively high
level.

A Functional Scenario can effectively act as a scenario suite.

### Logical Scenario

Describes an executable test situation but still contains parameter spaces or
parameter placeholders.

For example:

    speed:       [5, 10, 20, 30]
    latency:     [0, 50, 100, 200, 500]
    packet_loss: [0, 1, 5, 10, 20]

### Concrete Scenario

Contains one fully resolved parameter set.

For example:

    speed: 20
    latency: 100
    packet_loss: 5

A Concrete Scenario represents a **test configuration**, not an execution of
that test.

The same Concrete Scenario can therefore be executed more than once.

---

## Parameter Expansion

Parameter expansion happens entirely in the source repository.

Different test modalities can use different expansion strategies.

For example:

    Logical Scenario
          │
          ├──────────────┬──────────────┐
          ▼              ▼              ▼
      Simulation        HiL          Vehicle
          │              │              │
      Cartesian       Coverage       Strongly
       product        reduction       reduced
          │              │              │
          └──────────────┴──────────────┘
                         │
                         ▼
                 Concrete Scenarios

Simulation may use the complete Cartesian product.

HiL testing may use coverage-preserving parameter-space reduction.

Vehicle testing will likely use an even stronger reduction because physical
testing is expensive.

The algorithms and rules for these expansions are **out of scope for #127**.

Following #199, expansion is an explicit source-repository operation: generate
concrete definitions and stable Bazel target declarations, review and commit
them, and have CI check regeneration for drift. A package build consumes these
committed definitions. An ordinary Bazel action cannot introduce new targets
into the build graph that is already being executed.

Concrete identities must not depend on enumeration order. Adding another
parameter combination must not renumber existing procedures. A manually
reviewed, already-concrete definition is sufficient for the first package;
a general expansion engine is not a prerequisite.

All expansion methods produce the same downstream abstraction:

**Concrete Scenarios**

---

## Test Campaign Package

A Test Campaign Package selects Concrete Scenarios for an in-vehicle test
campaign.

The package is:

- portable
- immutable
- self-contained enough to execute independently of subsequent source
  repository changes

A proposed v1 package structure is:

    vehicle-campaign-2026-10.tar.gz

    manifest.yaml

    scenarios/
      network-loss-nominal/
        scenario.yaml
        procedure.md
        assets/
          network-setup.png

      emergency-stop-nominal/
        scenario.yaml
        procedure.md
        assets/
          test-area.png

Use a deterministic tar.gz, UTF-8 YAML metadata, and Markdown with typed
`test-field` / `test-check` fences beside the relevant instructions. Responses
never go into the package. Keep one observation declaration mechanism.

Pin source inputs, generator/toolchain versions, file ordering, and archive
metadata. Identical declared inputs must produce byte-identical archives.
The archive SHA-256 identifies the exact imported package; a campaign's
human-readable ID is a grouping label, not a mutable pointer to its latest
definition. Reimporting the same bytes returns the same package.

---

## Manifest

The package contains a manifest describing the campaign and its scenarios.

For example:

    format_version: 1
    campaign:
      id: vehicle-release-2026-10
      name: Vehicle Release Test Campaign
      planned_location: Proving ground
      planned_start: "2026-10-05"
      planned_end: "2026-10-05"

    source:
      repository: https://example.org/team/scenarios
      revision: "0123456789abcdef0123456789abcdef01234567"

    scenarios:
      - id: network-loss-nominal
        path: scenarios/network-loss-nominal/scenario.yaml

      - id: emergency-stop-nominal
        path: scenarios/emergency-stop-nominal/scenario.yaml

The revision above is illustrative. Production packages use the full actual
commit. List order supplies the initial execution order. Planned dates and
location describe the plan, not when or where an execution actually happened.

The package contains already-expanded Concrete Scenarios.

The Evidence Store therefore does not require information about the expansion
strategy or original parameter ranges.

Such information may optionally be included as opaque provenance metadata.

---

## Instructions

A Concrete Scenario can contain human-readable instructions.

### Setup and execution

`procedure.md` describes preparation, execution, and acceptance criteria, with
structured input declarations placed where the tester needs them.

For example:

    # Setup

    1. Position the vehicle in the designated test area.
    2. Verify E-Stop functionality.
    3. Establish the teleoperation connection.
    4. Verify the configured network conditions.

The execution section describes how the test should be performed.

For example:

    # Execution

    1. Accelerate to the specified test speed.
    2. Maintain constant velocity.
    3. Trigger the network interruption.
    4. Observe the vehicle behavior.
    5. Wait until the vehicle reaches a safe state.

Markdown allows instructions to reference supporting assets such as images.

Selecting a packaged procedure initializes its execution copy, evidence type,
tags, and metadata. Testers fill declared inputs normally. **Edit procedure**
is required to change instructions, field definitions, or prescribed defaults;
saving that edit records `procedure_modified: true`. Preserve the effective
copy with the execution and never overwrite the imported definition.

Notes remain tester-only observations or commentary, not a prefilled copy of
the instructions. Existing free-text procedure entry remains available, and
search continues to find historical procedure references outside a selected
package. Source definitions are never fetched from GitHub at runtime.

---

## Observations and Measurements

Declarations are machine-readable YAML inside typed Markdown fences. Answers
are separate structured execution data keyed by stable field ID. For example:

````markdown
Observe the stop and record the measured distance.

```test-check
id: vehicle_stopped
label: Vehicle comes to a complete stop
behavior: expect
```

```test-field
id: stopping_distance
type: measurement
label: Measured stopping distance
unit: m
```
````

The v1 proposal supports text, choices, measurements, evidence references, and
checks. Validate identifiers, types, options, units, and bindings. An unanswered
check is distinct from `PASS`, `FAIL`, and explicitly `NOT_EXECUTED`. There is
no automatic PASS default and no new core `PARTIAL` verdict. The first version
supports non-aborting `expect` checks; `assert` and branching are deferred
rather than accepted with misleading no-op behavior.

See the implementation plan for the exact response shape, finalization rules,
and limits on comparing check progress across changed definitions.

---

## Campaign Definition vs. Campaign Execution

The campaign definition and its execution state are separate concepts.

The campaign describes:

**What shall be tested.**

The execution describes:

**What happened while testing it.**

Therefore:

    Campaign
       │
       ├── Execution A
       │
       └── Execution B

The imported campaign remains immutable.

Execution state is mutable.

This allows the same campaign to be executed multiple times.

---

## Execution Order

Execution order belongs to the Campaign Execution, not to the Concrete
Scenarios.

The package may provide an initial/default order.

During execution, the tester can reorder tests.

Changing the execution order must not modify:

- the imported campaign
- the Test Campaign Package
- the Concrete Scenarios

Evidence-search sorting (#201) is independent: it orders query results across
pages and must never update a campaign session's scenario order.

---

## Execution State

For the initial #127 implementation, keep execution state deliberately simple.

Each planned scenario has zero or more attempts. A scenario is pending until
an attempt is finalized, then completed for planning purposes; completed does
not mean passed. An open draft can be shown as in progress. A later rerun must
not erase a previous attempt or its verdict.

An attempt draft has no overall result by default. Finalization uses the
existing `PASS`, `FAIL`, `ERROR`, or `SKIPPED` result. Individual checks explain
progress. Unstarted scenarios create no evidence; explicitly skipped ones may
be finalized as `SKIPPED` with a reason. Keep workflow state and verdict
separate. Interrupted or ambiguous legacy results need explicit assessment,
not a mechanical conversion from `PARTIAL` to PASS or FAIL.

---

## Reruns

A Concrete Scenario and an execution of that scenario are conceptually
different things.

A Concrete Scenario may be executed more than once.

For #127, a rerun creates a fresh attempt ID and, on finalization, a fresh
evidence submission ID. Re-sending an attempt uses its existing submission ID.
Retain all finalized attempts; default to the original procedure definition
and empty results when starting again. Copying a modified procedure must be
an explicit action and preserves the modification flag.

In particular, we do **not** need to solve yet:

- how multiple attempts determine an overall result
- whether the latest attempt wins
- how failures followed by successful reruns are summarized
- sophisticated pass/fail semantics

The session shows attempt history and counts rather than computing an overall
campaign verdict or treating the latest attempt as authoritative.

---

## Evidence

Evidence belongs to execution rather than to the immutable scenario
definition.

Evidence can include:

- images
- videos
- logs
- telemetry
- generated reports
- arbitrary supporting files

Existing Evidence Store functionality should be reused where appropriate.
The first implementation uses supported media uploads and external evidence
links; arbitrary local file uploads require a separate extension.

---

## Provenance

Campaign packages preserve the origin of their scenarios.

At minimum:

    source:
      repository: https://example.org/team/scenarios
      revision: "0123456789abcdef0123456789abcdef01234567"

This identifies the source repository revision from which the campaign was
created.

The Evidence Store does not need to interpret how the scenarios were
generated.

When an external catalogue is the authoring master, record its version and
content digest as a declared generation input. Include the reviewed executable
definition and any required assets. A link to a changing workbook is not an
executable snapshot. Requirement/FSC/LSC references are source-authored
traceability, not relationships inferred by Evidence Store from similar names.

---

## SUT Context and Future Release Traceability

The existing evidence API already requires `repo`, `branch`, and `rcs_ref`.
Capture those as the actual software under test, with reusable session
defaults and per-attempt confirmation. Never fill the SUT revision from the
scenario package's source revision. Record vehicle and operator-station
identities as execution context, including per-vehicle revisions if they differ.

Automatic deployed-build discovery and release-to-package binding remain
future work. Basic existing evidence fields are needed for the first version.

The resulting traceability is:

    Scenario repository revision
              │
              ▼
        Concrete Scenario
              │
              ▼
        Campaign Package
              │
              ▼
       Campaign Execution
              │
              ├── SUT revision
              ├── observations
              ├── measurements
              └── evidence

The scenario/source revision belongs to the campaign provenance.

The SUT revision belongs to the execution.

This allows the same campaign to be executed against different product
revisions.

---

## Scope for #127

Keep the first implementation intentionally limited.

### In scope

- Test Campaign Package concept
- package manifest
- importing a campaign package
- immutable imported campaign
- Concrete Scenarios within a campaign
- setup instructions
- execution instructions
- supporting assets
- simple structured observations and measurements
- inline typed field/check declarations and separate answers
- procedure picker, autofill, and edited execution copies
- source repository/revision provenance
- deterministic package production and digest-based import identity
- Campaign Execution separate from Campaign
- pending/completed execution tracking
- durable drafts and offline preparation for field use
- changing scenario execution order
- associating evidence with executions
- basic rerun support
- existing SUT fields and per-run context

### Explicitly out of scope

- Functional → Logical decomposition
- Logical → Concrete parameter expansion
- Cartesian-product generation
- HiL parameter-space reduction
- vehicle parameter-space reduction
- mathematical coverage criteria
- automatic selection of Concrete Scenarios
- sophisticated observation validation
- sophisticated execution/result state machines
- deriving overall results from multiple reruns
- automatic abort/branching and advanced attempt aggregation
- automatic deployed-build discovery and release binding
- comprehensive package versioning/migration strategy
- advanced package migration and replacement semantics

These can be introduced incrementally when concrete requirements emerge.

---

## Guiding Principle

Do not solve hypothetical future complexity as part of #127.

The implementation should establish the fundamental separation:

    Scenario definition
           ↓
    Concrete Scenario
           ↓
    immutable Campaign
           ↓
    mutable Campaign Execution
           ↓
    observations / measurements / evidence

while keeping the model straightforward enough to extend later.
