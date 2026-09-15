// Unit tests for where the store opens on a phone (#162), run with
// `node --test`.
//
// A phone is where a tester files what just happened, so it opens on Add
// Result. That must never hijack an address that says where to go: the
// installed app's #add shortcut, a link to a search somebody sent, or a link
// to one record.

import test from "node:test";
import assert from "node:assert/strict";

import { phoneStartTab } from "../static/phonenav.js";

test("a phone with a bare address opens on Add Result", () => {
  assert.equal(phoneStartTab({ isPhone: true }), "add");
});

test("a desktop opens where it always did", () => {
  assert.equal(phoneStartTab({ isPhone: false }), null);
});

test("a tab in the fragment wins", () => {
  assert.equal(phoneStartTab({ isPhone: true, hashTab: "search" }), null);
  assert.equal(phoneStartTab({ isPhone: true, hashTab: "analytics" }), null);
});

test("a link to a search or a record opens it, not the form", () => {
  assert.equal(phoneStartTab({ isPhone: true, hasLinkState: true }), null,
    "somebody sent this link to be read, on whatever device it was opened on");
});

test("nothing to go on is not a phone", () => {
  assert.equal(phoneStartTab(), null);
});
