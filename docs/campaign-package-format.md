# Campaign package format and tools

The package builder and validator extend the
[procedure validator](campaign-procedure-validation.md) with an offline,
deterministic archive format. This is the implemented v2 authoring contract.
[Server import and CAS storage](campaign-import.md) are available. Execution
screens follow separately.

## Build and inspect a package

From the repository root:

```sh
go run ./cmd/campaign-package build internal/campaign/testdata/package > /tmp/campaign.tar.gz
go run ./cmd/campaign-package validate /tmp/campaign.tar.gz
```

`build` writes the archive to stdout and its `sha256:<hex>` digest to stderr.
It validates all selected inputs before writing archive bytes. The destination
should be outside the input directory, and callers should discard it if the
command fails (shell redirection creates a file even on failure).

`validate` accepts a filename or `-` for stdin. It emits JSON containing the
archive digest, manifest, ordered scenario definitions, parsed procedure blocks,
and setup asset digests. It makes no network requests and extracts no files.

```sh
go run ./cmd/campaign-package build internal/campaign/testdata/package | go run ./cmd/campaign-package validate -
bazel run //cmd/campaign-package -- validate - < /tmp/campaign.tar.gz
```

Exit codes: 0 for success, 1 for validation/I/O failure, 2 for incorrect arguments.
Use an absolute source-directory path with Bazel's `build` command because its
run directory differs from the invoking shell's directory.

## Manifest

`manifest.yaml` is at the archive root:

```yaml
format_version: 2
campaign:
  id: equipment-patrol
  name: Equipment patrol
source:
  repository: https://example.org/team/scenarios
scenarios:
  - procedure_ref: patrol/display
    path: scenarios/display/scenario.yaml
  - procedure_ref: patrol/recording
    path: scenarios/recording/scenario.yaml
```

The campaign ID/name, source repository and a nonempty scenario list are required.
Campaign keys match `[A-Za-z0-9][A-Za-z0-9_.-]*`. Procedure references and definition
paths must be unique within the package. A procedure reference is a nonempty
string without surrounding whitespace or control characters; Bazel labels such
as `//tests:display` are valid. It is an identity, not a filesystem path.
Scenario order is the order in the manifest, independent of archive order.
The optional `campaign.planned_location`, `planned_start`, and `planned_end`
describe the plan; dates are quoted `YYYY-MM-DD` strings, with end not before start.
Omit occurrence-specific dates for a reusable daily patrol.

The source repository is an absolute HTTPS URL without credentials, query, or
fragment. It namespaces scenario identities; it is never fetched by these tools.

### Procedure identity and older packages

The exact procedure identity is **(package digest, procedure_ref)**. There is no
separate scenario/procedure `id` in v2. Each manifest entry's `procedure_ref`
must match its definition. Renaming a reference creates a different identity
in a new immutable package. Logical/functional scenario IDs remain optional
traceability, and field/check IDs still identify individual execution answers.

Version 1 archives remain readable and importable. Their legacy IDs are checked
on input, then removed from the parsed representation and replaced with procedure
references in manifest entries. `format_version` still describes the original
archive. Migration 17 applies the same normalization to existing stored definitions;
it does not change their original CAS bytes, digest, UUID, or import attribution.
Reimporting an existing v1 archive remains idempotent. Building v1 source inputs
is retained for compatibility; new examples and authoring use v2. Older server
versions reject v2, so upgrade the server before uploading newly authored packages.

### Product revision association lives outside the archive

