import test from "node:test";
import assert from "node:assert/strict";
import { draftControlsHTML, readDraftForm } from "../static/campaigndrafts.js";

test("draft controls keep stable field IDs and escape entered values", () => {
  const procedure = {
    blocks: [
      {
        field: {
          id: "measurement",
          type: "measurement",
          label: "Distance",
          unit: "m",
        },
      },
      { check: { id: "visible", label: "Visible" } },
      { field: { id: "note", type: "text", label: "Note" } },
    ],
  };
  const html = draftControlsHTML(
    procedure,
    {
      answers: {
        measurement: 0,
        visible: { outcome: "FAIL", reason: "<script>" },
        note: "</textarea><script>",
      },
    },
    "procedure.md",
    {},
  );
  assert.ok(html.includes('data-answer="measurement"'));
  assert.ok(html.includes('value="0"'));
  assert.ok(html.includes('value="FAIL" selected'));
  assert.ok(!html.includes("<script>"));
});

test("form captures typed answers and preserves labels on previously saved links", () => {
  const inputs = [
    { dataset: { answer: "measurement", type: "measurement" }, value: "0" },
    { dataset: { answer: "visible", type: "check" }, value: "NOT_EXECUTED" },
    {
      dataset: { answer: "links", type: "evidence" },
      value: "https://example.org/log",
    },
  ];
  const form = {
    elements: { namedItem: name => name === "evidence_type" ? null : ({ value: "" }) },
    querySelectorAll: (selector) =>
      selector === "[data-answer]"
        ? inputs
        : [{ dataset: { reason: "visible" }, value: "no equipment" }],
  };
  const data = readDraftForm(form, {
    links: [{ uri: "https://example.org/log", label: "Recording" }],
  });
  assert.equal(data.answers.measurement, 0);
  assert.deepEqual(data.answers.visible, {
    outcome: "NOT_EXECUTED",
    reason: "no equipment",
  });
  assert.deepEqual(data.answers.links, [
    { uri: "https://example.org/log", label: "Recording" },
  ]);
  inputs[1].value = "";
  assert.throws(() => readDraftForm(form), /Select a check outcome/);
});
