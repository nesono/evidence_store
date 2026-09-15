// Unit tests for remembering the last run on this device (#162), run with
// `node --test`.
//
// The next record at a rig is nearly always about the same repository, branch,
// build and procedure. Those come back into an empty form; nothing else does,
// and nothing is ever typed over.

import test from "node:test";
import assert from "node:assert/strict";

import { LAST_RUN_KEY, fieldsToRestore, loadLastRun, pickLastRun, rememberLastRun } from "../static/lastrun.js";

const memory = () => {
  const data = new Map();
  return { getItem: k => (data.has(k) ? data.get(k) : null), setItem: (k, v) => data.set(k, String(v)), data };
};

test("only the run's context is remembered, not the result or the log", () => {
  const record = {
    repo: "org/firmware", branch: "main", rcs_ref: "abc123", procedure_ref: "manual/brake",
    result: "FAIL", source: "user:alice", metadata: { observations: "smoke" },
  };
  assert.deepEqual(pickLastRun(record),
    { repo: "org/firmware", branch: "main", rcs_ref: "abc123", procedure_ref: "manual/brake" });
});

test("empty and missing values are not remembered", () => {
  assert.deepEqual(pickLastRun({ repo: "org/firmware", branch: "  ", rcs_ref: "" }), { repo: "org/firmware" });
  assert.deepEqual(pickLastRun(null), {});
});

test("a remembered value fills an empty field and never overwrites a typed one", () => {
  const saved = { repo: "org/firmware", branch: "main", rcs_ref: "abc123", procedure_ref: "manual/brake" };
  assert.deepEqual(fieldsToRestore(saved, { repo: "", branch: "release", rcs_ref: "  ", procedure_ref: undefined }),
    { repo: "org/firmware", rcs_ref: "abc123", procedure_ref: "manual/brake" });
});

test("it round-trips through storage", () => {
  const storage = memory();
  rememberLastRun({ repo: "org/firmware", procedure_ref: "manual/brake", result: "PASS" }, storage);
  assert.deepEqual(loadLastRun(storage), { repo: "org/firmware", procedure_ref: "manual/brake" });
});

test("storage that refuses, or holds something else, costs nothing", () => {
  const refusing = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("blocked"); } };
  assert.doesNotThrow(() => rememberLastRun({ repo: "org/firmware" }, refusing));
  assert.deepEqual(loadLastRun(refusing), {});

  const corrupt = memory();
  corrupt.setItem(LAST_RUN_KEY, "{not json");
  assert.deepEqual(loadLastRun(corrupt), {});
  corrupt.setItem(LAST_RUN_KEY, "42");
  assert.deepEqual(loadLastRun(corrupt), {});
});
