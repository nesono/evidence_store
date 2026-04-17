const API_BASE = "/api/v1";

const TEXT_FIELDS = [
  "repo", "branch", "rcs_ref", "evidence_type", "source", "procedure_ref", "tags", "notes",
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
      const d = parseUserDateTime(filters[f]);
      input.value = d ? formatTime(d.toISOString()) : filters[f];
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
      const d = parseUserDateTime(v);
      filters[f] = d ? d.toISOString() : v;
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

// --- Auth ---

const API_KEY_STORAGE = "evidence_api_key";

function getStoredAPIKey() {
  return localStorage.getItem(API_KEY_STORAGE) || "";
}

function setStoredAPIKey(key) {
  if (key) {
    localStorage.setItem(API_KEY_STORAGE, key);
  } else {
    localStorage.removeItem(API_KEY_STORAGE);
  }
  updateAuthUI();
}

function updateAuthUI() {
  const btn = document.getElementById("auth-logout");
  if (btn) btn.hidden = !getStoredAPIKey();
}

function promptForAPIKey(msg) {
  const key = prompt(msg || "Enter your API key:");
  if (key !== null) {
    setStoredAPIKey(key.trim());
  }
  return getStoredAPIKey();
}

// Wrapper around fetch that attaches Authorization header and handles 401.
async function apiFetch(url, options = {}) {
  const key = getStoredAPIKey();
  if (key) {
    options.headers = { ...options.headers, Authorization: `Bearer ${key}` };
  }
  const resp = await fetch(url, options);
  if (resp.status === 401) {
    const newKey = promptForAPIKey("Authentication required. Enter your API key:");
    if (newKey) {
      options.headers = { ...options.headers, Authorization: `Bearer ${newKey}` };
      return fetch(url, options);
    }
  }
  return resp;
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

  const resp = await apiFetch(`${API_BASE}/evidence?${params}`);
  if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  return resp.json();
}

async function fetchEvidenceById(id) {
  const resp = await apiFetch(`${API_BASE}/evidence/${id}`);
  if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  return resp.json();
}

const HEALTH_POLL_MS = 5000;

async function checkHealth() {
  const el = document.getElementById("health-status");
  try {
    const resp = await fetch("/healthz", { cache: "no-store" });
    if (resp.ok) {
      el.innerHTML = `<span class="health-dot health-ok"></span> Connected`;
    } else {
      el.innerHTML = `<span class="health-dot health-fail"></span> Unhealthy`;
    }
  } catch {
    el.innerHTML = `<span class="health-dot health-fail"></span> Offline`;
  }
}

function startHealthPolling() {
  checkHealth();
  setInterval(checkHealth, HEALTH_POLL_MS);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") checkHealth();
  });
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
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
}

