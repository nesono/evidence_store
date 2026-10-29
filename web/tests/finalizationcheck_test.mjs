import test from "node:test";
import assert from "node:assert/strict";
import { finalizationBlockers } from "../static/finalizationcheck.js";

const procedure = {blocks:[
  {check:{id:"check",label:"Check"}},
  {field:{id:"distance",label:"Distance",required:true}},
]};
const finish = "2026-10-29T10:00:00Z";
const complete = {repo:"repo",branch:"main",rcs_ref:"build",result:"PASS",answers:{check:{outcome:"PASS"},distance:0}};

test("finalization lists all missing fields and unsaved work", () => {
  const targets = finalizationBlockers(procedure,{answers:{}},"",true).map(i=>i.target);
  for(const target of ["save","repo","branch","rcs_ref","result","finished","answer:check","notes"]) assert.ok(targets.includes(target),target);
});

test("complete PASS accepts a zero measurement, but missing inputs or failed checks block",()=>{
  assert.deepEqual(finalizationBlockers(procedure,complete,finish),[]);
  const invalid = {...complete,answers:{check:{outcome:"FAIL"}}};
  const targets = finalizationBlockers(procedure,invalid,finish).map(i=>i.target);
  assert.ok(targets.includes("answer:check"));
  assert.ok(targets.includes("answer:distance"));
});

test("SKIPPED needs check reasons and notes, not invented measurements",()=>{
  const skipped = {...complete,result:"SKIPPED",notes:"No equipment",answers:{check:{outcome:"NOT_EXECUTED",reason:"No equipment"}}};
  assert.deepEqual(finalizationBlockers(procedure,skipped,finish),[]);
  const targets = finalizationBlockers(procedure,{...skipped,notes:"",answers:{check:{outcome:"NOT_EXECUTED"}}},finish).map(i=>i.target);
  assert.ok(targets.includes("reason:check")); assert.ok(targets.includes("notes"));
});
