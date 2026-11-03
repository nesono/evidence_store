import test from "node:test";
import assert from "node:assert/strict";
import { campaignProgress, fuzzyMatch } from "../static/campaignworkspace.js";

test("campaign progress keeps skips and unfinished retries distinct from exercised procedures", () => {
  assert.deepEqual(campaignProgress([
    { result: "PASS", latest_id: "a" },
    { result: "FAIL", latest_id: "b", draft_id: "retry" },
    { result: "SKIPPED", latest_id: "c" },
    { result: "", draft_id: "draft" },
    { result: "" },
  ]), { total: 5, passed: 1, exercised: 2, skipped: 1, drafts: 2, unfinished: 3 });
});
test("fuzzy procedure search accepts abbreviations, ignores case and treats symbols literally", () => {
  assert.equal(fuzzyMatch("Daily patrol / Display check", "dly dsp"), true);
  assert.equal(fuzzyMatch("Daily patrol", "PD"), false);
  assert.equal(fuzzyMatch("Daily patrol", "%"), false);
  assert.equal(fuzzyMatch("Daily patrol", ""), true);
});
