// Unit tests for the markdown editing rules in web/static/markdownedit.js.
//
// Every rule is a pure function over (text, selection) returning an edit, so
// what the editor decides can be checked here and only the applying of it needs
// a browser.

import test from "node:test";
import assert from "node:assert/strict";

import {
  continueList, indentList, insertLink, toggleWrap,
} from "../static/markdownedit.js";

// apply performs an edit the way the textarea does, so a test can assert on the
// text somebody would end up looking at rather than on offsets.
function apply(value, edit) {
  if (!edit) return null;
  const after = value.slice(0, edit.from) + edit.insert + value.slice(edit.to);
  return { text: after, cursor: edit.cursor, selectTo: edit.selectTo ?? edit.cursor };
}

// at marks the cursor in a fixture with │, which keeps the tests readable.
function at(fixture) {
  const cursor = fixture.indexOf("│");
  return { value: fixture.replace("│", ""), cursor };
}

// --- Enter continues a list ---

test("Enter after a bullet starts the next one", () => {
  const { value, cursor } = at("- first│");
  const { text } = apply(value, continueList(value, cursor));
  assert.equal(text, "- first\n- ");
});

test("Enter after a numbered item counts on", () => {
  const { value, cursor } = at("1. first│");
  assert.equal(apply(value, continueList(value, cursor)).text, "1. first\n2. ");

  const ninth = at("9. ninth│");
  assert.equal(apply(ninth.value, continueList(ninth.value, ninth.cursor)).text,
    "9. ninth\n10. ", "the number carries, rather than restarting at 1");
});

test("the marker style is kept", () => {
  for (const [line, next] of [["* a│", "* a\n* "], ["+ a│", "+ a\n+ "], ["1) a│", "1) a\n2) "]]) {
    const { value, cursor } = at(line);
    assert.equal(apply(value, continueList(value, cursor)).text, next);
  }
});

test("indentation is kept, so a nested list stays nested", () => {
  const { value, cursor } = at("  - nested│");
  assert.equal(apply(value, continueList(value, cursor)).text, "  - nested\n  - ");
});

test("a task list continues unticked", () => {
  // Copying the tick would mark work done that nobody has done.
  const { value, cursor } = at("- [x] shipped│");
  assert.equal(apply(value, continueList(value, cursor)).text, "- [x] shipped\n- [ ] ");
});

// The rule people notice missing. Without it there is no way out of a list
// except deleting the marker by hand.
test("Enter on an empty item ends the list", () => {
  const { value, cursor } = at("- first\n- │");
  const { text, cursor: after } = apply(value, continueList(value, cursor));
  assert.equal(text, "- first\n");
  assert.equal(after, "- first\n".length, "the cursor lands on the now-empty line");
});

test("an empty task item ends the list too", () => {
  const { value, cursor } = at("- [ ] │");
  assert.equal(apply(value, continueList(value, cursor)).text, "");
});

test("Enter in the middle of an item splits it", () => {
  const { value, cursor } = at("- first│ and more");
  assert.equal(apply(value, continueList(value, cursor)).text, "- first\n-  and more");
});

test("Enter outside a list is left alone", () => {
  for (const fixture of ["just prose│", "# heading│", "", "  │"]) {
    const { value, cursor } = at(fixture || "│");
    assert.equal(continueList(value, Math.max(cursor, 0)), null);
  }
});

// --- Tab indents ---

test("Tab indents a list item by two spaces", () => {
  // Two, not four: four is also a code block in CommonMark, and a sub-step that
  // rendered as a code sample would read as the editor being broken.
  const { value, cursor } = at("- item│");
  assert.equal(apply(value, indentList(value, cursor, cursor, false)).text, "  - item");
});

test("Shift-Tab takes the indentation back off", () => {
  const { value, cursor } = at("  - item│");
  assert.equal(apply(value, indentList(value, cursor, cursor, true)).text, "- item");
});

test("Shift-Tab at the left margin does nothing", () => {
  // Returning null is what lets Tab move focus instead, so the log field is not
  // a keyboard trap for somebody navigating without a mouse.
  const { value, cursor } = at("- item│");
  assert.equal(indentList(value, cursor, cursor, true), null);
});

test("Tab outside a list is not swallowed", () => {
  const { value, cursor } = at("just prose│");
  assert.equal(indentList(value, cursor, cursor, false), null);
});

test("a selection indents every list line it covers", () => {
  const value = "- one\n- two\n- three";
  const edit = indentList(value, 0, value.length, false);
  assert.equal(apply(value, edit).text, "  - one\n  - two\n  - three");
});

test("indenting leaves prose among the items alone", () => {
  const value = "- one\nprose\n- two";
  assert.equal(apply(value, indentList(value, 0, value.length, false)).text,
    "  - one\nprose\n  - two");
});

// --- Emphasis ---

test("bold wraps the selection", () => {
  const value = "make this loud";
  const edit = toggleWrap(value, 5, 9, "**");
  assert.equal(apply(value, edit).text, "make **this** loud");
});

test("bold on already-bold text takes it off", () => {
  const value = "make **this** loud";
  // Selecting the word inside the markers is what a double-click gives you.
  assert.equal(apply(value, toggleWrap(value, 7, 11, "**")).text, "make this loud");
  // And selecting the markers too.
  assert.equal(apply(value, toggleWrap(value, 5, 13, "**")).text, "make this loud");
});

test("with no selection it wraps the word the cursor is in", () => {
  const { value, cursor } = at("make th│is loud");
  assert.equal(apply(value, toggleWrap(value, cursor, cursor, "**")).text, "make **this** loud");
});

test("with no word either it leaves the markers ready", () => {
  const value = "";
  const edit = toggleWrap(value, 0, 0, "**");
  const { text, cursor } = apply(value, edit);
  assert.equal(text, "****");
  assert.equal(cursor, 2, "the cursor waits between them");
});

test("italic and code use their own markers", () => {
  const value = "one";
  assert.equal(apply(value, toggleWrap(value, 0, 3, "*")).text, "*one*");
  assert.equal(apply(value, toggleWrap(value, 0, 3, "`")).text, "`one`");
});

test("italic inside bold is not mistaken for it", () => {
  // **word** starts and ends with *, so a naive check would read it as already
  // italic and strip one star from each end.
  const value = "**word**";
  assert.equal(apply(value, toggleWrap(value, 0, 8, "*")).text, "***word***");
});

// --- Links ---

test("a selected phrase becomes the link text", () => {
  const value = "see the report";
  const edit = insertLink(value, 8, 14);
  const { text, cursor, selectTo } = apply(value, edit);
  // The placeholder is a scheme, not the word "url": the renderer refuses a
  // target it does not recognise, so `(url)` would leave the raw markdown on
  // screen and read as the link feature being broken.
  assert.equal(text, "see the [report](https://)");
  assert.equal(text.slice(cursor, selectTo), "https://",
    "the placeholder is selected, ready to type over");
});

test("a selected address becomes the target", () => {
  const value = "https://example.com/x";
  const { text, cursor } = apply(value, insertLink(value, 0, value.length));
  assert.equal(text, "[](https://example.com/x)");
  assert.equal(cursor, 1, "the cursor waits where the words go");
});

test("a blob reference counts as an address", () => {
  // The one link a log most often carries: an image this store holds.
  const value = "/api/v1/blobs/sha256:abc";
  assert.equal(apply(value, insertLink(value, 0, value.length)).text,
    "[](/api/v1/blobs/sha256:abc)");
});
