// Unit tests for naming what a form is missing, run with `node --test`.
//
// Tried on a phone, a result form with one field left empty looked like a
// Submit button that did nothing: the browser's hint was off-screen or not
// shown at all. The form now lists every missing field under its buttons;
// which fields count, and how their names are joined, are tested here.

import test from "node:test";
import assert from "node:assert/strict";

import { joinSeparator, missingGroups } from "../static/formcheck.js";

const field = (name, { valid = true, type = "text", willValidate = true } = {}) =>
  ({ name, type, willValidate, validity: { valid } });

test("a form with everything filled in is missing nothing", () => {
  assert.deepEqual(missingGroups([field("repo"), field("rcs_ref")]), []);
});

test("every missing field is listed, in form order, not just the first", () => {
  const groups = missingGroups([field("repo", { valid: false }), field("branch"), field("rcs_ref", { valid: false })]);
  assert.deepEqual(groups.map(g => g.elements[0].name), ["repo", "rcs_ref"]);
});

test("a radio group is one missing field, however many buttons it has", () => {
  const radios = ["PASS", "FAIL", "ERROR", "SKIPPED"].map(() => field("result", { valid: false, type: "radio" }));
  const groups = missingGroups([...radios, field("procedure_ref", { valid: false })]);
  assert.equal(groups.length, 2);
  assert.equal(groups[0].elements.length, 4, "all four buttons are marked");
});

test("fields the browser does not validate are left out", () => {
  // A button, a fieldset, a disabled field: willValidate is false for all of them.
  assert.deepEqual(missingGroups([field("add-another", { valid: false, willValidate: false })]), []);
});

test("names join the way a sentence would", () => {
  const join = names => names.map((n, i) => joinSeparator(i, names.length) + n).join("");
  assert.equal(join(["Result"]), "Result");
  assert.equal(join(["Result", "Commit"]), "Result and Commit");
  assert.equal(join(["Result", "Commit", "Repo"]), "Result, Commit and Repo");
});
