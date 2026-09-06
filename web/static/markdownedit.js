// Editing behaviour for the markdown boxes: the things somebody typing a list
// or emphasising a word expects to happen without thinking about it.
//
// This is the writing half; markdown.js is the reading half and shares nothing
// with it. A test log is written in a hurry, standing next to a rig, and a
// tester who has to type "- " at the start of every line is a tester who stops
// using lists.
//
// Every rule below is a pure function over (text, selection) returning an edit
// to apply, so the decisions can be tested under node without a document. The
// DOM layer at the bottom is the only part that touches a textarea, and it is
// deliberately thin.

// --- Lists ---

// A list item: indentation, a marker, the space after it, and optionally a task
// box. `1.` and `1)` are both ordered markers; providers of markdown disagree
// about the second, but people type it.
const LIST_ITEM = /^(\s*)([-*+]|\d+[.)])(\s+)(\[[ xX]\]\s+)?(.*)$/;

// lineAround returns the bounds of the line the cursor sits in.
function lineAround(value, cursor) {
  const start = value.lastIndexOf("\n", cursor - 1) + 1;
  const end = value.indexOf("\n", cursor);
  return { start, end: end === -1 ? value.length : end };
}

// continueList decides what Enter should do inside a list.
//
// Three outcomes, and the third is the one people notice missing: on an item
// with nothing in it, Enter ends the list rather than adding another empty
// bullet. Without that there is no way out of a list except deleting the marker
// by hand, which is worse than never having had the feature.
//
// Returns null when Enter should do whatever Enter normally does.
export function continueList(value, cursor) {
  const { start, end } = lineAround(value, cursor);
  const match = LIST_ITEM.exec(value.slice(start, end));
  if (!match) return null;

  const [, indent, marker, spacing, task, content] = match;

  // An item holding nothing but its marker. Ending the list means taking the
  // marker away and leaving the line empty, which is where the cursor already
  // is — no new line, because the one they are on has just become the blank
  // line after the list.
  if (content.trim() === "" && !hasTaskText(task, content)) {
    return { from: start, to: end, insert: "", cursor: start };
  }

  // The text after the cursor travels to the new item, which is what makes
  // pressing Enter in the middle of an item split it the way it looks like it
  // should.
  const prefix = indent + nextMarker(marker) + spacing + (task ? "[ ] " : "");
  return { from: cursor, to: cursor, insert: "\n" + prefix, cursor: cursor + 1 + prefix.length };
}

// hasTaskText keeps an unticked box with words after it from counting as empty.
function hasTaskText(task, content) {
  return !!task && content.trim() !== "";
}

// nextMarker keeps a bullet as it was and advances a number, so a list typed as
// 1. 2. 3. carries on counting rather than restarting.
function nextMarker(marker) {
  const ordered = /^(\d+)([.)])$/.exec(marker);
  if (!ordered) return marker;
  return String(Number(ordered[1]) + 1) + ordered[2];
}

// indentList moves list items in or out by one level.
//
// Two spaces, because that is the shallowest indentation every markdown reader
// agrees is nesting — four is also a code block in CommonMark, and a tester
// whose sub-step turned into a code sample would reasonably conclude the editor
// was broken.
//
// Returns null when the selection is not in a list, which is what keeps Tab
// doing its real job: moving focus. A textarea that swallowed every Tab would
// be a keyboard trap, and somebody navigating this form without a mouse could
// not get out of the log field.
export function indentList(value, start, end, outdent) {
  const first = lineAround(value, start);
  const last = lineAround(value, end);
  const block = value.slice(first.start, last.end);
  const lines = block.split("\n");
  if (!lines.some(line => LIST_ITEM.test(line))) return null;

  let removed = 0;
  const shifted = lines.map(line => {
    if (!LIST_ITEM.test(line)) return line;
    if (!outdent) return INDENT + line;
    const taken = /^( {1,2}|\t)/.exec(line);
    if (!taken) return line;
    removed += taken[0].length;
    return line.slice(taken[0].length);
  });
  if (outdent && removed === 0) return null;

  const insert = shifted.join("\n");

  // A cursor stays on its own line, shifted by what was added or taken; a
  // selection keeps the whole block selected, so holding Tab goes on indenting
  // the same lines rather than walking off the end of them.
  if (start === end) {
    const moved = Math.max(first.start, start + (outdent ? -removed : INDENT.length));
    return { from: first.start, to: last.end, insert, cursor: moved };
  }
  return {
    from: first.start,
    to: last.end,
    insert,
    cursor: first.start,
    selectTo: first.start + insert.length,
  };
}

