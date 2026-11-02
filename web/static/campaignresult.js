// Read-only evidence snapshot rendering. All content is untrusted metadata;
// malformed snapshots fall back to the existing technical metadata view.
import { escapeHTML as esc, renderMarkdown } from "./markdown.js";
import { resolveAssetImages } from "./campaignview.js";

export function campaignResultHTML(snapshot) {
  if (
    !snapshot ||
    snapshot.schema_version !== 1 ||
    !Array.isArray(snapshot.procedure?.procedure?.blocks)
  )
    return "";
  try {
    const scenario = snapshot.procedure,
      answers = snapshot.answers || {};
    const path = String(snapshot.scenario_path || "");
    const description =
      path.slice(0, path.lastIndexOf("/") + 1) +
      scenario.definition.description;
    const counts = { PASS: 0, FAIL: 0, NOT_EXECUTED: 0, unanswered: 0 };
    const blocks = scenario.procedure.blocks
      .map((block) => {
        if (block.kind === "markdown")
          return `<div class="test-log">${renderMarkdown(resolveAssetImages(block.markdown, description, scenario.assets))}</div>`;
        const field = block.field || block.check;
        if (!field || field.bind) return "";
        const answer = answers[field.id];
        if (block.check) {
          const outcome = answer?.outcome;
          if (["PASS", "FAIL", "NOT_EXECUTED"].includes(outcome))
            counts[outcome]++;
          else counts.unanswered++;
          return `<div class="campaign-control"><strong>${esc(field.label)}</strong><p>${esc(outcome || "Not answered")}</p>${answer?.reason ? `<p>${esc(answer.reason)}</p>` : ""}</div>`;
        }
        let value =
          answer === undefined
            ? "Not answered"
            : typeof answer === "string" || typeof answer === "number"
              ? String(answer)
              : JSON.stringify(answer);
        if (field.unit && answer !== undefined) value += ` ${field.unit}`;
        return `<div class="campaign-control"><strong>${esc(field.label)}</strong><p>${esc(value)}</p></div>`;
      })
      .join("");
    return `<section class="record-block"><h3>Campaign procedure result</h3>
      <p>Campaign: ${esc(snapshot.campaign_key)} · Procedure: ${esc(snapshot.procedure_ref)}</p>
      <p>${counts.PASS} passed · ${counts.FAIL} failed · ${counts.NOT_EXECUTED} not executed · ${counts.unanswered} unanswered checks</p>
      <details><summary>Executed instructions and recorded answers</summary>${blocks}</details></section>`;
  } catch {
    return "";
  }
}
