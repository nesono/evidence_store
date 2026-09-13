// Unit tests for fitting a record's tags onto one line of the results table,
// run with `node --test`.
//
// The table gives Tags whatever width is left (#167), and a row that grows to
// show every tag breaks the fixed-height window the table is read through. So
// a cell shows as many tags as fit and says how many it left out. Deciding how
// many is arithmetic on measured widths, and that part is tested here without
// a document.

import test from "node:test";
import assert from "node:assert/strict";

import { visibleTagCount } from "../static/tagfit.js";

const more = w => () => w;

test("a record without tags shows none", () => {
  assert.equal(visibleTagCount([], 120), 0);
});

test("every tag shows when they all fit", () => {
  assert.equal(visibleTagCount([40, 30], 120, { gap: 4, moreWidth: more(20) }), 2,
    "40 + 4 + 30 = 74, and no room is kept for a counter nobody needs");
});

test("an exact fit still fits", () => {
  assert.equal(visibleTagCount([50, 50], 104, { gap: 4, moreWidth: more(20) }), 2);
});

test("the counter's own width is paid for by the tags before it", () => {
  // Three tags need 128px. Two would fit in 84px on their own, but not with
  // the "+1" beside them (84 + 4 + 20 = 108), so only the first shows.
  assert.equal(visibleTagCount([40, 40, 40], 100, { gap: 4, moreWidth: more(20) }), 1);
});

test("the counter is measured for the number it will actually show", () => {
  const asked = [];
  const moreWidth = hidden => { asked.push(hidden); return hidden >= 10 ? 30 : 20; };
  const widths = Array(12).fill(10);

  // Two tags and "+10" need 24 + 4 + 30 = 58px, which 50 cannot hold. Measured
  // as if it were "+9" it would be 48 and wrongly fit.
  assert.equal(visibleTagCount(widths, 50, { gap: 4, moreWidth }), 1);
  assert.ok(asked.includes(10), "a two-digit counter is wider, and was asked for");
});

test("the first tag always shows, even when it is wider than the cell", () => {
  // A cell of nothing but "+3" says there are tags and not one of them; the
  // first is shown and truncated instead, which at least names something.
  assert.equal(visibleTagCount([300, 40, 40], 100, { gap: 4, moreWidth: more(20) }), 1);
  assert.equal(visibleTagCount([300], 100), 1);
});

test("no gap and no counter are the defaults", () => {
  assert.equal(visibleTagCount([50, 50], 100), 2);
  assert.equal(visibleTagCount([50, 51], 100), 1);
});
