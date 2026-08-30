// What the evidence_type field may say, and how it reads.
//
// Its own module because more than one place needs the same vocabulary and
// none of them owns it. Everything here moved out of app.js unchanged.

// How the evidence was collected: a closed set of three (DESIGN.md §2.2). The
// stored values are slugs and the labels are what a reader is shown, so the
// column that used to hold `bazel`, `pytest` and `junit` reads as one thing.
export const EVIDENCE_TYPES = ["ci", "manual_test", "demonstration"];
export const EVIDENCE_TYPE_LABELS = {
  ci: "CI",
  manual_test: "Manual Test",
  demonstration: "Demonstration",
};

// Which one a new record starts on is the markup's business: the Add Result
// form marks it `selected`, so a constant here would be a second answer to a
// question that already has one.

// A record from a store that has not run migration 000006 yet, or one written
// around the API, still has to render as something — so an unknown value shows
// itself rather than becoming blank.
export function evidenceTypeLabel(value) {
  return EVIDENCE_TYPE_LABELS[value] || value || "";
}
