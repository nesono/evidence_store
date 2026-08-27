// Programmatic edits the browser can still undo.
//
// A tester types a test log, pastes a photograph into the middle of it, types
// some more, and presses Cmd-Z. What they expect back is the sentence they just
// wrote. What they got instead was a reopened tab, because Safari's Edit menu
// falls through to its own history when the focused field has nothing to undo
// — and the field had nothing to undo because this page had emptied its stack.
//
// Both of the obvious ways to write into a textarea do that. `field.value = …`
// discards the undo stack outright, and `setRangeText` does not join it, so the
// edits around it become unreachable too. Measured in Chrome, on this page:
//
//   typed, then undo                  -> reverted
//   typed, setRangeText, then undo    -> nothing happened
//   typed, value assignment, undo     -> nothing happened
//   typed, execCommand insertText     -> reverted just the insertion
//
// So every programmatic edit to a field a person also types in goes through
// here. document.execCommand is deprecated and has no replacement for this:
// it is the only way to make an edit that the native undo stack records. When
// it is refused, the fallbacks below still make the edit — the text is what
// matters most — and undo is what is lost.

// adjustCaret moves a caret that was sitting after an edit somewhere before it.
//
// Pure, because this is the part that is easy to get wrong and invisible when
// it is: a caret that jumps to the wrong place is only noticed by the person
// whose next keystroke lands in the wrong sentence.
export function adjustCaret(caret, start, removed, inserted) {
  if (caret <= start) return caret;
  // Inside what was replaced: the text it referred to is gone, so the end of
  // the replacement is the nearest honest place to be.
  if (caret <= start + removed) return start + inserted;
  return caret + (inserted - removed);
}

// edit replaces [start, end) with text, keeping the undo stack intact, and
// leaves the page's focus and caret where it found them.
//
// Restoring focus matters more than it sounds: an image upload finishes
// whenever it finishes, which may be several sentences after the tester pasted
// it, and stealing the caret out of the middle of a word to complete a
// background edit would be its own bug.
export function edit(field, start, end, text) {
  const previouslyFocused = document.activeElement;
  const hadFocus = previouslyFocused === field;
  const caret = hadFocus ? { start: field.selectionStart, end: field.selectionEnd } : null;

  const applied = applyEdit(field, start, end, text);

  if (hadFocus) {
    const removed = end - start;
    field.setSelectionRange(
      adjustCaret(caret.start, start, removed, text.length),
      adjustCaret(caret.end, start, removed, text.length),
    );
  } else if (previouslyFocused && previouslyFocused !== field && previouslyFocused.focus) {
    previouslyFocused.focus();
  }
  return applied;
}

// applyEdit does the write itself and reports whether undo survived it.
function applyEdit(field, start, end, text) {
  field.focus();
  field.setSelectionRange(start, end);

  try {
    // insertText with an empty string is not a deletion in every browser, so a
    // removal asks for one by name.
    const command = text === "" ? "delete" : "insertText";
    if (document.execCommand(command, false, text === "" ? undefined : text)) {
      // execCommand fires `input` itself. Dispatching another would make every
      // listener run twice for one edit.
      return true;
    }
  } catch {
    // Some browsers throw rather than returning false. Either way, fall back.
  }

  // Undo is lost, the text is not. setRangeText over an assignment because it
  // at least leaves the rest of the field alone.
  field.setRangeText(text, start, end, "end");
  field.dispatchEvent(new Event("input", { bubbles: true }));
  return false;
}

// insertAtCursor puts text where the caret is, or at the end if the field is
// not focused.
export function insertAtCursor(field, text) {
  const start = field === document.activeElement ? field.selectionStart : field.value.length;
  const end = field === document.activeElement ? field.selectionEnd : field.value.length;
  field.focus();
  field.setSelectionRange(start, end);
  const applied = applyEdit(field, start, end, text);
  // Deliberately left where the insertion ends: the tester is mid-sentence and
  // wants to carry on after what was just put in.
  return applied;
}

// replaceFirst swaps the first occurrence of token for replacement, wherever it
// has ended up by now.
//
// By text rather than by remembered position, because between an image being
// pasted and its upload finishing the tester has carried on typing and every
// offset has moved.
export function replaceFirst(field, token, replacement) {
  const at = field.value.indexOf(token);
  if (at === -1) return false; // the tester deleted it meanwhile
  edit(field, at, at + token.length, replacement);
  return true;
}

// setValue replaces everything in a field, undoably — for the buttons that fill
// one in on the tester's behalf.
//
// Whether that is worth undoing is the same question as whether it is worth
// typing over, and it plainly is: Locate and Look up write into fields whose
// whole point is that the person who was standing there can correct them.
export function setValue(field, text) {
  if (field.value === text) return true;
  return edit(field, 0, field.value.length, text);
}
