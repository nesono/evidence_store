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
      const d = new Date(filters[f]);
      input.value = isNaN(d.getTime()) ? filters[f] : formatTime(d.toISOString());
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
    const v = form.querySelector(`[name="${f}"]`).value.trim();
    if (v) {
      const d = new Date(v);
      filters[f] = isNaN(d.getTime()) ? v : d.toISOString();
    }
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

function renderTags(metadata) {
  if (!metadata || !metadata.tags || metadata.tags.length === 0) return "";
  return metadata.tags.map(t => `<span class="badge badge-tag">${esc(t)}</span>`).join(" ");
}

function formatTime(iso) {
  const d = new Date(iso);
  const pad = n => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function renderTable(records) {
  const tbody = document.getElementById("results-body");
  if (!records || records.length === 0) {
    tbody.innerHTML = `<tr><td colspan="9" class="empty-state">No records found</td></tr>`;
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
      <td>${formatTime(r.finished_at)}</td>
      <td>${renderTags(r.metadata)}</td>
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
  tbody.innerHTML = `<tr><td colspan="9" class="empty-state">Loading...</td></tr>`;

  try {
    const data = await fetchEvidence(filters, cursor);
    renderTable(data.records);
    renderSummary(data.records ? data.records.length : 0, !!data.next_cursor);
    renderPagination(data.next_cursor || null);
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="9" class="empty-state">Error: ${esc(err.message)}</td></tr>`;
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
    `<tr><td colspan="9" class="empty-state">Enter filters and click Search</td></tr>`;
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

// --- Tabs ---

document.querySelectorAll(".nav-tab").forEach(tab => {
  tab.addEventListener("click", (e) => {
    e.preventDefault();
    const target = tab.dataset.tab;
    document.querySelectorAll(".nav-tab").forEach(t => t.classList.remove("active"));
    tab.classList.add("active");
    document.querySelectorAll(".tab-content").forEach(s => s.hidden = true);
    document.getElementById(`tab-${target}`).hidden = false;
  });
});

// --- Add Evidence ---

async function submitEvidence(andAnother) {
  const form = document.getElementById("add-form");
  const feedback = document.getElementById("add-feedback");

  if (!form.checkValidity()) { form.reportValidity(); return; }

  let finishedAt;
  const rawFinished = form.finished_at.value.trim();
  if (rawFinished) {
    const d = new Date(rawFinished);
    if (isNaN(d.getTime())) {
      feedback.innerHTML = `<p class="feedback-error">Invalid date format. Use YYYY-MM-DD HH:MM</p>`;
      return;
    }
    finishedAt = d.toISOString();
  } else {
    finishedAt = new Date().toISOString();
  }

  const metadata = {};
  const tags = form.tags.value.trim();
  if (tags) metadata.tags = tags.split(",").map(t => t.trim()).filter(Boolean);
  const notes = form.notes.value.trim();
  if (notes) metadata.notes = notes;
  Object.assign(metadata, readCustomFields());

  const record = {
    repo: form.repo.value.trim(),
    branch: form.branch.value.trim(),
    rcs_ref: form.rcs_ref.value.trim(),
    procedure_ref: form.procedure_ref.value.trim(),
    evidence_type: form.evidence_type.value.trim(),
    source: form.source.value.trim(),
    result: form.querySelector('[name="result"]:checked').value,
    finished_at: finishedAt,
  };
  if (Object.keys(metadata).length > 0) record.metadata = metadata;

  feedback.innerHTML = "";
  const btn = form.querySelector('button[type="submit"]');
  btn.setAttribute("aria-busy", "true");

  try {
    const resp = await fetch(`${API_BASE}/evidence`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(record),
    });
    const data = await resp.json();
    if (!resp.ok) {
      const msg = data.errors ? data.errors.map(e => e.message || e).join(", ") : (data.error || JSON.stringify(data));
      feedback.innerHTML = `<p class="feedback-error">${esc(msg)}</p>`;
      return;
    }
    if (andAnother) {
      feedback.innerHTML = `<p class="feedback-ok">Created <code>${data.id}</code></p>`;
      form.querySelector('[name="result"]:checked').checked = false;
      form.notes.value = "";
      document.getElementById("custom-fields-list").innerHTML = "";
    } else {
      feedback.innerHTML = `<p class="feedback-ok">Created <code>${data.id}</code> &mdash; switching to search...</p>`;
      setTimeout(() => {
        document.querySelector('[data-tab="search"]').click();
        feedback.innerHTML = "";
      }, 1000);
    }
  } catch (err) {
    feedback.innerHTML = `<p class="feedback-error">Network error: ${esc(err.message)}</p>`;
  } finally {
    btn.removeAttribute("aria-busy");
  }
}

// --- Custom metadata fields ---

document.getElementById("add-custom-field").addEventListener("click", () => {
  const list = document.getElementById("custom-fields-list");
  const row = document.createElement("div");
  row.className = "custom-field-row grid";
  row.innerHTML = `
    <input type="text" placeholder="key" class="cf-key">
    <input type="text" placeholder="value" class="cf-value">
    <button type="button" class="secondary outline cf-remove">&times;</button>
  `;
  row.querySelector(".cf-remove").addEventListener("click", () => row.remove());
  list.appendChild(row);
  row.querySelector(".cf-key").focus();
});

function readCustomFields() {
  const fields = {};
  document.querySelectorAll(".custom-field-row").forEach(row => {
    const key = row.querySelector(".cf-key").value.trim();
    const val = row.querySelector(".cf-value").value.trim();
    if (key) fields[key] = val;
  });
  return fields;
}

document.getElementById("add-form").addEventListener("submit", (e) => {
  e.preventDefault();
  submitEvidence(false);
});

document.getElementById("add-another").addEventListener("click", () => {
  submitEvidence(true);
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