// Parse a user-entered datetime string. Zoneless values are treated as UTC.
function parseUserDateTime(str) {
  str = str.trim();
  if (!str) return null;
  // If the string has a timezone suffix (Z or +/-HH:MM), parse directly.
  if (/Z$|[+-]\d{2}:?\d{2}$/.test(str)) {
    const d = new Date(str);
    return isNaN(d.getTime()) ? null : d;
  }
  // Zoneless — treat as UTC by appending Z (normalize separators first).
  const normalized = str.replace(" ", "T");
  const d = new Date(normalized + "Z");
  return isNaN(d.getTime()) ? null : d;
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
    const d = parseUserDateTime(rawFinished);
    if (!d) {
      feedback.innerHTML = `<p class="feedback-error">Invalid date format. Use YYYY-MM-DD HH:MM (UTC)</p>`;
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
    const resp = await apiFetch(`${API_BASE}/evidence`, {
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
      const currentTpl = document.getElementById("template-select").value;
      if (currentTpl) {
        applyTemplate(currentTpl);
      } else {
        document.getElementById("custom-fields-list").innerHTML = "";
      }
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

document.getElementById("fill-now").addEventListener("click", () => {
  document.querySelector('#add-form [name="finished_at"]').value = formatTime(new Date().toISOString());
});

document.getElementById("add-form").addEventListener("submit", (e) => {
  e.preventDefault();
  submitEvidence(false);
});

document.getElementById("add-another").addEventListener("click", () => {
  submitEvidence(true);
});

// --- Form Templates ---

const TEMPLATE_STORAGE_KEY = "evidence_templates";
const TEMPLATE_DEFAULT_FIELDS = ["repo", "branch", "rcs_ref", "procedure_ref", "evidence_type", "source", "tags"];

function loadTemplates() {
  try {
    return JSON.parse(localStorage.getItem(TEMPLATE_STORAGE_KEY)) || [];
  } catch { return []; }
}

function saveTemplates(templates) {
  localStorage.setItem(TEMPLATE_STORAGE_KEY, JSON.stringify(templates));
}

function refreshTemplateDropdown() {
  const sel = document.getElementById("template-select");
  const current = sel.value;
  sel.innerHTML = `<option value="">-- No template --</option>`;
  for (const tpl of loadTemplates()) {
    const opt = document.createElement("option");
    opt.value = tpl.id;
    opt.textContent = tpl.name;
    sel.appendChild(opt);
  }
  sel.value = current || "";
}

function applyTemplate(templateId) {
  const form = document.getElementById("add-form");
  const cfList = document.getElementById("custom-fields-list");

  if (!templateId) {
    for (const f of TEMPLATE_DEFAULT_FIELDS) {
      const input = form.querySelector(`[name="${f}"]`);
      if (input) input.value = f === "evidence_type" ? "manual" : "";
    }
    cfList.innerHTML = "";
    return;
  }

  const tpl = loadTemplates().find(t => t.id === templateId);
  if (!tpl) return;

  for (const f of TEMPLATE_DEFAULT_FIELDS) {
    const input = form.querySelector(`[name="${f}"]`);
    if (input) input.value = (tpl.defaults && tpl.defaults[f]) || (f === "evidence_type" ? "manual" : "");
  }

  cfList.innerHTML = "";
  if (tpl.customFields) {
    for (const cf of tpl.customFields) {
      const row = document.createElement("div");
      row.className = "custom-field-row grid";
      row.innerHTML = `
        <input type="text" value="${esc(cf.key)}" class="cf-key" readonly title="${esc(cf.label || cf.key)}">
        <input type="text" placeholder="${esc(cf.placeholder || "")}" class="cf-value">
        <button type="button" class="secondary outline cf-remove">&times;</button>
      `;
      row.querySelector(".cf-remove").addEventListener("click", () => row.remove());
      cfList.appendChild(row);
    }
  }
}

document.getElementById("template-select").addEventListener("change", (e) => {
  applyTemplate(e.target.value);
});

// --- Template Management Dialog ---

const templateDialog = document.getElementById("template-dialog");

document.getElementById("close-template-dialog").addEventListener("click", () => {
  templateDialog.close();
});

document.getElementById("template-manage").addEventListener("click", () => {
  renderTemplateList();
  templateDialog.showModal();
});

function renderTemplateList() {
  document.getElementById("template-dialog-title").textContent = "Manage Templates";
  const content = document.getElementById("template-dialog-content");
  const templates = loadTemplates();

  let html = "";
  if (templates.length === 0) {
    html += `<p style="color:var(--pico-muted-color);font-size:0.9em">No templates yet.</p>`;
  } else {
    for (const tpl of templates) {
      html += `
        <div class="template-list-item">
          <span>${esc(tpl.name)}</span>
          <div class="template-list-actions">
            <button class="secondary outline" data-edit="${tpl.id}">Edit</button>
            <button class="secondary outline" data-delete="${tpl.id}">&times;</button>
          </div>
        </div>`;
    }
  }
  html += `<div style="margin-top:0.5em"><button class="secondary" id="tpl-create-new" style="width:auto;padding:0.3em 0.8em;font-size:0.85em">+ Create New</button></div>`;
  html += `
    <div class="template-import-export">
      <button class="secondary outline" id="tpl-export">Export All</button>
      <button class="secondary outline" id="tpl-import-btn">Import</button>
      <input type="file" id="tpl-import-file" accept=".json" hidden>
    </div>`;

  content.innerHTML = html;

  content.querySelector("#tpl-create-new").addEventListener("click", () => renderTemplateEditor(null));
  content.querySelectorAll("[data-edit]").forEach(btn => {
    btn.addEventListener("click", () => renderTemplateEditor(btn.dataset.edit));
  });
  content.querySelectorAll("[data-delete]").forEach(btn => {
    btn.addEventListener("click", () => {
      const templates = loadTemplates().filter(t => t.id !== btn.dataset.delete);
      saveTemplates(templates);
      refreshTemplateDropdown();
      renderTemplateList();
    });
  });
  content.querySelector("#tpl-export").addEventListener("click", exportTemplates);
  content.querySelector("#tpl-import-btn").addEventListener("click", () => {
    content.querySelector("#tpl-import-file").click();
  });
  content.querySelector("#tpl-import-file").addEventListener("change", (e) => {
    if (e.target.files[0]) importTemplates(e.target.files[0]);
  });
}

function renderTemplateEditor(templateId) {
  document.getElementById("template-dialog-title").textContent = templateId ? "Edit Template" : "New Template";
  const content = document.getElementById("template-dialog-content");
  const tpl = templateId ? loadTemplates().find(t => t.id === templateId) : null;
  const defaults = (tpl && tpl.defaults) || {};
  const customFields = (tpl && tpl.customFields) || [];

  let fieldsHtml = customFields.map((cf, i) => `
    <div class="template-field-def" data-idx="${i}">
      <input type="text" value="${esc(cf.key)}" placeholder="key" class="tfd-key">
      <input type="text" value="${esc(cf.label)}" placeholder="label" class="tfd-label">
      <input type="text" value="${esc(cf.placeholder || "")}" placeholder="placeholder" class="tfd-placeholder">
      <button type="button" class="secondary outline cf-remove">&times;</button>
    </div>`).join("");

  content.innerHTML = `
    <div class="template-editor">
      <label>Template name
        <input type="text" id="tpl-ed-name" value="${esc(tpl ? tpl.name : "")}" placeholder="My Template" required>
      </label>
      <fieldset>
        <legend>Default values</legend>
        <div class="grid">
          <label>Repo <input type="text" id="tpl-def-repo" value="${esc(defaults.repo || "")}"></label>
          <label>Branch <input type="text" id="tpl-def-branch" value="${esc(defaults.branch || "")}"></label>
        </div>
        <div class="grid">
          <label>Commit <input type="text" id="tpl-def-rcs_ref" value="${esc(defaults.rcs_ref || "")}"></label>
          <label>Procedure <input type="text" id="tpl-def-procedure_ref" value="${esc(defaults.procedure_ref || "")}"></label>
        </div>
        <div class="grid">
          <label>Evidence type <input type="text" id="tpl-def-evidence_type" value="${esc(defaults.evidence_type || "")}"></label>
          <label>Source <input type="text" id="tpl-def-source" value="${esc(defaults.source || "")}"></label>
        </div>
        <label>Tags <input type="text" id="tpl-def-tags" value="${esc(defaults.tags || "")}"></label>
      </fieldset>
      <fieldset>
        <legend>Custom metadata fields</legend>
        <div id="tpl-field-defs">${fieldsHtml}</div>
        <button type="button" class="secondary outline" id="tpl-add-field" style="font-size:0.8em;padding:0.2em 0.6em;width:auto;margin-top:0.3em">+ Add field</button>
      </fieldset>
      <div class="filter-actions" style="margin-top:0.5em">
        <button id="tpl-ed-save">Save</button>
        <button class="secondary" id="tpl-ed-cancel">Cancel</button>
      </div>
    </div>`;

  content.querySelectorAll(".cf-remove").forEach(btn => {
    btn.addEventListener("click", () => btn.closest(".template-field-def").remove());
  });

  content.querySelector("#tpl-add-field").addEventListener("click", () => {
    const defs = content.querySelector("#tpl-field-defs");
    const row = document.createElement("div");
    row.className = "template-field-def";
    row.innerHTML = `
      <input type="text" placeholder="key" class="tfd-key">
      <input type="text" placeholder="label" class="tfd-label">
      <input type="text" placeholder="placeholder" class="tfd-placeholder">
      <button type="button" class="secondary outline cf-remove">&times;</button>
    `;
    row.querySelector(".cf-remove").addEventListener("click", () => row.remove());
    defs.appendChild(row);
    row.querySelector(".tfd-key").focus();
  });

  content.querySelector("#tpl-ed-cancel").addEventListener("click", () => renderTemplateList());

  content.querySelector("#tpl-ed-save").addEventListener("click", () => {
    const name = content.querySelector("#tpl-ed-name").value.trim();
    if (!name) { content.querySelector("#tpl-ed-name").focus(); return; }

    const newDefaults = {};
    for (const f of TEMPLATE_DEFAULT_FIELDS) {
      const v = content.querySelector(`#tpl-def-${f}`).value.trim();
      if (v) newDefaults[f] = v;
    }

    const newFields = [];
    content.querySelectorAll(".template-field-def").forEach(row => {
      const key = row.querySelector(".tfd-key").value.trim();
      const label = row.querySelector(".tfd-label").value.trim();
      const placeholder = row.querySelector(".tfd-placeholder").value.trim();
      if (key) newFields.push({ key, label: label || key, placeholder });
    });

    const templates = loadTemplates();
    if (templateId) {
      const idx = templates.findIndex(t => t.id === templateId);
      if (idx !== -1) {
        templates[idx] = { ...templates[idx], name, defaults: newDefaults, customFields: newFields };
      }
    } else {
      templates.push({ id: "tpl_" + Date.now(), name, defaults: newDefaults, customFields: newFields });
    }
    saveTemplates(templates);
    refreshTemplateDropdown();
    renderTemplateList();
  });
}

// --- Save Current Form as Template ---

document.getElementById("template-save-current").addEventListener("click", () => {
  const form = document.getElementById("add-form");
  const defaults = {};
  for (const f of TEMPLATE_DEFAULT_FIELDS) {
    const v = form.querySelector(`[name="${f}"]`).value.trim();
    if (v) defaults[f] = v;
  }

  const customFields = [];
  document.querySelectorAll(".custom-field-row").forEach(row => {
    const key = row.querySelector(".cf-key").value.trim();
    if (key) {
      customFields.push({ key, label: key, placeholder: "" });
    }
  });

  const templates = loadTemplates();
  const tpl = { id: "tpl_" + Date.now(), name: "", defaults, customFields };
  templates.push(tpl);
  saveTemplates(templates);
  refreshTemplateDropdown();

  renderTemplateEditor(tpl.id);
  templateDialog.showModal();
});

// --- Template Import/Export ---

function exportTemplates() {
  const data = JSON.stringify(loadTemplates(), null, 2);
  const blob = new Blob([data], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "evidence-templates.json";
  a.click();
  URL.revokeObjectURL(a.href);
}

function importTemplates(file) {
  const reader = new FileReader();
  reader.onload = () => {
    try {
      const imported = JSON.parse(reader.result);
      if (!Array.isArray(imported)) throw new Error("Expected an array");
      const existing = loadTemplates();
      const existingIds = new Set(existing.map(t => t.id));
      for (const tpl of imported) {
        if (!tpl.id || !tpl.name) continue;
        if (existingIds.has(tpl.id)) {
          const idx = existing.findIndex(t => t.id === tpl.id);
          existing[idx] = tpl;
        } else {
          existing.push(tpl);
        }
      }
      saveTemplates(existing);
      refreshTemplateDropdown();
      renderTemplateList();
    } catch (err) {
      alert(`Import failed: ${err.message}`);
    }
  };
  reader.readAsText(file);
}

// --- Auth UI ---

document.getElementById("auth-logout")?.addEventListener("click", () => {
  setStoredAPIKey("");
});

document.getElementById("auth-login")?.addEventListener("click", () => {
  promptForAPIKey("Enter your API key:");
});

// --- Init ---

(async function init() {
  startHealthPolling();
  updateAuthUI();
  refreshTemplateDropdown();
  document.querySelector('#add-form [name="finished_at"]').value = formatTime(new Date().toISOString());
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
