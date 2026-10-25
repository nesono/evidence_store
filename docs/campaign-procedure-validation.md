# Campaign procedure validation

The first campaign implementation provides a procedure validator, usable by
authors before archive import and execution screens are added. The contract
comes from the [implementation plan](test-campaign-implementation-plan.md).

## Try it

From the repository root:

```sh
go run ./cmd/campaign-validate internal/campaign/testdata/simple.md
go run ./cmd/campaign-validate internal/campaign/testdata/measurement.md
go run ./cmd/campaign-validate internal/campaign/testdata/grouped.md
```

Or use the Bazel target, reading the procedure on standard input so file paths
do not depend on Bazel's run directory:

```sh
bazel run //cmd/campaign-validate -- - < internal/campaign/testdata/grouped.md
```

The command emits JSON to stdout and diagnostics to stderr. Exit status is 0
for success, 1 for invalid input or an I/O error, and 2 for incorrect arguments.
It accepts exactly one file path or `-` for stdin. Input is limited to 1 MiB,
with at most 1,000 controls and 64 KiB per declaration.

## Representation

Output has `schema_version: 1`, the exact original `source`, and ordered
`blocks`. Each block has a one-based source `line` and one of:

- `kind: markdown` with the untouched `markdown` text;
- `kind: field` with a typed `field` declaration;
- `kind: check` with a `check` declaration.

Field IDs are unique across both fields and checks. Ordinary fields default to
`required: false`; declared defaults retain their JSON type. Missing defaults
are omitted, including for all checks and the bound overall result. Responses
are not generated. Consumers must still render Markdown safely and treat all
labels and field values as data.

The parser validates text, choice, measurement, and evidence declarations;
`expect` checks; and a single optional `execution.result` binding with the four
existing result choices. It rejects unsupported properties, inapplicable
properties, invalid defaults, duplicate YAML keys, aliases/anchors, explicit
tags, merge keys, and multiple YAML documents. There is no coercion from
numbers or booleans into string options or text defaults.

Control fences must be exactly ` ```test-field ` or ` ```test-check ` (without
the surrounding spaces shown here), at the start of a line, closed by exactly
three backticks on their own line. Both LF and CRLF input are accepted. Other
fenced code is ordinary Markdown; examples inside longer backtick fences or
tilde fences do not become live controls. Unknown `test-*` fence names and
indented/malformed control fences are rejected outside ordinary fenced code.
Declarations embedded in blockquotes or list text are not top-level controls.

## Implementation boundaries and next step

This validates procedure declarations, not campaign archives, execution answers,
asset availability, or test results. No server endpoints or database migrations
are introduced. The sample procedures are generic format fixtures.

Next, integrate this parser with package validation/import and asset lifetime
tracking, then add procedure selection and draft capture. Before implementing
the archive manifest, reconcile the plan with
[#200](https://github.com/nesono/evidence_store/issues/200): revision-to-package
associations belong outside content-addressed archives. This parser does not
encode a manifest or revision association, so it does not lock in the earlier
manifest example.

Tests:

```sh
go test ./internal/campaign ./cmd/campaign-validate
bazel test //internal/campaign:campaign_test //cmd/campaign-validate:campaign_validate_test
```
