const API_BASE = "/api/v1";

const TEXT_FIELDS = [
  "repo", "branch", "rcs_ref", "evidence_type", "source", "procedure_ref", "tags",
];
const DATETIME_FIELDS = ["finished_after", "finished_before"];

let cursorStack = [];
let currentCursor = null;

// --- URL State ---

function readFiltersFromURL() {
  const params = new URLSearchParams(window.location.search);
  const filters = {};
  for (const f of TEXT_FIELDS) {
    if (params.has(f)) filters[f] = params.get(f);
  }
  for (const f of DATETIME_FIELDS) {
    if (params.has(f)) filters[f] = params.get(f);
  }
  if (params.has("result")) filters.result = params.get("result");
  if (params.has("limit")) filters.limit = params.get("limit");
  if (params.has("include_inherited")) filters.include_inherited = params.get("include_inherited");
  if (params.has("cursor")) filters.cursor = params.get("cursor");
  if (params.has("detail")) filters.detail = params.get("detail");
  return filters;
}

function writeFiltersToURL(filters) {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) {
    if (v !== "" && v !== null && v !== undefined) {
      params.set(k, v);
    }
  }
  const qs = params.toString();
  const url = qs ? `?${qs}` : window.location.pathname;
  history.pushState(null, "", url);
}

function populateFormFromFilters(filters) {
  const form = document.getElementById("filter-form");
  for (const f of TEXT_FIELDS) {
    const input = form.querySelector(`[name="${f}"]`);
    if (input) input.value = filters[f] || "";
  }
  for (const f of DATETIME_FIELDS) {
    const input = form.querySelector(`[name="${f}"]`);
    if (input && filters[f]) {
      input.value = filters[f].replace("Z", "").replace(/\+.*$/, "");
    }
  }
  const resultChecks = form.querySelectorAll('[name="result"]');
  const activeResults = (filters.result || "").split(",").filter(Boolean);
  resultChecks.forEach(cb => {
    cb.checked = activeResults.includes(cb.value);
  });
  const limitInput = form.querySelector('[name="limit"]');
  if (limitInput) limitInput.value = filters.limit || "";
  const inheritedInput = form.querySelector('[name="include_inherited"]');
  if (inheritedInput) inheritedInput.checked = filters.include_inherited !== "false";
}

function readFormFilters() {
  const form = document.getElementById("filter-form");
  const filters = {};
  for (const f of TEXT_FIELDS) {
    const v = form.querySelector(`[name="${f}"]`).value.trim();
    if (v) filters[f] = v;
  }
  for (const f of DATETIME_FIELDS) {
    const v = form.querySelector(`[name="${f}"]`).value;
    if (v) filters[f] = new Date(v).toISOString();
  }
  const results = Array.from(form.querySelectorAll('[name="result"]:checked')).map(cb => cb.value);
  if (results.length > 0) filters.result = results.join(",");
  const limit = form.querySelector('[name="limit"]').value;
  if (limit) filters.limit = limit;
  if (!form.querySelector('[name="include_inherited"]').checked) {
    filters.include_inherited = "false";
  }
  return filters;
}

// --- API ---

async function fetchEvidence(filters, cursor) {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) {
    if (k === "detail") continue;
    if (v !== "" && v !== null && v !== undefined) {
      params.set(k, v);
    }
  }
  if (cursor) params.set("cursor", cursor);
  if (!params.has("limit")) params.set("limit", "50");

  const resp = await fetch(`${API_BASE}/evidence?${params}`);
  if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  return resp.json();
}

async function fetchEvidenceById(id) {
  const resp = await fetch(`${API_BASE}/evidence/${id}`);
  if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  return resp.json();
}

async function checkHealth() {
  const el = document.getElementById("health-status");
  try {
    const resp = await fetch("/healthz");
    if (resp.ok) {
      el.innerHTML = `<span class="health-dot health-ok"></span> Connected`;
    } else {
      el.innerHTML = `<span class="health-dot health-fail"></span> Unhealthy`;
    }
  } catch {
    el.innerHTML = `<span class="health-dot health-fail"></span> Offline`;
  }
}

// --- Rendering ---

function resultBadge(result) {
  const cls = result ? result.toLowerCase() : "unknown";
  return `<span class="badge badge-${cls}">${result || "?"}</span>`;
}

function relativeTime(iso) {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(iso).toLocaleDateString();
}

function renderTable(records) {
  const tbody = document.getElementById("results-body");
  if (!records || records.length === 0) {
    tbody.innerHTML = `<tr><td colspan="8" class="empty-state">No records found</td></tr>`;
    return;
  }
  tbody.innerHTML = records.map(r => `
    <tr data-id="${r.id}" class="${r.inherited ? "inherited-row" : ""}">
      <td>${resultBadge(r.result)}</td>
      <td>${esc(r.procedure_ref)}</td>
      <td>${esc(r.repo)}</td>
      <td>${esc(r.branch || "")}</td>
      <td class="commit-ref">${esc((r.rcs_ref || "").slice(0, 10))}</td>
      <td>${esc(r.evidence_type)}</td>
      <td>${esc(r.source)}</td>
      <td title="${r.finished_at}">${relativeTime(r.finished_at)}</td>
    </tr>
  `).join("");
}

