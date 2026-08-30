// The "= 2026-03-30 14:00 UTC" line under a datetime box.
//
// Both forms have one: the filter bar's finished-after and finished-before, and
// the Add Result form's finished-at. Neither owns it, so it lives here rather
// than in whichever module happened to be written first.
//
// Separate from datetime.js on purpose — that module parses and formats and
// touches no document, which is what lets it be unit-tested. This is the part
// that writes into the page.

import { formatTime } from "./common.js";
import { parseUserDateTime } from "./datetime.js";

export function updateUtcPreview(input) {
  const preview = document.querySelector(`.utc-preview[data-preview-for="${input.name}"]`);
  if (!preview) return;
  const raw = input.value.trim();
  if (!raw) {
    preview.textContent = "";
    preview.classList.remove("utc-preview-error");
    return;
  }
  const d = parseUserDateTime(raw);
  if (!d) {
    preview.textContent = "unparseable — expected e.g. 2026-03-30 14:00";
    preview.classList.add("utc-preview-error");
    return;
  }
  preview.textContent = `= ${formatTime(d.toISOString())} UTC`;
  preview.classList.remove("utc-preview-error");
}

// wireUtcPreviews attaches the preview to every datetime box on the page, and
// renders each one immediately so a field populated from a URL is explained
// before anybody types in it.
export function wireUtcPreviews() {
  document.querySelectorAll("input[data-utc-preview]").forEach(input => {
    input.addEventListener("input", () => updateUtcPreview(input));
    updateUtcPreview(input);
  });
}
