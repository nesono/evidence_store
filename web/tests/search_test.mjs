// Unit tests for the URL state behind the Search tab, run with `node --test`.
//
// A search is a link somebody can send, which is why this is worth testing and
// why a regression in it is so quiet: the page still renders, the table still
// fills, it is just not the search that was sent. Nothing here touches a
// document — parseSearchState and searchStateToQuery are the halves of the
// round trip, and both are pure.

import test from "node:test";
import assert from "node:assert/strict";

import { activeAdvancedCount, parseSearchState, searchStateToQuery } from "../static/search.js";

// --- Reading a link ---

test("the filters a link carries come back", () => {
  const { filters } = parseSearchState(
    "?repo=org%2Ffirmware&ref=main&result=FAIL&source=jdoe&procedure_ref=manual%2Fbrake");

  assert.deepEqual(filters, {
    repo: "org/firmware", ref: "main", result: "FAIL",
    source: "jdoe", procedure_ref: "manual/brake",
  });
});

test("an empty query asks for everything", () => {
  const state = parseSearchState("");

  assert.deepEqual(state.filters, {}, "no filters is a legitimate query, not a missing one");
  assert.equal(state.windowOffset, 0);
  assert.equal(state.sortColumn, "");
  assert.equal(state.detail, null);
});

test("a link from before the identity fields merged still works", () => {
  // `branch` and `rcs_ref` were separate boxes once. They are still valid API
  // filters, but the form has no box for either, so a link carrying one would
  // otherwise constrain the results in a way nobody could see or clear.
  assert.equal(parseSearchState("?branch=main").filters.ref, "main");
  assert.equal(parseSearchState("?rcs_ref=abc123").filters.ref, "abc123");

  // Carrying both keeps the commit, as the more specific of the two.
  assert.equal(parseSearchState("?branch=main&rcs_ref=abc123").filters.ref, "abc123");

  // And an explicit `ref` is never overwritten by the older spelling.
  assert.equal(parseSearchState("?ref=v2.0.0&branch=main").filters.ref, "v2.0.0");
});

test("a link naming an evidence type that no longer exists drops it", () => {
  // `bazel` predates the taxonomy. Keeping it would search on a constraint the
  // dropdown cannot show, so every such link would come up empty for no visible
  // reason.
  assert.equal(parseSearchState("?evidence_type=bazel").filters.evidence_type, undefined);
  assert.equal(parseSearchState("?evidence_type=manual_test").filters.evidence_type, "manual_test");
});

test("the window is read, and nonsense in it is ignored", () => {
  assert.equal(parseSearchState("?offset=250").windowOffset, 250);
  assert.equal(parseSearchState("?offset=-5").windowOffset, 0, "a negative offset is no offset");
  assert.equal(parseSearchState("?offset=banana").windowOffset, 0);

  assert.equal(parseSearchState("?limit=200").windowSize, 200);
  assert.equal(parseSearchState("?limit=37").windowSize, 50,
    "a size the control does not offer falls back rather than sticking");
  assert.equal(parseSearchState("", { savedWindowSize: 100 }).windowSize, 100,
    "with no limit in the link, the saved preference decides");
});

test("sort direction is only descending when it says so", () => {
  assert.deepEqual(
    (({ sortColumn, sortDesc }) => ({ sortColumn, sortDesc }))(parseSearchState("?sort=finished_at&order=desc")),
    { sortColumn: "finished_at", sortDesc: true });
  assert.equal(parseSearchState("?sort=repo&order=asc").sortDesc, false);
  assert.equal(parseSearchState("?sort=repo").sortDesc, false, "no order is ascending");
});

test("an opened record is part of the link", () => {
  assert.equal(parseSearchState("?detail=abc-123").detail, "abc-123");
});

test("a cursor from an older link is ignored rather than half-honoured", () => {
  // It is an opaque position marker: it cannot say which window it refers to,
  // so the view would have no range to show and no way back.
  const state = parseSearchState("?cursor=eyJpZCI6IjEifQ&repo=org%2Ffirmware");

  assert.equal(state.windowOffset, 0, "such links open at the first window");
  assert.equal(state.filters.repo, "org/firmware", "the rest of the link still applies");
  assert.equal(state.filters.cursor, undefined);
});

// --- Writing one ---

test("what is written can be read back", () => {
  const filters = { repo: "org/firmware", ref: "main", result: "FAIL,ERROR" };
  const query = searchStateToQuery(filters, {
    windowOffset: 100, windowSize: 200, sortColumn: "finished_at", sortDesc: true, detail: "abc-123",
  });

  const back = parseSearchState(query);
  assert.deepEqual(back.filters, filters);
  assert.equal(back.windowOffset, 100);
  assert.equal(back.windowSize, 200);
  assert.equal(back.sortColumn, "finished_at");
  assert.equal(back.sortDesc, true);
  assert.equal(back.detail, "abc-123");
});

test("an empty filter is left out rather than written as nothing", () => {
  // `?repo=` would read back as a filter on the empty string, which matches no
  // record at all — a link that quietly finds nothing.
  const query = searchStateToQuery({ repo: "", ref: null, source: undefined, notes: "x" },
    { windowSize: 50 });

  assert.equal(query.includes("repo="), false);
  assert.equal(query.includes("ref="), false);
  assert.equal(query.includes("source="), false);
  assert.deepEqual(parseSearchState(query).filters, { notes: "x" });
});

test("the first window is not spelled out", () => {
  const query = searchStateToQuery({}, { windowOffset: 0, windowSize: 50 });
  assert.equal(query.includes("offset="), false, "offset=0 is the default and adds nothing");
  assert.equal(query, "?limit=50");
});

test("no sort means no order either", () => {
  const query = searchStateToQuery({}, { windowSize: 50, sortColumn: "", sortDesc: true });
  assert.equal(query.includes("order="), false,
    "a direction with nothing to sort by is a claim about nothing");
});

// --- The badge on the collapsed filter panel ---

test("the advanced count reflects what is hidden behind the toggle", () => {
  // The badge exists so a collapsed panel never hides a constraint that is
  // shaping the results.
  assert.equal(activeAdvancedCount({}), 0);
  assert.equal(activeAdvancedCount({ repo: "org/firmware" }), 0, "repo is in the bar, not behind it");
  assert.equal(activeAdvancedCount({ source: "jdoe", notes: "flaky" }), 2);
  assert.equal(activeAdvancedCount({ finished_after: "2026-01-01" }), 1);
});

test("inheritance counts only when it has been turned off", () => {
  // On is the default, so it is not a constraint anybody needs warning about.
  assert.equal(activeAdvancedCount({ include_inherited: "true" }), 0);
  assert.equal(activeAdvancedCount({ include_inherited: "false" }), 1);
});
