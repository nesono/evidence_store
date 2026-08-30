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

// --- Logging out ---

// The bug this guards: with authentication configured, an anonymous request is
// a 401, and every view opens with one. Treating that 401 as an expired session
// sent the browser back to the identity provider, which still had its own
// session and signed the person straight back in — so the logout button
// appeared to do nothing at all.
test("a page reached by logging out knows it", async () => {
  assert.equal(await signedOutWith("?signed_out=1"), true);
});

test("an ordinary page is not mistaken for a logout", async () => {
  // A session that simply expired should still send somebody to log in, which
  // is the behaviour the signed-out marker has to stay out of the way of.
  for (const search of ["", "?repo=acme/widgets", "?signed_outish=1"]) {
    assert.equal(await signedOutWith(search), false, `${search || "(no query)"} is not a logout`);
  }
});

test("asking outside a browser is answered, not thrown", async () => {
  // These tests import the module with no window at all; so does anything else
  // that reuses it off the page. Throwing here would take the whole module out.
  assert.equal(await signedOutWith(null), false);
});

// signedOutWith imports a fresh copy of common.js with window.location.search
// set and asks it the question, since the answer is memoised per module
// instance and read on first use — so it has to be asked while the window it
// describes is still in place. A null search means no window at all.
async function signedOutWith(search) {
  const previous = globalThis.window;
  if (search === null) {
    delete globalThis.window;
  } else {
    globalThis.window = { location: { search } };
  }
  try {
    const mod = await import(`../static/common.js?signed-out-case=${encodeURIComponent(String(search))}`);
    return mod.signedOutOnPurpose();
  } finally {
    if (previous === undefined) delete globalThis.window;
    else globalThis.window = previous;
  }
}