const INDENT = "  ";

// --- Emphasis ---

// toggleWrap puts markers around the selection, or takes them off again if they
// are already there.
//
// Toggling rather than only adding, because the second press of ⌘B on a word
// that is already bold means "no, not that" — and a rule that only ever added
// would leave ****doubled**** markers behind, which renders as nothing anybody
// wanted.
//
// With no selection it wraps the word the cursor is in, since that is what
// somebody who has just typed a word and reached for ⌘B is asking for. With no
// word either, it leaves the markers with the cursor between them, ready.
export function toggleWrap(value, start, end, marker) {
  if (start === end) {
    const word = wordAround(value, start);
    start = word.start;
    end = word.end;
  }
  const selected = value.slice(start, end);

  // Already wrapped, inside the selection: **word** with the stars taken.
  if (selected.length >= 2 * marker.length && wrappedBy(selected, marker)) {
    const bare = selected.slice(marker.length, selected.length - marker.length);
    return { from: start, to: end, insert: bare, cursor: start, selectTo: start + bare.length };
  }

  // Already wrapped, just outside it: **wo|rd** with only the word selected.
  if (start >= marker.length &&
      wrappedBy(value.slice(start - marker.length, end + marker.length), marker)) {
    return {
      from: start - marker.length,
      to: end + marker.length,
      insert: selected,
      cursor: start - marker.length,
      selectTo: start - marker.length + selected.length,
    };
  }

  return {
    from: start,
    to: end,
    insert: marker + selected + marker,
    cursor: start + marker.length,
    selectTo: start + marker.length + selected.length,
  };
}

// wrappedBy reports whether text is surrounded by exactly this marker.
//
// Exactly, because the markers are made of the same character: **word** both
// starts and ends with a "*", and a looser check would read it as italic and
// strip one star from each end when somebody asked for italic inside bold.
function wrappedBy(text, marker) {
  if (text.length < 2 * marker.length) return false;
  if (!text.startsWith(marker) || !text.endsWith(marker)) return false;
  const ch = marker[0];
  return text[marker.length] !== ch && text[text.length - marker.length - 1] !== ch;
}

