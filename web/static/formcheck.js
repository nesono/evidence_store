// Saying what a form is missing, where the person pressing Submit is looking.
//
// The browser's own check stops at the first empty field, scrolls away from
// the button that was just pressed, and on a phone shows a hint small enough to
// miss — Safari on iOS often shows none. Tried on a phone, a missed field
// looked like a Submit button that did nothing. So the form checks itself: it
// names every missing field right under the buttons, each name a way to reach
// that field, and outlines the fields until they are filled in.

// missingGroups returns the fields that stop the form submitting, in form
// order. A group of radio buttons is one field however many buttons it has.
// Takes anything shaped like form.elements, so it can be tested without a page.
export function missingGroups(elements) {
  const groups = [];
  const byKey = new Map();
  for (const el of elements) {
    if (!el.willValidate || el.validity.valid) continue;
    const key = el.type === "radio" ? `radio:${el.name}` : el;
    if (!byKey.has(key)) {
      const group = { key, elements: [] };
      byKey.set(key, group);
      groups.push(group);
    }
    byKey.get(key).elements.push(el);
  }
  return groups;
}

// joinSeparator is what goes before the i-th of n names: nothing, a comma, or
// "and" before the last — "Result", "Result and Commit", "Result, Commit and Repo".
export function joinSeparator(i, n) {
  if (i === 0) return "";
  return i === n - 1 ? " and " : ", ";
}

// The name a person would use for a field: its label's own words, without
// the "(required)" beside them, or a radio group's legend.
function fieldName(el) {
  const holder = el.type === "radio" ? el.closest("fieldset")?.querySelector("legend") : el.closest("label");
  const own = holder && [...holder.childNodes].find(n => n.nodeType === Node.TEXT_NODE && n.textContent.trim());
  return own?.textContent.trim() || el.getAttribute("aria-label") || el.placeholder || el.name;
}

function mark(form) {
  for (const el of form.querySelectorAll("[aria-invalid]")) el.removeAttribute("aria-invalid");
  for (const el of form.querySelectorAll(".field-missing")) el.classList.remove("field-missing");

  const groups = missingGroups(form.elements);
  for (const { elements } of groups) {
    const fieldset = elements[0].type === "radio" ? elements[0].closest("fieldset") : null;
    if (fieldset) fieldset.classList.add("field-missing");
    for (const el of elements) el.setAttribute("aria-invalid", "true");
  }
  return groups;
}

function render(groups, slot, { announce }) {
  slot.replaceChildren();
  if (groups.length === 0) return;

  const message = document.createElement("p");
  message.className = "feedback-error missing-summary";
  // Announced when Submit is pressed, not again on every keystroke after.
  if (announce) message.setAttribute("role", "alert");
  message.append("Fill in before submitting: ");
  groups.forEach(({ elements }, i) => {
    message.append(joinSeparator(i, groups.length));
    const jump = document.createElement("button");
    jump.type = "button";
    jump.className = "missing-field";
    jump.textContent = fieldName(elements[0]);
    jump.addEventListener("click", () => {
      elements[0].scrollIntoView({ block: "center", behavior: "smooth" });
      elements[0].focus({ preventScroll: true });
    });
    message.append(jump);
  });
  message.append(".");
  slot.append(message);
}

// showMissing marks what is missing and says so in `slot`. Returns whether
// anything was.
export function showMissing(form, slot) {
  const groups = mark(form);
  render(groups, slot, { announce: true });
  return groups.length > 0;
}

// refreshMissing keeps an open summary true as fields are filled in, and
// removes it once nothing is missing. It does nothing until Submit has been
// pressed once: an empty form nobody has tried to send is not an error.
export function refreshMissing(form, slot) {
  if (!slot.querySelector(".missing-summary")) return;
  render(mark(form), slot, { announce: false });
}
