// Unit tests for web/static/evidencetype.js, run with `node --test`.
//
// A closed set of three that two other modules depend on, and the reason it is
// closed is in DESIGN.md §2.2: the column once held `bazel`, `pytest`, `gotest`
// and `junit` — four spellings of "a machine ran it" that no query could treat
// as one thing.

import test from "node:test";
import assert from "node:assert/strict";

import { EVIDENCE_TYPES, EVIDENCE_TYPE_LABELS, evidenceTypeLabel } from "../static/evidencetype.js";

test("the set is the three the store accepts", () => {
  // The API rejects anything else and a CHECK constraint keeps the column from
  // drifting, so this list disagreeing with the server is a bug on this side.
  assert.deepEqual(EVIDENCE_TYPES, ["ci", "manual_test", "demonstration"]);
});

test("every type has a label of its own", () => {
  for (const type of EVIDENCE_TYPES) {
    assert.ok(evidenceTypeLabel(type), `${type} has no label`);
    assert.notEqual(evidenceTypeLabel(type), type, `${type} shows its slug rather than a label`);
  }
  assert.deepEqual(Object.keys(EVIDENCE_TYPE_LABELS).sort(), [...EVIDENCE_TYPES].sort(),
    "a label for a type that does not exist, or a type with no label, is a set that has drifted");
});

test("a type from before the taxonomy shows itself rather than a blank", () => {
  // A record written around the API, or by a store that has not run migration
  // 000006, still has to render as something.
  assert.equal(evidenceTypeLabel("bazel"), "bazel");
  assert.equal(evidenceTypeLabel(""), "");
  assert.equal(evidenceTypeLabel(undefined), "");
  assert.equal(evidenceTypeLabel(null), "");
});
