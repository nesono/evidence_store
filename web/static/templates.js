// Named presets for the Add Result form.
//
// A bench that runs the same handful of manual procedures fills in the same
// six fields every time; a template is those fields saved under a name. They
// live in localStorage rather than on the server because they are one person's
// convenience on one machine, not something the store has an opinion about.
//
// Moved out of app.js unchanged (issue #124), apart from the wiring: the DOM
// listeners now attach in mountTemplates rather than at import, so this module
// can be loaded by a test that has no document.

import { esc } from "./common.js";
import {
  DEFAULT_EVIDENCE_TYPE, EVIDENCE_TYPES, EVIDENCE_TYPE_LABELS, evidenceTypeOr,
} from "./evidencetype.js";

// mountTemplates wires the controls and fills the dropdown. Called once, from
// the page's startup, so that importing this module does nothing on its own.
export function mountTemplates() {
  document.getElementById("template-select").addEventListener("change", (e) => {
    applyTemplate(e.target.value);
  });

  document.getElementById("close-template-dialog").addEventListener("click", () => {
    templateDialog().close();
  });

  document.getElementById("template-manage").addEventListener("click", () => {
    renderTemplateList();
    templateDialog().showModal();
  });

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
    templateDialog().showModal();
  });

  refreshTemplateDropdown();
}

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

export function refreshTemplateDropdown() {
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

export function applyTemplate(templateId) {
  const form = document.getElementById("add-form");
  const cfList = document.getElementById("custom-fields-list");

  if (!templateId) {
    for (const f of TEMPLATE_DEFAULT_FIELDS) {
      const input = form.querySelector(`[name="${f}"]`);
      // A pinned source is the server's answer, not a field to clear.
      if (input && !input.readOnly) input.value = f === "evidence_type" ? DEFAULT_EVIDENCE_TYPE : "";
    }
    cfList.innerHTML = "";
    return;
  }

  const tpl = loadTemplates().find(t => t.id === templateId);
  if (!tpl) return;

  for (const f of TEMPLATE_DEFAULT_FIELDS) {
    const input = form.querySelector(`[name="${f}"]`);
    if (!input || input.readOnly) continue;
    const saved = (tpl.defaults && tpl.defaults[f]) || "";
    input.value = f === "evidence_type" ? evidenceTypeOr(saved) : saved;
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

// --- Template Management Dialog ---

// Looked up on use rather than held: this module is imported before the
// document is necessarily ready, and a stale reference would be worse than
// a lookup that costs nothing.
function templateDialog() {
  return document.getElementById("template-dialog");
}

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
          <label>Evidence type
            <select id="tpl-def-evidence_type">
              <option value="">— form default —</option>
              ${EVIDENCE_TYPES.map(t => `<option value="${t}"${defaults.evidence_type === t ? " selected" : ""}>${EVIDENCE_TYPE_LABELS[t]}</option>`).join("")}
            </select>
          </label>
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
