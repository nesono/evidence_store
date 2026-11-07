// Pure rendering for immutable campaign definitions. Controls are disabled
// previews: this view never collects answers or pretends to save an execution.
import { formatTime } from "./common.js";
import { escapeHTML as esc, renderMarkdown } from "./markdown.js";

export const MAX_CAMPAIGN_BYTES = 16 * 1024 * 1024;
export const PROCEDURE_PAGE_SIZE = 10;

export function uploadProblem(file) {
  if (!file) return "Choose a campaign .tar.gz file.";
  if (file.size === 0) return "The selected file is empty.";
  if (file.size > MAX_CAMPAIGN_BYTES) return "Campaign archives must be at most 16 MiB.";
  return "";
}

export function matchingScenarios(scenarios, query) {
  const text = query.trim().toLowerCase();
  return scenarios.filter(({ definition: d }) =>
    [d.title, d.procedure_ref].some(value => String(value).toLowerCase().includes(text)));
}

export function procedurePage(scenarios, query, offset = 0) {
  const matches = matchingScenarios(scenarios, query);
  return { rows: matches.slice(offset, offset + PROCEDURE_PAGE_SIZE), total: matches.length };
}

export function procedureListHTML(rows, selectedID) {
  if (!rows.length) return '<tr><td colspan="2" class="empty-state">No matching procedures.</td></tr>';
  return rows.map(({ definition: d }) => `<tr data-procedure-ref="${esc(d.procedure_ref)}" class="${d.procedure_ref === selectedID ? "selected" : ""}">
    <td><button type="button" class="campaign-procedure-item" aria-pressed="${d.procedure_ref === selectedID}">${esc(d.title)}</button></td>
    <td class="campaign-procedure-reference">${esc(d.procedure_ref)}</td>
  </tr>`).join("");
}

export function campaignRepositoryHTML(repo = "") {
  const name = repo.split("/").filter(Boolean).at(-1)?.replace(/\.git$/, "") || "—";
  try {
    const url = new URL(repo);
    if (url.protocol === "https:" && !url.username && !url.password)
      return `<a href="${esc(url.href)}" target="_blank" rel="noopener noreferrer" title="${esc(repo)}">${esc(name)}</a>`;
  } catch { /* Free-text repository identities do not identify a host. */ }
  return `<span title="${esc(repo)}">${esc(name)}</span>`;
}

export function campaignCommitHTML(commit = "") {
  return commit ? `<a href="#" class="campaign-package-commit" data-copy-commit="${esc(commit)}" title="Copy full commit: ${esc(commit)}" aria-label="Copy full product commit ${esc(commit)}">${esc(commit.slice(0, 8))}</a>` : '<span title="Product revision not recorded">—</span>';
}

export function packageListHTML(rows, selectedID, picker = false) {
  if (!rows.length) return '<tr><td colspan="5" class="empty-state">No campaign packages imported yet.</td></tr>';
  return rows.map(row => {
    const repo = row.manifest.product?.repository || row.manifest.source?.repository || "";
    const commit = row.manifest.product?.commit || "";
    return `<tr class="campaign-package-row"><td><time datetime="${esc(row.imported_at)}" title="Added (UTC)">${esc(formatTime(row.imported_at).slice(0, 16))}</time></td>
      <td><button type="button" class="campaign-package" ${picker ? "data-package" : "data-campaign-id"}="${esc(row.id)}" aria-pressed="${row.id === selectedID}">${esc(row.manifest.campaign.name)}</button></td>
      <td class="campaign-package-count">${esc(String(row.scenario_count ?? row.manifest.scenarios?.length ?? 0))}</td>
      <td>${campaignRepositoryHTML(repo)}</td>
      <td>${campaignCommitHTML(commit)}</td>
    </tr>`;
  }).join("");
}

export async function copyPackageCommit(link, report) {
  try {
    await navigator.clipboard.writeText(link.dataset.copyCommit);
    report("Product commit copied to clipboard.");
  } catch { report("Could not copy the product commit. Clipboard access requires HTTPS or localhost and browser permission."); }
}

