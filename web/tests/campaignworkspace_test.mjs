import test from "node:test";
import assert from "node:assert/strict";
import { campaignProgress, fuzzyMatch, procedureState } from "../static/campaignworkspace.js";

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

test("procedure circles distinguish progress and every final outcome", () => {
  assert.equal(procedureState({}).kind, 'not-started');
  assert.equal(procedureState({draft_id:'draft'}).kind, 'started');
  for (const [result, kind] of Object.entries({PASS:'passed',FAIL:'failed',SKIPPED:'skipped',ERROR:'error'})) {
    assert.equal(procedureState({result}).kind, kind);
    assert.equal(procedureState({result, draft_id:'retry'}).kind, 'started');
  }
  assert.equal(procedureState({}, {local_pending:true}).kind, 'started');
  assert.deepEqual(procedureState({result:'PASS'}, {local_pending:true,local_complete:true}), {kind:'started',label:'Completed locally, waiting to synchronize'});
  assert.equal(procedureState({latest_id:'expired'}).label, 'Result expired');
});
