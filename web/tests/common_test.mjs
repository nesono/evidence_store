// Unit tests for the shared helpers in web/static/common.js.
//
// Only the ones that can be tested here: `esc` builds a detached element to do
// its escaping, so it needs a document and is left to the browser smoke test.

import test from "node:test";
import assert from "node:assert/strict";

import { formatTime, resultBadge } from "../static/common.js";

test("a time is shown in UTC, whatever this machine thinks the time is", () => {
  // Every record's finished_at and ingested_at goes through here. Rendering one
  // in local time would make two people reading the same record disagree about
  // when the test ran.
  assert.equal(formatTime("2026-08-25T21:50:31Z"), "2026-08-25 21:50");
  assert.equal(formatTime("2026-08-25T23:50:00+02:00"), "2026-08-25 21:50",
    "an offset is converted, not ignored");
});

test("every part of a time is padded", () => {
  // So that a column of them lines up and sorts as text the way it sorts as
  // time.
  assert.equal(formatTime("2026-01-02T03:04:00Z"), "2026-01-02 03:04");
});

test("a verdict is badged by its own name", () => {
  for (const result of ["PASS", "FAIL", "ERROR", "SKIPPED"]) {
    const html = resultBadge(result);
    assert.match(html, new RegExp(`badge-${result.toLowerCase()}`),
      `${result} should carry its own class`);
    assert.match(html, new RegExp(`>${result}<`));
  }
});

test("a record with no verdict still renders as something", () => {
  // A row that renders nothing is a row somebody reads straight past.
  assert.match(resultBadge(""), /badge-unknown/);
  assert.match(resultBadge(undefined), /\?/);
});
