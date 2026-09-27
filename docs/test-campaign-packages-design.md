# Test Campaign Packages — Design Agreement for #127

Related issue: [#127 — Add test campaign support](https://github.com/nesono/evidence_store/issues/127).

## Goal

Introduce **Test Campaign Packages** for in-vehicle testing.

A test campaign is an immutable, portable collection of already-expanded
**Concrete Scenarios** that can be imported into the Evidence Store and
executed by a tester.

The initial implementation for #127 should deliberately remain small.
Additional execution semantics, validation, provenance, and workflow features
can be added later.

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

A possible package structure is:

    vehicle-campaign-2026-09.tar.gz

    manifest.yaml

    scenarios/
      network-loss-023/
        scenario.yaml
        setup.md
        execution.md
        observations.yaml
        assets/
          network-setup.png

      emergency-stop-012/
        scenario.yaml
        setup.md
        execution.md
        observations.yaml
        assets/
          test-area.png

The exact archive and serialization formats are implementation details.

---

## Manifest

The package contains a manifest describing the campaign and its scenarios.

For example:

    campaign:
      id: vehicle-release-2026-09
      name: Vehicle Release Test Campaign
      format_version: 1

    source:
      repository: land-scenarios
      revision: a81f9c2

    scenarios:
      - id: network-loss-023
        path: scenarios/network-loss-023

      - id: emergency-stop-012
        path: scenarios/emergency-stop-012

The package contains already-expanded Concrete Scenarios.

The Evidence Store therefore does not require information about the expansion
strategy or original parameter ranges.

Such information may optionally be included as opaque provenance metadata.

---

## Instructions

A Concrete Scenario can contain human-readable instructions.

### Setup

`setup.md` describes preparation of the test.

For example:

    # Setup

    1. Position the vehicle in the designated test area.
    2. Verify E-Stop functionality.
    3. Establish the teleoperation connection.
    4. Verify the configured network conditions.

### Execution

`execution.md` describes how the test should be performed.

For example:

    # Execution

    1. Accelerate to the specified test speed.
    2. Maintain constant velocity.
    3. Trigger the network interruption.
    4. Observe the vehicle behavior.
    5. Wait until the vehicle reaches a safe state.

Markdown allows instructions to reference supporting assets such as images.

---

## Observations and Measurements

Information that needs to be collected during a test should be represented
structurally rather than embedded into Markdown.

For example:

    observations:
      - id: vehicle_stopped
        type: choice
        question: Did the vehicle come to a stop?
        options:
          - yes
          - no

      - id: stopping_distance
        type: measurement
        question: Stopping distance
        unit: m

      - id: notes
        type: text
        question: Additional observations

For #127, the observation model should remain deliberately small.

More sophisticated validation, additional control types, ranges, etc. can be
introduced later.

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

---

## Execution State

For the initial #127 implementation, keep execution state deliberately simple.

At minimum, a scenario within a Campaign Execution needs to support:

- pending
- completed

More sophisticated distinctions between execution state and test result
(e.g. running, passed, failed, aborted, skipped) can be introduced later.

We should avoid prematurely designing a comprehensive test-result state
machine as part of #127.

---

## Reruns

A Concrete Scenario and an execution of that scenario are conceptually
different things.

A Concrete Scenario may be executed more than once.

For #127, reruns should be supported without introducing an unnecessarily
complex attempt/result model.

In particular, we do **not** need to solve yet:

- how multiple attempts determine an overall result
- whether the latest attempt wins
- how failures followed by successful reruns are summarized
- sophisticated pass/fail semantics

The data model should simply avoid making future support for these concepts
unnecessarily difficult.

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

---

## Provenance

Campaign packages preserve the origin of their scenarios.

At minimum:

    source:
      repository: land-scenarios
      revision: a81f9c2

This identifies the source repository revision from which the campaign was
created.

The Evidence Store does not need to interpret how the scenarios were
generated.

---

## Future SUT Traceability

In the future, Campaign Executions should also be associated with the revision
of the System Under Test.

This is **not part of #127**.

Eventually we want traceability such as:

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
- source repository/revision provenance
- Campaign Execution separate from Campaign
- pending/completed execution tracking
- changing scenario execution order
- associating evidence with executions
- basic rerun support

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
- detailed attempt semantics
- SUT revision binding
- comprehensive package versioning/migration strategy
- advanced duplicate/import semantics

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