export function packageInfoHTML(row) {
  const m = row.definition.manifest;
  return `<h3>${esc(m.campaign.name)}</h3>
    <p class="campaign-scope">${row.definition.scenarios.length} procedures in this package. Importing does not start an execution.</p>
    <dl class="campaign-metadata">
      <dt>Campaign key</dt><dd>${esc(m.campaign.id)}</dd>
      ${m.product ? `<dt>Product repository</dt><dd>${esc(m.product.repository)}</dd><dt>Build branch</dt><dd>${esc(m.product.branch)}</dd><dt>Product commit</dt><dd><code>${esc(m.product.commit)}</code></dd>` : '<dt>Product commit</dt><dd>Not recorded in this legacy package. Set the deployed revision when starting an execution.</dd>'}
      ${m.campaign.planned_location ? `<dt>Planned location</dt><dd>${esc(m.campaign.planned_location)}</dd>` : ""}
      ${m.campaign.planned_start ? `<dt>Planned start</dt><dd>${esc(m.campaign.planned_start)}</dd>` : ""}
      ${m.campaign.planned_end ? `<dt>Planned end</dt><dd>${esc(m.campaign.planned_end)}</dd>` : ""}
    </dl>
    <details class="campaign-provenance"><summary>Package source and import details</summary>
      <dl class="campaign-metadata">
        <dt>Imported package UUID</dt><dd><code>${esc(row.id)}</code></dd>
        <dt>Source repository</dt><dd>${esc(m.source.repository)}</dd>
        <dt>Package digest</dt><dd><code>${esc(row.digest)}</code></dd>
        <dt>Imported</dt><dd>${esc(formatTime(row.imported_at))} by ${esc(row.imported_by)}</dd>
      </dl>
    </details>`;
}

// Resolve only validated archive-relative dependencies. Raw HTML and unrelated
// URLs still go through the existing safe Markdown renderer; image hydration
// receives only same-origin CAS references with verified digest spelling.
export function resolveAssetImages(markdown, descriptionPath, assets) {
  const base = descriptionPath.slice(0, descriptionPath.lastIndexOf("/") + 1);
  return markdown.replace(/!\[([^\]\n]*)\]\(([^)\s]+)\)/g, (whole, label, relative) => {
    const digest = assets?.[base + relative];
    if (!/^sha256:[0-9a-f]{64}$/.test(digest || "")) return whole;
    return `![${label}](/api/v1/blobs/${digest})`;
  });
}

function previewField(field, index) {
  const id = `campaign-preview-${index}`;
  const label = `${esc(field.label)}${field.required ? ' <small>(required)</small>' : ""}`;
  const defaultValue = field.default ?? "";
  let control;
  switch (field.type) {
    case "text":
      control = field.multiline
        ? `<textarea id="${id}" disabled rows="2">${esc(defaultValue)}</textarea>`
        : `<input id="${id}" disabled type="text" value="${esc(defaultValue)}">`;
      break;
    case "measurement":
      control = `<input id="${id}" disabled type="number" step="any" value="${esc(defaultValue)}"><small>Unit: ${esc(field.unit)}</small>`;
      break;
    case "choice":
      control = `<select id="${id}" disabled><option value=""${defaultValue === "" ? " selected" : ""}>Not answered</option>${field.options.map(option =>
        `<option${option === defaultValue ? " selected" : ""}>${esc(option)}</option>`).join("")}</select>`;
      break;
    case "evidence":
      control = `<input id="${id}" type="text" disabled placeholder="Evidence attachments or links">`;
      break;
    default:
      return '<p class="feedback-error">Unsupported field type. Update this application to view the procedure.</p>';
  }
  return `<div class="campaign-control"><label for="${id}">${label}</label>${control}<small>Field: <code>${esc(field.id)}</code>${field.bind ? ` · ${esc(field.bind)}` : ""}</small></div>`;
}

function previewCheck(check, index) {
  return `<div class="campaign-control"><label for="campaign-preview-${index}">${esc(check.label)}</label>
    <select id="campaign-preview-${index}" disabled><option selected>Not answered</option><option>PASS</option><option>FAIL</option><option>NOT_EXECUTED</option></select>
    <small>Check: <code>${esc(check.id)}</code> · ${esc(check.behavior)}</small></div>`;
}

export function procedureHTML(scenario, entryPath) {
  const d = scenario.definition;
  const base = entryPath.slice(0, entryPath.lastIndexOf("/") + 1);
  const descriptionPath = base + d.description;
  const blocks = scenario.procedure.blocks.map((block, index) => {
    if (block.kind === "field") return previewField(block.field, index);
    if (block.kind === "check") return previewCheck(block.check, index);
    if (block.kind === "markdown") return `<div class="test-log">${renderMarkdown(resolveAssetImages(block.markdown, descriptionPath, scenario.assets))}</div>`;
    return '<p class="feedback-error">Unsupported procedure block.</p>';
  }).join("");
  return `<h3>${esc(d.title)}</h3><p><code>${esc(d.procedure_ref)}</code></p>
    <p class="campaign-preview-notice">Definition preview — inputs are disabled. This view does not start a test or save results.</p>
    <details><summary>Procedure defaults and traceability</summary><pre>${esc(JSON.stringify({
      parameters: d.parameters || {}, defaults: d.defaults, traceability: d.traceability || [],
    }, null, 2))}</pre></details>${blocks}`;
}
