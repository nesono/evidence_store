import test from "node:test";
import assert from "node:assert/strict";
import { campaignProgress, fuzzyMatch, procedureState, filteredProcedures, toggleProgressFilter, campaignProgressHTML } from "../static/campaignworkspace.js";

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


test("progress filters include, exclude, toggle off, and combine with search", () => {
  const rows = [
    { procedure_ref: "a", title: "Display", result: "PASS", draft_id: "retry" },
    { procedure_ref: "b", title: "Display", result: "FAIL" },
    { procedure_ref: "c", title: "Patrol", result: "SKIPPED" },
    { procedure_ref: "d", title: "Radio" },
  ];
  const refs = (filter, query = "", local) => filteredProcedures(rows, query, filter, local).map(p => p.procedure_ref);
  const included = toggleProgressFilter(null, "exercised");
  assert.deepEqual(refs(included), ["a", "b"]);
  assert.deepEqual(refs(toggleProgressFilter(null, "exercised", true)), ["c", "d"]);
  assert.equal(toggleProgressFilter(included, "exercised"), null);
  assert.equal(toggleProgressFilter(included, "exercised", true), null);
  assert.deepEqual(refs(toggleProgressFilter(included, "passed")), ["a"]);
  assert.deepEqual(refs(null, "radio"), ["d"]);
  assert.deepEqual(refs({category: "skipped", inverted: true}, "display"), ["a", "b"]);
  const local = new Map([["d", {local_pending: true}]]);
  assert.deepEqual(refs({category: "drafts", inverted: false}, "", local), ["a", "d"]);
  const html = campaignProgressHTML({procedures: rows}, {category: "drafts", inverted: true}, local);
  assert.match(html, /aria-pressed="true" aria-label="In progress: 2 of 4, excluded"/);
  assert.match(html, /Excluding/);
  assert.match(campaignProgressHTML({procedures: []}), /style="--progress: 0%"/);
});


test("default procedure view hides exercised and skipped results while retaining unfinished retries", () => {
  const rows = [
    { procedure_ref: "passed", title: "Passed", result: "PASS" },
    { procedure_ref: "failed", title: "Failed", result: "FAIL" },
    { procedure_ref: "error", title: "Error", result: "ERROR" },
    { procedure_ref: "skipped", title: "Skipped", result: "SKIPPED" },
    { procedure_ref: "new", title: "New" },
    { procedure_ref: "retry", title: "Retry", result: "PASS", draft_id: "draft" },
    { procedure_ref: "local", title: "Local retry", result: "PASS" },
  ];
  const local = new Map([["local", {local_pending: true}]]);
  const refs = filter => filteredProcedures(rows, "", filter, local).map(p => p.procedure_ref);
  assert.deepEqual(refs(null), ["new", "retry", "local"]);
  const skipped = toggleProgressFilter(null, "skipped");
  assert.deepEqual(refs(skipped), ["skipped"]);
  assert.deepEqual(refs(toggleProgressFilter(skipped, "skipped")), refs(null));
  const exercised = toggleProgressFilter(null, "exercised");
  assert.deepEqual(refs(exercised), ["passed", "failed", "error", "retry", "local"]);
  assert.deepEqual(refs(toggleProgressFilter(exercised, "exercised")), refs(null));
  assert.deepEqual(filteredProcedures(rows, "passed", null, local), []);
});