// wordAround finds the word the cursor is sitting in or against.
function wordAround(value, cursor) {
  const isWord = ch => /[\w'-]/.test(ch);
  let start = cursor;
  let end = cursor;
  while (start > 0 && isWord(value[start - 1])) start--;
  while (end < value.length && isWord(value[end])) end++;
  return { start, end };
}

// insertLink turns a selection into a link.
//
// Which half the selection becomes depends on what it looks like: select an
// address and it becomes the target with the cursor waiting on the text; select
// words and they become the text with the cursor waiting on the address. Both
// are what somebody who just pressed ⌘K was about to type next.
export function insertLink(value, start, end) {
  const selected = value.slice(start, end);
  if (looksLikeURL(selected)) {
    const insert = `[](${selected})`;
    return { from: start, to: end, insert, cursor: start + 1, selectTo: start + 1 };
  }
  // The placeholder is a real scheme rather than the word "url", so the preview
  // shows a link the moment it is inserted. A target the renderer refuses —
  // and it refuses anything without an allowed scheme, deliberately — leaves
  // the raw markdown on screen, which reads as the link feature being broken
  // rather than as a placeholder waiting to be filled in.
  const insert = `[${selected}](${URL_PLACEHOLDER})`;
  const at = start + selected.length + 3;
  return { from: start, to: end, insert, cursor: at, selectTo: at + URL_PLACEHOLDER.length };
}

function looksLikeURL(text) {
  return /^(https?:\/\/|\/)\S*$/.test(text.trim()) && text.trim() !== "";
}

// --- The textarea ---

const URL_PLACEHOLDER = "https://";

// What each shortcut does. ⌘E for code is what chat applications and GitHub
// use, and it is the third thing a test log needs after bold and a link: an
// error string set apart from the prose.
//
// ⌘⇧E does the same, because ⌘E is not always ours to take — a browser
// extension that claims it wins before the page ever sees the key, and there is
// nothing a page can do about that but offer another way in.
const WRAPPERS = { b: "**", i: "*", e: "`" };
const SHIFTED_WRAPPERS = { e: "`" };

// mountMarkdownEditor gives a textarea the behaviour above.
//
// Edits go through execCommand("insertText") — deprecated, and still the only
// way to change a textarea while leaving the browser's own undo stack intact.
// Assigning to .value wipes it, and an editor that loses ⌘Z on every automatic
// bullet is worse than one that never inserted the bullet.
export function mountMarkdownEditor(textarea) {
  if (!textarea || textarea.dataset.mdEditor === "on") return;
  textarea.dataset.mdEditor = "on";

  textarea.addEventListener("keydown", event => {
    const edit = editFor(event, textarea);
    if (!edit) return;
    event.preventDefault();
    apply(textarea, edit);
  });
}

// editFor turns a keystroke into an edit, or nothing.
function editFor(event, textarea) {
  const { value, selectionStart: start, selectionEnd: end } = textarea;

  if (event.key === "Tab" && !event.metaKey && !event.ctrlKey && !event.altKey) {
    return indentList(value, start, end, event.shiftKey);
  }

  if (event.key === "Enter" && !event.shiftKey && !event.metaKey && !event.ctrlKey) {
    // Only with a plain cursor. Enter over a selection replaces it, and
    // guessing which list the result belongs to would be inventing an answer.
    return start === end ? continueList(value, start) : null;
  }

  // ⌘ on a Mac, Ctrl elsewhere. Both are accepted everywhere rather than
  // sniffing the platform, which is a guess this does not need to make.
  if (!event.metaKey && !event.ctrlKey) return null;
  if (event.altKey) return null;

  const key = event.key.toLowerCase();
  if (event.shiftKey) {
    const shifted = SHIFTED_WRAPPERS[key];
    return shifted ? toggleWrap(value, start, end, shifted) : null;
  }
  if (WRAPPERS[key]) return toggleWrap(value, start, end, WRAPPERS[key]);
  if (key === "k") return insertLink(value, start, end);
  return null;
}

function apply(textarea, edit) {
  const expected =
    textarea.value.slice(0, edit.from) + edit.insert + textarea.value.slice(edit.to);

  // execCommand first, because it is the only way to change a textarea and
  // leave the browser's own undo stack intact: assigning to .value wipes it,
  // and an editor that loses ⌘Z on every automatic bullet is worse than one
  // that never inserted the bullet.
  //
  // It is also deprecated, and Safari has never supported it dependably on a
  // textarea — it returns true and changes nothing, which is why this checks
  // the text afterwards rather than trusting what it reports. Where it did
  // nothing, setRangeText does the edit properly; undo history is the price,
  // and a working editor without undo beats a dead one with it.
  textarea.setSelectionRange(edit.from, edit.to);
  let applied = false;
  try {
    applied = document.execCommand("insertText", false, edit.insert) &&
      textarea.value === expected;
  } catch {
    applied = false;
  }

  if (!applied) {
    textarea.setRangeText(edit.insert, edit.from, edit.to, "end");
    // execCommand raises input on its own; setRangeText does not, and the form
    // listens for it to size the box and to notice there is a draft worth
    // keeping.
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  }

  textarea.setSelectionRange(edit.cursor, edit.selectTo ?? edit.cursor);
}