function renderSummary(count, hasMore) {
  const el = document.getElementById("results-summary");
  const suffix = hasMore ? "+" : "";
  el.textContent = `${count}${suffix} record${count !== 1 ? "s" : ""}`;
}

function renderPagination(nextCursor) {
  currentCursor = nextCursor;
  document.getElementById("next-page").disabled = !nextCursor;
  document.getElementById("prev-page").disabled = cursorStack.length === 0;
}

function renderDetail(record) {
  const el = document.getElementById("detail-content");
  const fields = [
    ["ID", record.id],
    ["Result", resultBadge(record.result)],
    ["Repo", esc(record.repo)],
    ["Branch", esc(record.branch || "")],
    ["Commit", `<span class="commit-ref">${esc(record.rcs_ref)}</span>`],
    ["Procedure", esc(record.procedure_ref)],
    ["Type", esc(record.evidence_type)],
    ["Source", esc(record.source)],
    ["Finished", record.finished_at],
    ["Ingested", record.ingested_at],
    ["Inherited", record.inherited ? "Yes" : "No"],
  ];
  if (record.inheritance_declaration_id) {
    fields.push(["Inheritance ID", record.inheritance_declaration_id]);
  }

  let html = '<dl class="detail-grid">';
  for (const [label, value] of fields) {
    html += `<dt>${label}</dt><dd>${value}</dd>`;
  }
  html += "</dl>";

  if (record.metadata && Object.keys(record.metadata).length > 0) {
    html += `<div class="metadata-block"><strong>Metadata</strong><pre><code>${esc(JSON.stringify(record.metadata, null, 2))}</code></pre></div>`;
  }

  el.innerHTML = html;
  document.getElementById("detail-dialog").showModal();
}

function esc(str) {
  const d = document.createElement("div");
  d.textContent = str;
  return d.innerHTML;
}

// --- Search ---

async function doSearch(filters, cursor) {
  const tbody = document.getElementById("results-body");
  tbody.innerHTML = `<tr><td colspan="8" class="empty-state">Loading...</td></tr>`;

  try {
    const data = await fetchEvidence(filters, cursor);
    renderTable(data.records);
    renderSummary(data.records ? data.records.length : 0, !!data.next_cursor);
    renderPagination(data.next_cursor || null);
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="8" class="empty-state">Error: ${esc(err.message)}</td></tr>`;
    renderSummary(0, false);
    renderPagination(null);
  }
}

// --- Events ---

document.getElementById("filter-form").addEventListener("submit", (e) => {
  e.preventDefault();
  cursorStack = [];
  const filters = readFormFilters();
  writeFiltersToURL(filters);
  doSearch(filters, null);
});

document.getElementById("clear-filters").addEventListener("click", () => {
  const form = document.getElementById("filter-form");
  form.reset();
  cursorStack = [];
  writeFiltersToURL({});
  document.getElementById("results-body").innerHTML =
    `<tr><td colspan="8" class="empty-state">Enter filters and click Search</td></tr>`;
  document.getElementById("results-summary").textContent = "";
  renderPagination(null);
});

document.getElementById("next-page").addEventListener("click", () => {
  if (!currentCursor) return;
  const filters = readFormFilters();
  cursorStack.push(filters.cursor || null);
  filters.cursor = currentCursor;
  writeFiltersToURL(filters);
  doSearch(filters, currentCursor);
});

document.getElementById("prev-page").addEventListener("click", () => {
  if (cursorStack.length === 0) return;
  const filters = readFormFilters();
  const prevCursor = cursorStack.pop();
  if (prevCursor) {
    filters.cursor = prevCursor;
  } else {
    delete filters.cursor;
  }
  writeFiltersToURL(filters);
  doSearch(filters, prevCursor);
});

document.getElementById("results-body").addEventListener("click", async (e) => {
  const row = e.target.closest("tr[data-id]");
  if (!row) return;
  try {
    const record = await fetchEvidenceById(row.dataset.id);
    renderDetail(record);
    const filters = readFormFilters();
    filters.detail = row.dataset.id;
    writeFiltersToURL(filters);
  } catch (err) {
    alert(`Failed to load record: ${err.message}`);
  }
});

document.getElementById("close-detail").addEventListener("click", () => {
  document.getElementById("detail-dialog").close();
  const filters = readFormFilters();
  delete filters.detail;
  writeFiltersToURL(filters);
});

window.addEventListener("popstate", () => {
  const filters = readFiltersFromURL();
  populateFormFromFilters(filters);
  doSearch(filters, filters.cursor || null);
});

// --- Init ---

(async function init() {
  checkHealth();
  const filters = readFiltersFromURL();
  populateFormFromFilters(filters);

  const hasFilters = Object.keys(filters).some(k => k !== "detail" && k !== "cursor");
  if (hasFilters) {
    doSearch(filters, filters.cursor || null);
  }

  if (filters.detail) {
    try {
      const record = await fetchEvidenceById(filters.detail);
      renderDetail(record);
    } catch { /* ignore */ }
  }
})();