Following [#200](https://github.com/nesono/evidence_store/issues/200),
`source.revision` is rejected. The build/release system records an association
between the actual product revision and the generated package digest separately.
Two product revisions can then refer to byte-identical scenario packages.
Importer-side persistence of that association is a later implementation slice.

This supersedes the older embedded-revision examples in the original proposal.
The archive digest describes content; it does not prove who built it, which build
produced it, or which software was deployed. Those are separate provenance and
execution facts. The tools do not infer any of them from the current checkout.

## Scenario definition

```yaml
title: Check operator display
procedure_ref: patrol/display
description: procedure.md
parameters:
  observation_duration_s: 10
defaults:
  evidence_type: manual_test
  tags: [patrol]
  metadata:
    cluster: Commissioning
assets: [assets/setup.png]
traceability:
  - kind: requirement
    id: REQ-DISPLAY
    repository: https://example.org/team/specifications
    path: requirements/display.md#REQ-DISPLAY
    entry_version: "1"
```

Required: nonempty `title`, matching `procedure_ref`, `description`,
and `defaults.evidence_type` using the existing canonical enum. Procedure
references must be unique within a package. Optional maps `parameters` and
`defaults.metadata` hold JSON-compatible values. Optional tags are nonempty,
unique strings. Metadata defaults cannot set `campaign`, `procedure_modified`,
`notes`, `observations`, `result`, `finished_at`, `source`, `repo`, `branch`, or
`rcs_ref`: those are execution data. Typed prompts belong in the procedure.

The description is relative to `scenario.yaml`. Assets are an explicit list of
paths relative to the description; declaring a dependency avoids discovering
files through prose parsing or a recursive directory scan. Setup assets support
PNG, JPEG, GIF, and WebP, identified from their bytes. The validator checks media
type, not image decoding or visual correctness. Other local asset types are
rejected in this first version. Test-result media are a separate execution concern.

Procedures use the existing typed Markdown contract. Images must use
`![label](assets/setup.png)` and name a declared local asset. Reference-style or
other unsupported Markdown image syntax is rejected. Images inside fenced code
or inline code are examples, not dependencies. Required setup information must
travel in the procedure or its assets; external links are reference material,
not an offline availability guarantee. No HTML rendering happens in the validator;
future UI rendering must retain the existing safe Markdown restrictions.

Optional traceability links use `functional_scenario`, `logical_scenario`, or
`requirement` as `kind`, plus `id`, HTTPS `repository`, and repository-relative
`path` (optionally with a fragment). `entry_version` is an optional quoted
specification-entry version. Product/source commit revisions do not belong in
these links. Exact source-checkout provenance belongs with the external build
record; this validator checks link shape without resolving the referenced entry.

## Serialization and boundaries

- YAML documents are UTF-8 mappings with string keys and JSON scalar types.
  Unknown schema properties, nulls, duplicate keys, aliases/anchors, explicit
  tags, merge keys, nonfinite numbers, and multiple documents are rejected.
  Quote dates and numeric-looking IDs. Only the explicit metadata/parameter
  maps are open; generator/input provenance extensions are not implemented yet.
- Paths are canonical, relative POSIX paths: no `..`, `.`, absolute paths,
  repeated separators, backslashes, URL escaping, query/fragment syntax, or
  control characters. A definition or asset cannot escape its owner's directory.
- Archive reads accept regular files and directories. Links, special files,
  PAX metadata, duplicate paths, and file/directory conflicts are rejected.
  Unselected files in an uploaded archive are rejected. Empty directories may
  be present but are not emitted by the builder.
- Directory builds read only the declared files, reject symbolic-link inputs,
  and use an `os.Root` handle to confine reads. Build from an immutable source
  snapshot; concurrent source edits are not a reproducible input.
- Limits: 16 MiB compressed archive, 64 MiB expanded archive including tar
  overhead, 2,048 entries, 8 MiB per file, and 1 MiB per YAML/Markdown document.
  Existing control/declaration limits apply within each procedure.
- The gzip checksum and end of stream are checked. Multiple gzip members,
  trailing compressed data, and nonzero data after the tar terminator are rejected.

## Reproducibility

The builder sorts file paths, preserves file contents exactly, emits USTAR
regular-file headers with mode 0644, uid/gid 0, empty owner names and mtime 0,
and uses Go gzip best compression with an empty name/comment and zero header
time. It emits no directory entries. Paths must fit the USTAR representation.

Pin the Go toolchain used for package creation; different compressor versions
are not promised byte-identical output. Input file permissions, modification
times, unrelated source files, and map insertion order cannot affect the archive.
YAML formatting is part of the content: differently formatted but semantically
equivalent input produces a different digest. Source-side generators must keep
their serialization deterministic. The validator hashes the received bytes and
does not rewrite noncanonical archives to pretend they were the builder's output.

## Verification

```sh
go test ./internal/campaign ./cmd/campaign-package ./cmd/campaign-validate
bazel test //internal/campaign:campaign_test //cmd/campaign-package:campaign_package_test
```

Tests cover deterministic headers and bytes, round-trip parsing, changed-content
digests, input selection, invalid definitions, missing/unsupported assets, path
escapes and links, duplicate/conflicting entries, corrupt/trailing gzip streams,
and expansion limits. The server consumes the same validation boundary to publish
an immutable package with transactional metadata and explicit blob references
for the archive and setup assets; see [campaign import](campaign-import.md).
