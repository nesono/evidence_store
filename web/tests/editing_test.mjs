// Unit tests for web/static/editing.js, run with `node --test`.
//
// The DOM half of this module is verified in a real browser, because whether
// undo survives an edit is a fact about the browser and not about this code.
// What is tested here is the caret arithmetic: the part that is easy to get
// wrong, and invisible when it is — a caret that jumps is noticed only by the
// person whose next keystroke lands in the wrong sentence.

import test from "node:test";
import assert from "node:assert/strict";

import { adjustCaret } from "../static/editing.js";

// An image placeholder being swapped for the reference it turned into:
// "![attaching…](#pending-1)" (25 chars) becomes a 60-character reference.
const START = 10, REMOVED = 25, INSERTED = 60;
const at = caret => adjustCaret(caret, START, REMOVED, INSERTED);

test("a caret before the edit does not move", () => {
  assert.equal(at(0), 0);
  assert.equal(at(9), 9);
  assert.equal(at(START), START, "sitting exactly where the edit begins is still before it");
});

test("a caret after the edit moves by the difference", () => {
  // The tester carried on typing past the placeholder while the upload ran.
  assert.equal(at(START + REMOVED), START + INSERTED);
  assert.equal(at(START + REMOVED + 12), START + INSERTED + 12);
});

test("a caret inside what was replaced lands at the end of the replacement", () => {
  // The text it pointed at is gone; the end of what replaced it is the nearest
  // honest place to be.
  assert.equal(at(START + 1), START + INSERTED);
  assert.equal(at(START + REMOVED - 1), START + INSERTED);
});

test("text getting shorter moves the caret back", () => {
  // The other direction: a failed upload removes its placeholder entirely.
  assert.equal(adjustCaret(40, 10, 25, 0), 15);
  assert.equal(adjustCaret(10, 10, 25, 0), 10);
  assert.equal(adjustCaret(20, 10, 25, 0), 10, "inside the removed run, so at where it was");
});

test("an edit that changes nothing leaves the caret alone", () => {
  assert.equal(adjustCaret(30, 10, 5, 5), 30);
});
