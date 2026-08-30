// Unit tests for web/static/evidencetype.js, run with `node --test`.
//
// A closed set of three that two other modules depend on, and the reason it is
// closed is in DESIGN.md §2.2: the column once held `bazel`, `pytest`, `gotest`
// and `junit` — four spellings of "a machine ran it" that no query could treat
// as one thing.

import test from "node:test";
import assert from "node:assert/strict";

import {
  DEFAULT_EVIDENCE_TYPE, EVIDENCE_TYPES, evidenceTypeLabel, evidenceTypeOr,
} from "../static/evidencetype.js";

test("the set is the three the store accepts", () => {
  // The API rejects anything else and a CHECK constraint keeps the column from
  // drifting, so this list disagreeing with the server is a bug on this side.
  assert.deepEqual(EVIDENCE_TYPES, ["ci", "manual_test", "demonstration"]);
});

test("every type has a label, and the default is one of them", () => {
  for (const type of EVIDENCE_TYPES) {
    assert.ok(evidenceTypeLabel(type), `${type} has no label`);
    assert.notEqual(evidenceTypeLabel(type), type, `${type} shows its slug rather than a label`);
  }
  assert.ok(EVIDENCE_TYPES.includes(DEFAULT_EVIDENCE_TYPE));
});

test("a type from before the taxonomy shows itself rather than a blank", () => {
  // A record written around the API, or by a store that has not run migration
  // 000006, still has to render as something.
  assert.equal(evidenceTypeLabel("bazel"), "bazel");
  assert.equal(evidenceTypeLabel(""), "");
  assert.equal(evidenceTypeLabel(undefined), "");
  assert.equal(evidenceTypeLabel(null), "");
});

test("a select is only ever given a value it has", () => {
  // Templates were saved when the field was free text, so one can still hold
  // `bazel`. A value the control does not have leaves it blank and the form
  // unsubmittable.
  assert.equal(evidenceTypeOr("ci"), "ci");
  assert.equal(evidenceTypeOr("bazel"), DEFAULT_EVIDENCE_TYPE);
  assert.equal(evidenceTypeOr(""), DEFAULT_EVIDENCE_TYPE);
  assert.equal(evidenceTypeOr(undefined), DEFAULT_EVIDENCE_TYPE);
  assert.equal(evidenceTypeOr("bazel", "ci"), "ci", "the caller may choose the fallback");
});
