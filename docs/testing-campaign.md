# Campaign for testing: Vehicle feature tour

Use this example for campaign UX, offline execution and result review. The older
`internal/campaign/testdata/package` is a minimal v2 parser fixture, not the testing
campaign. The new example has **15 concrete scenarios**, an actual packaged setup
image, and product-build provenance.

## Build and import

From the repository root, explicitly identify the product commit being tested:

```sh
go run ./cmd/campaign-package example \
  --repository https://github.com/nesono/evidence_store \
  --branch "$(git branch --show-current)" \
  --commit "$(git rev-parse HEAD)" \
  --output /tmp/vehicle-feature-tour.tar.gz
go run ./cmd/campaign-package validate /tmp/vehicle-feature-tour.tar.gz
```

For a real product build, pass that product repository's URL, branch and full commit
instead. These are explicit build inputs; the tool never guesses a product revision.
The example's vehicle behavior is illustrative simulator test content, not a
claim that Evidence Store implements a vehicle simulator or that these thresholds
are validated real-vehicle acceptance criteria.

1. In **Campaigns → Campaign packages**, import `vehicle-feature-tour.tar.gz`.
2. Verify its **Product commit**. Import it again: the existing package opens with
   **already imported**, and there is only one package listing.
3. Click **Start an execution**. Repository, branch and deployed revision default
   to the package build. Review them and explicitly press **Start campaign**.
4. Start another execution if desired: the same package may have multiple runs,
   including repeated runs against the same commit.
5. To test another product build, generate the example with its different commit.
   It is a different package even if its procedure definitions are identical.

The archive is deterministic for the same inputs/toolchain. Reimporting equivalent
normalized content (including old v1/v2 encodings or header/YAML formatting changes)
returns the earliest stored package and its original archive digest. Package content
and product provenance both participate in equivalence. Existing historical IDs and
executions are preserved; equivalent package rows are collapsed only in listings.

## Cases

### Readiness

**01 · Simulator readiness and build check** captures operator, displayed build,
configuration choice and two checks. Compare the displayed revision with the
execution context. Exercises required text, choice defaults and independent checks.

### Operator-demo

**02 · Demonstrate operator display and recording** uses the `demonstration`
evidence type, state/recording checks, optional external evidence links, multiline
observations and test-log screenshots/videos.

### Sensor-recovery

**03 · Sensor dropout and recovery** fixes a two-second dropout parameter and
captures a recovery-time measurement plus degraded/recovered checks. Also useful
for NOT_EXECUTED reasons and overall SKIPPED when a simulator capability is absent.

### Stopping-sweep

**3 speeds × 2 loads = 6 concrete scenarios**:

| Speed | Empty limit | Loaded limit |
|---|---|---|
| 5 km/h | 2 m | 3 m |
| 10 km/h | 4 m | 5 m |
| 15 km/h | 6 m | 7 m |

Each case has a unique stable reference such as
`feature-tour/stopping-10-loaded`, fixed parameter values, actual speed/distance
measurements, and separate stability/distance checks.

### Detection-sweep

**2 lighting conditions × 3 target classes = 6 concrete scenarios**:

- Lighting: day, dusk.
- Target: pedestrian, cone, vehicle.
- Fixed distance: 20 m; ten sampled frames per case.
- Illustrative target: at least nine detections, median latency at most 150 ms,
  and the expected classification.

Each case captures detection count, latency and observed classification, plus
checks and optional screenshots. Example reference: `feature-tour/detection-dusk-cone`.

## What to exercise

The matrices expand **at package build time** in `internal/campaign/example.go`.
The runtime executes ordinary concrete scenarios; every combination has its own
draft, retries, checks and result. No template expansion or code execution happens
on import, and numeric measurements do not automatically decide the verdict.

- Save an unfinished measured case, navigate away and resume it.
- Finalize one PASS and one FAIL, documenting the failed check in notes.
- Retry a failed combination; confirm both attempts remain visible.
- Prepare offline, disconnect/reload, attach a photo and complete another combination.
- Reconnect and synchronize; verify the parameter values and build provenance in
  the resulting evidence's campaign metadata.
- Close a partially finished execution: unfinished cases become SKIPPED while
  observations survive. Reopen and retry selected combinations.
- Use two browser profiles to exercise explicit conflicting draft resolution.

See [offline execution](campaign-offline.md) for connectivity and storage details.
