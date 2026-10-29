// Mirrors the server's completion rules for actionable feedback. Drafts may be
// incomplete; finalization still validates the saved revision on the server.
export function finalizationBlockers(procedure, data, finished, dirty = false) {
  const issues = [];
  const add = (target, message) => issues.push({ target, message });
  if (dirty) add("save", "Save draft first to include your latest changes.");
  for (const [key, label] of [["repo", "Repository"], ["branch", "Branch"], ["rcs_ref", "Deployed revision"]]) {
    if (!String(data[key] || "").trim()) add(key, `${label} is required.`);
  }
  if (!["PASS", "FAIL", "ERROR", "SKIPPED"].includes(data.result)) add("result", "Choose an overall Result; individual checks do not set it.");
  if (!finished || !/(Z|[+-]\d{2}:\d{2})$/.test(finished.trim()) || !Number.isFinite(Date.parse(finished))) add("finished", "Actual finish time is required, including its timezone.");
  const answers = data.answers || {};
  const missing = [];
  let failed = 0;
  for (const block of procedure.blocks) {
    if (block.check) {
      const check = block.check, answer = answers[check.id];
      if (!answer?.outcome) add(`answer:${check.id}`, `${check.label}: choose a check outcome.`);
      else if (data.result === "PASS" && answer.outcome !== "PASS") add(`answer:${check.id}`, `${check.label}: must pass for overall PASS.`);
      else if (data.result === "SKIPPED" && answer.outcome !== "NOT_EXECUTED") add(`answer:${check.id}`, `${check.label}: must be NOT_EXECUTED for SKIPPED.`);
      if (answer?.outcome === "NOT_EXECUTED" && !answer.reason?.trim()) add(`reason:${check.id}`, `${check.label}: a reason is required for NOT_EXECUTED.`);
      if (answer?.outcome === "FAIL") failed++;
    }
    const field = block.field;
    if (!field || field.bind || !field.required) continue;
    const answer = answers[field.id];
    if (answer === undefined || answer === null || (typeof answer === "string" && !answer.trim()) || (Array.isArray(answer) && !answer.length)) {
      missing.push(field.label);
      if (data.result === "PASS") add(`answer:${field.id}`, `${field.label} is required for PASS.`);
    }
  }
  if ((missing.length || ["ERROR", "SKIPPED"].includes(data.result) || (data.result === "FAIL" && !failed)) && !data.notes?.trim()) {
    add("notes", `Tester notes must explain the assessment${missing.length ? ` and missing inputs: ${missing.join(", ")}` : ""}.`);
  }
  return issues;
}
