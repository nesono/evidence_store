import test from "node:test";
import assert from "node:assert/strict";
import { campaignResultHTML } from "../static/campaignresult.js";

test("campaign evidence summarizes checks and escapes recorded answers", () => {
  const html = campaignResultHTML({
    schema_version: 1,
    campaign_key: "patrol",
    procedure_ref: "test/display",
    scenario_path: "scenario.yaml",
    procedure: {
      definition: { description: "procedure.md" },
      assets: {},
      procedure: {
        blocks: [
          { kind: "check", check: { id: "visible", label: "Visible" } },
          { kind: "field", field: { id: "note", label: "Observation" } },
        ],
      },
    },
    answers: {
      visible: { outcome: "FAIL", reason: "<script>" },
      note: "<img onerror=alert(1)>",
    },
  });
  assert.match(html, /0 passed · 1 failed/);
  assert.ok(!html.includes("<script>"));
  assert.ok(!html.includes("<img onerror"));
  assert.equal(campaignResultHTML({ schema_version: 99 }), "");
});
