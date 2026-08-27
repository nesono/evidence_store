import {
  API_BASE,
  apiFetch,
  apiFetchNoRedirect,
  esc,
  formatTime,
  getStoredAPIKey,
  goToLogin,
  logout,
  promptForAPIKey,
  resultBadge,
  setAuthMode,
} from "./common.js";
import { showAnalytics } from "./analytics.js";
import { mount as mountAccess, showAccess } from "./access.js";
import { attachRangePicker } from "./datepicker.js";
import { parseUserDateTime } from "./datetime.js";
import { renderMarkdown } from "./markdown.js";
import { attachImageUploads, hydrateImages, releaseImages } from "./images.js";
import {
  formatAccuracy,
  formatCoordinates,
  mapURL,
  parseCoordinates,
  requestPosition,
} from "./location.js";
import { composeWeather, describeReading, fetchWeather, weatherPoint } from "./weather.js";
import { OFFLINE, connectionState, onConnectionChange, registerServiceWorker, startConnectionIndicator } from "./offline.js";
import {
  BLOCKED, STALE_URGENT_DAYS, STALE_WARN_DAYS,
  ageInDays, assessDurability, createOutbox, heldFrom, newEntry, openStore,
  roomIsTight, staleness,
} from "./outbox.js";
import { describeProgress, describeSync, formatBytes, progressFraction, syncOutbox } from "./sync.js";
import { useStash } from "./images.js";
import { digestsInRecord } from "./blobref.js";
import { setValue } from "./editing.js";

// Fields shown in the collapsed bar; everything else lives behind "More filters".
// `ref` is one box matching a branch, a tag or a commit, the same as analytics
// offers — a record has exactly one of each, so filtering on a combination of
// them only ever narrowed to nothing.
const BAR_TEXT_FIELDS = ["repo", "ref"];
const ADVANCED_TEXT_FIELDS = ["evidence_type", "source", "procedure_ref", "tags", "notes"];
const TEXT_FIELDS = [...BAR_TEXT_FIELDS, ...ADVANCED_TEXT_FIELDS];
const DATETIME_FIELDS = ["finished_after", "finished_before"];
// Badged onto the toggle, so a collapsed panel never hides an applied constraint.
const ADVANCED_FIELDS = [...ADVANCED_TEXT_FIELDS, ...DATETIME_FIELDS, "include_inherited"];

// How the evidence was collected: a closed set of three (DESIGN.md §2.2). The
// stored values are slugs and the labels are what a reader is shown, so the
// column that used to hold `bazel`, `pytest` and `junit` reads as one thing.
const EVIDENCE_TYPES = ["ci", "manual_test", "demonstration"];
const EVIDENCE_TYPE_LABELS = {
  ci: "CI",
  manual_test: "Manual Test",
  demonstration: "Demonstration",
};
const DEFAULT_EVIDENCE_TYPE = "manual_test";

// A record from a store that has not run migration 000006 yet, or one written
// around the API, still has to render as something — so an unknown value shows
// itself rather than becoming blank.
function evidenceTypeLabel(value) {
  return EVIDENCE_TYPE_LABELS[value] || value || "";
}

// Templates were saved when the field was a free-text box, so one can still be
// holding `bazel`. A value the select does not have leaves the control blank
// and the form unsubmittable, so anything unrecognised falls back.
function evidenceTypeOr(value, fallback = DEFAULT_EVIDENCE_TYPE) {
  return EVIDENCE_TYPES.includes(value) ? value : fallback;
}

// The list endpoint's default ordering. Named because reading a window from the
// far end has to ask for the exact reverse of it.
const DEFAULT_SORT_COLUMN = "ingested_at";

const WINDOW_SIZES = [25, 50, 100, 200, 500];
const DEFAULT_WINDOW_SIZE = 50;

// The current window into the result set.
let windowOffset = 0;
let windowSize = DEFAULT_WINDOW_SIZE;
let sortColumn = "";     // "" means the API's default ordering
let sortDesc = false;
let totalRecords = null; // cached, so only a filter change pays for the COUNT(*)

// --- Preferences ---

const WINDOW_SIZE_KEY = "evidence_window_size";

function loadPref(key, fallback) {
  try {
    const raw = localStorage.getItem(key);
    return raw === null ? fallback : JSON.parse(raw);
  } catch {
    return fallback;
  }
}

function savePref(key, value) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch { /* storage unavailable (private mode); preference just won't persist */ }
}

function normalizeWindowSize(n) {
  return WINDOW_SIZES.includes(n) ? n : DEFAULT_WINDOW_SIZE;
}

// --- URL State ---

function readStateFromURL() {
  const params = new URLSearchParams(window.location.search);
  const filters = {};
  for (const f of [...TEXT_FIELDS, ...DATETIME_FIELDS]) {
    if (params.has(f)) filters[f] = params.get(f);
  }
  if (params.has("result")) filters.result = params.get("result");
  if (params.has("include_inherited")) filters.include_inherited = params.get("include_inherited");

  // Links made before the two identity fields became one still carry `branch`
  // or `rcs_ref`. Both are still valid API filters, but the form no longer has
  // a box for either, and a filter with nowhere to show is a filter the user
  // cannot see or clear — so they are folded into `ref`, which matches either
  // column. A link carrying both keeps the commit, as the more specific of the
  // two.
  if (!filters.ref) {
    const legacy = params.get("rcs_ref") || params.get("branch");
    if (legacy) filters.ref = legacy;
  }

  // Same rule for a link carrying a type that no longer exists — `bazel`, from
  // before the taxonomy. The dropdown cannot show it, so keeping it would search
  // on a constraint the user can neither see nor clear, and every such link
  // would come up empty for no visible reason. Dropping it shows the records.
  if (filters.evidence_type && !EVIDENCE_TYPES.includes(filters.evidence_type)) {
    delete filters.evidence_type;
  }

  const offset = parseInt(params.get("offset"), 10);
  windowOffset = Number.isFinite(offset) && offset > 0 ? offset : 0;

  const limit = parseInt(params.get("limit"), 10);
  windowSize = Number.isFinite(limit)
    ? normalizeWindowSize(limit)
    : normalizeWindowSize(loadPref(WINDOW_SIZE_KEY, DEFAULT_WINDOW_SIZE));

  sortColumn = params.get("sort") || "";
  sortDesc = params.get("order") === "desc";

  // A `cursor=` from an older link is deliberately ignored. It is an opaque
  // position marker: it cannot say which window it refers to, so the view would
  // have no range to show and no way back. Those links open at the first window.
  return { filters, detail: params.get("detail") };
}

function writeStateToURL(filters, detail) {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) {
    if (v !== "" && v !== null && v !== undefined) {
      params.set(k, v);
    }
  }
  if (windowOffset > 0) params.set("offset", String(windowOffset));
  params.set("limit", String(windowSize));
  if (sortColumn) {
    params.set("sort", sortColumn);
    params.set("order", sortDesc ? "desc" : "asc");
  }
  if (detail) params.set("detail", detail);
  history.pushState(null, "", `?${params}`);
}

function populateFormFromFilters(filters) {
  const form = document.getElementById("filter-form");
  for (const f of TEXT_FIELDS) {
    const input = form.querySelector(`[name="${f}"]`);
    if (input) input.value = filters[f] || "";
  }
  for (const f of DATETIME_FIELDS) {
    const input = form.querySelector(`[name="${f}"]`);
    if (input) {
      if (filters[f]) {
        const d = parseUserDateTime(filters[f]);
        input.value = d ? formatTime(d.toISOString()) : filters[f];
      } else {
        input.value = "";
      }
    }
  }
  const activeResults = (filters.result || "").split(",").filter(Boolean);
  form.querySelectorAll('[name="result"]').forEach(cb => {
    cb.checked = activeResults.includes(cb.value);
  });
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
  if (!form.querySelector('[name="include_inherited"]').checked) {
    filters.include_inherited = "false";
  }
  return filters;
}

// --- Filter panel ---

function activeAdvancedCount(filters) {
  let n = 0;
  for (const f of ADVANCED_FIELDS) {
    if (f === "include_inherited") {
      if (filters.include_inherited === "false") n++;
    } else if (filters[f]) {
      n++;
    }
  }
  return n;
}

// Initials keep the closed control narrow; the full names go in the tooltip so
// the abbreviation is never the only way to read the filter.
const RESULT_INITIALS = { PASS: "P", FAIL: "F", ERROR: "E", SKIPPED: "S" };

// The result checkboxes live in a closed dropdown most of the time, so the
// summary has to say what is selected — otherwise an active filter is invisible.
function refreshResultSummary() {
  const selected = Array.from(
    document.querySelectorAll('#result-dropdown [name="result"]:checked')
  ).map(cb => cb.value);

  const label = document.getElementById("result-summary");
  if (selected.length === 0) {
    label.textContent = "All Results";
    label.closest("summary").title = "Filter by result (all results shown)";
    return;
  }
  label.textContent = selected.map(v => RESULT_INITIALS[v] || v).join(", ");
  label.closest("summary").title = selected.join(", ");
}

function advancedExpanded() {
  return document.getElementById("toggle-advanced").getAttribute("aria-expanded") === "true";
}

function setAdvancedExpanded(expanded) {
  document.getElementById("filter-advanced").hidden = !expanded;
  document.getElementById("toggle-advanced").setAttribute("aria-expanded", String(expanded));
  refreshAdvancedToggle();
}

function refreshAdvancedToggle() {
  const btn = document.getElementById("toggle-advanced");
  if (advancedExpanded()) {
    btn.textContent = "Fewer filters";
    return;
  }
  const n = activeAdvancedCount(readFormFilters());
  btn.textContent = n > 0 ? `More filters (${n})` : "More filters";
}

// --- API ---

// fetchWindow retrieves the current window of records.
//
// A window near the end of a large result set is expensive to read directly:
// OFFSET makes Postgres walk every skipped row, which at two million records
// costs seconds. Approaching from whichever end is closer keeps that walk short,
// so jumping to the last window is as cheap as the first. This relies on
// descending order being the exact reverse of ascending, which holds because the
// API's id tie-break follows the sort direction.
async function fetchWindow(filters) {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) {
    if (v !== "" && v !== null && v !== undefined) {
      params.set(k, v);
    }
  }

  // The total only changes when the filters do, so it is fetched once and cached.
  if (totalRecords === null) {
    params.set("include_total", "true");
  } else {
    params.set("include_total", "false");
  }

  let limit = windowSize;
  let offset = windowOffset;
  const fromFarEnd = totalRecords !== null && windowOffset > totalRecords / 2;

  if (fromFarEnd) {
    limit = Math.min(windowSize, Math.max(0, totalRecords - windowOffset));
    offset = Math.max(0, totalRecords - windowOffset - limit);
  }

  if (limit <= 0) {
    // Window sits entirely past the end; let the caller clamp and retry.
    return { records: [], total: totalRecords };
  }

  params.set("limit", String(limit));
  if (offset > 0) params.set("offset", String(offset));

  if (fromFarEnd || sortColumn) {
    params.set("sort", sortColumn || DEFAULT_SORT_COLUMN);
    const desc = fromFarEnd ? !sortDesc : sortDesc;
    params.set("order", desc ? "desc" : "asc");
  }

  const resp = await apiFetch(`${API_BASE}/evidence?${params}`);
  if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  const data = await resp.json();

  if (fromFarEnd && data.records) {
    data.records = data.records.slice().reverse();
  }
  return data;
}

async function fetchEvidenceById(id) {
  const resp = await apiFetch(`${API_BASE}/evidence/${id}`);
  if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  return resp.json();
}

// evidence_type is not here any more: it is a closed set of three offered as a
// dropdown, so there is nothing to complete from what the store happens to hold.
const DATALIST_FIELDS = [
  { field: "repo",   listId: "repos-list" },
  { field: "source", listId: "sources-list" },
];

async function refreshDatalists() {
  await Promise.all(DATALIST_FIELDS.map(async ({ field, listId }) => {
    const resp = await apiFetch(`${API_BASE}/evidence/distinct?field=${field}&limit=500`);
    if (!resp.ok) return;
    const { values } = await resp.json();
    const list = document.getElementById(listId);
    if (!list) return;
    list.innerHTML = (values || []).map(v => `<option value="${esc(v)}"></option>`).join("");
  }));
}

// --- Rendering ---

function renderTags(metadata) {
  if (!metadata || !metadata.tags || metadata.tags.length === 0) return "";
  return metadata.tags.map(t => `<span class="badge badge-tag">${esc(t)}</span>`).join(" ");
}

function rowHTML(r) {
  const branch = r.branch || "";
  return `
    <tr data-id="${r.id}" class="${r.inherited ? "inherited-row" : ""}">
      <td class="col-result">${resultBadge(r.result)}</td>
      <td class="col-procedure" title="${esc(r.procedure_ref)}">${esc(r.procedure_ref)}</td>
      <td class="col-repo" title="${esc(r.repo)}">${esc(r.repo)}</td>
      <td class="col-branch" title="${esc(branch)}">${esc(branch)}</td>
      <td class="col-commit commit-ref">${esc((r.rcs_ref || "").slice(0, 10))}</td>
      <td class="col-type">${esc(evidenceTypeLabel(r.evidence_type))}</td>
      <td class="col-source" title="${esc(r.source)}">${esc(r.source)}</td>
      <td class="col-finished">${formatTime(r.finished_at)}</td>
      <td class="col-tags">${renderTags(r.metadata)}</td>
    </tr>`;
}

function renderTable(records) {
  const tbody = document.getElementById("results-body");
  if (!records || records.length === 0) {
    tbody.innerHTML = `<tr><td colspan="9" class="empty-state">No records match these filters</td></tr>`;
    return;
  }
  tbody.innerHTML = records.map(rowHTML).join("");
  // Each window starts at its own top rather than inheriting the previous scroll.
  document.getElementById("results-window").scrollTop = 0;
}

// Inherited records are resolved outside the paginated window, so they are listed
// separately and left out of the range readout.
function renderInherited(records) {
  const panel = document.getElementById("inherited-panel");
  if (!records || records.length === 0) {
    panel.hidden = true;
    document.getElementById("inherited-body").innerHTML = "";
    return;
  }
  panel.hidden = false;
  document.getElementById("inherited-count").textContent = records.length.toLocaleString();
  document.getElementById("inherited-body").innerHTML = records.map(rowHTML).join("");
}

function renderRange(count) {
  const el = document.getElementById("results-summary");
  const n = x => x.toLocaleString();

  if (count === 0) {
    el.textContent = totalRecords ? `no records in this window of ${n(totalRecords)}` : "no records";
    return;
  }
  const from = windowOffset + 1;
  const to = windowOffset + count;

  if (totalRecords === null) {
    el.textContent = `${n(from)}–${n(to)}`;
  } else if (windowOffset === 0 && count === totalRecords) {
    el.textContent = `${n(totalRecords)} record${totalRecords !== 1 ? "s" : ""}`;
  } else {
    el.textContent = `${n(from)}–${n(to)} of ${n(totalRecords)} records`;
  }
}

function lastWindowOffset() {
  if (!totalRecords) return 0;
  return Math.max(0, Math.floor((totalRecords - 1) / windowSize) * windowSize);
}

function renderWindowNav() {
  const atStart = windowOffset === 0;
  const atEnd = totalRecords === null || windowOffset >= lastWindowOffset();
  document.getElementById("first-window").disabled = atStart;
  document.getElementById("prev-page").disabled = atStart;
  document.getElementById("next-page").disabled = atEnd;
  document.getElementById("last-window").disabled = atEnd;
}

// Where the test was run, as a detail field. Returns "" when the record does
// not say, and — as with the log below — leaves anything that is not a string
// alone, since that is some other client's field and not a place.
function renderLocation(metadata) {
  if (typeof metadata.location !== "string" || !metadata.location.trim()) return "";
  const text = metadata.location.trim();

  let html = esc(text);
  // Only a coordinate pair gets a map link, and the link is never followed
  // until the reader clicks it: showing a record must not tell a map service
  // what someone is reading.
  const coords = parseCoordinates(text);
  if (coords) {
    html += ` <a href="${mapURL(coords)}" target="_blank" rel="noopener noreferrer">map</a>`;
  }
  // A point without its margin reads as certainty the device never had.
  const accuracy = formatAccuracy(metadata.location_accuracy_m);
  if (accuracy) html += ` <small class="location-accuracy">${accuracy}</small>`;
  return html;
}

// What the weather was doing, as a detail field.
//
// The hour beside it is what separates a reading from a description: a line the
// tester wrote has no hour and does not get one, and a line the service gave is
// for an hour that is not the minute of the test. Anything that is not a string
// is some other client's field and stays in the dump.
function renderWeather(metadata) {
  if (typeof metadata.weather_conditions !== "string" || !metadata.weather_conditions.trim()) {
    return "";
  }

  let html = esc(metadata.weather_conditions.trim());
  if (typeof metadata.weather_observed_at === "string") {
    const observed = new Date(metadata.weather_observed_at);
    if (!Number.isNaN(observed.getTime())) {
      html += ` <small class="weather-observed">reading for ${formatTime(metadata.weather_observed_at)} UTC</small>`;
    }
  }
  return html;
}

function renderDetail(record) {
  const el = document.getElementById("detail-content");
  const metadata = record.metadata || {};
  const rest = { ...metadata };

  // Location sits with the record's own fields rather than in the metadata dump:
  // where a manual test was run is part of what it proves, and a reader looking
  // for it should not have to read JSON to find out.
  const location = renderLocation(metadata);
  if (location) {
    delete rest.location;
    delete rest.location_accuracy_m;
  }

  // Weather goes beside it for the same reason: braking distance on a wet
  // surface is a different measurement from braking distance on a dry one, and
  // a reader comparing two records needs to see which without reading JSON.
  const weather = renderWeather(metadata);
  if (weather) {
    delete rest.weather_conditions;
    delete rest.weather_observed_at;
  }

  const fields = [
    ["ID", record.id],
    ["Result", resultBadge(record.result)],
    ["Repo", esc(record.repo)],
    ["Branch", esc(record.branch || "")],
    ["Commit", `<span class="commit-ref">${esc(record.rcs_ref)}</span>`],
    ["Procedure", esc(record.procedure_ref)],
    ["Type", esc(evidenceTypeLabel(record.evidence_type))],
    ["Source", esc(record.source)],
    ...(location ? [["Location", location]] : []),
    ...(weather ? [["Weather", weather]] : []),
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

  // The tester's log comes up with the record rather than behind a click: it is
  // the substance of a manual result, and the fields above are just its label.
  // It is lifted out of the metadata dump below because a log rendered as one
  // JSON string of escaped newlines is a log nobody reads.
  //
  // Anything but a string under `observations` is some other client's field and
  // stays in the dump, where it is at least visible, rather than being rendered
  // as a log or silently dropped from both places.
  const log = typeof metadata.observations === "string" ? metadata.observations : "";
  if (log.trim()) {
    delete rest.observations;
    html += `<div class="metadata-block">
      <strong>Test log</strong>
      <div class="test-log">${renderMarkdown(log)}</div>
    </div>`;
  }

  if (Object.keys(rest).length > 0) {
    html += `<div class="metadata-block"><strong>Metadata</strong><pre><code>${esc(JSON.stringify(rest, null, 2))}</code></pre></div>`;
  }

  el.innerHTML = html;
  // The log's images are fetched once the markup is in the document: the
  // renderer leaves them without a src because reading a blob needs the API key.
  hydrateImages(el);
  document.getElementById("detail-dialog").showModal();
}

// --- Search ---

async function doSearch(filters, { clamped = false } = {}) {
  const tbody = document.getElementById("results-body");
  tbody.innerHTML = `<tr><td colspan="9" class="empty-state">Loading...</td></tr>`;

  try {
    const data = await fetchWindow(filters);
    if (data.total !== undefined && data.total !== null) {
      totalRecords = data.total;
    }

    // The requested window can sit past the end — a stale deep link, a smaller
    // result set than last time, or a larger window size. Clamp once and refetch.
    if (!clamped && totalRecords !== null && windowOffset > 0 && windowOffset >= totalRecords) {
      windowOffset = lastWindowOffset();
      writeStateToURL(filters);
      return doSearch(filters, { clamped: true });
    }

    renderTable(data.records);
    renderInherited(data.inherited_records);
    renderRange((data.records || []).length);
    renderWindowNav();
  } catch (err) {
    // "Failed to fetch" is what the browser says and not what happened. With
    // no connection there is nothing wrong with the search or the store: the
    // archive is simply somewhere else, and saying so stops a tester on a
    // proving ground hunting for a fault that is not there.
    const message = connectionState() === OFFLINE
      ? "Offline \u2014 searching needs a connection. Filing a result does not."
      : `Error: ${esc(err.message)}`;
    tbody.innerHTML = `<tr><td colspan="9" class="empty-state">${message}</td></tr>`;
    document.getElementById("results-summary").textContent = "";
    renderInherited(null);
    renderWindowNav();
  }
}

// Re-runs the search from the first window, discarding the cached total because
// changing the filters changes the count.
function search() {
  windowOffset = 0;
  totalRecords = null;
  const filters = readFormFilters();
  refreshAdvancedToggle();
  writeStateToURL(filters);
  doSearch(filters);
}

// Moves the window without re-counting — the filters, and so the total, are
// unchanged.
function moveWindow(offset) {
  const target = Math.max(0, Math.min(offset, lastWindowOffset()));
  if (target === windowOffset) return;
  windowOffset = target;
  const filters = readFormFilters();
  writeStateToURL(filters);
  doSearch(filters);
}

// --- Events ---

document.getElementById("filter-form").addEventListener("submit", (e) => {
  e.preventDefault();
  search();
});

document.getElementById("clear-filters").addEventListener("click", () => {
  const form = document.getElementById("filter-form");
  form.reset();
  form.querySelectorAll("input[data-utc-preview]").forEach(updateUtcPreview);
  refreshResultSummary();
  // An open calendar still showing the range that was just cleared would be
  // reading from a form that no longer says that.
  rangePicker.close({ restoreFocus: false });
  search();
});

document.getElementById("toggle-advanced").addEventListener("click", () => {
  setAdvancedExpanded(!advancedExpanded());
});

// --- Finished-at range ---

const finishedRange = document.getElementById("finished-range");

const rangePicker = attachRangePicker({
  root: finishedRange,
  fromInput: finishedRange.querySelector('[name="finished_after"]'),
  toInput: finishedRange.querySelector('[name="finished_before"]'),
  // Picking a range is the whole interaction; making the user find the Search
  // button afterwards would be a step with nothing in it.
  onApply: search,
});

// The × on each box. Dropping one end of the range is a single click, and the
// other end stays where it is — going back to "no start" or "no end" should not
// cost the user the half of the filter they still want.
finishedRange.addEventListener("click", (e) => {
  const clear = e.target.closest("[data-clears]");
  if (!clear) return;
  const input = finishedRange.querySelector(`[name="${clear.dataset.clears}"]`);
  if (!input.value) return;
  input.value = "";
  updateUtcPreview(input);
  refreshAdvancedToggle();
  search();
});

// The boxes are still typed into directly, and the badge on the collapsed panel
// has to count what is in them either way.
finishedRange.addEventListener("input", refreshAdvancedToggle);

// --- Result dropdown ---

const resultDropdown = document.getElementById("result-dropdown");

resultDropdown.addEventListener("change", refreshResultSummary);

// <details> has no dismiss behaviour of its own, so closing on an outside click
// and on Escape has to be wired up.
document.addEventListener("click", (e) => {
  if (resultDropdown.open && !resultDropdown.contains(e.target)) {
    resultDropdown.open = false;
  }
});

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && resultDropdown.open) {
    resultDropdown.open = false;
    resultDropdown.querySelector("summary").focus();
  }
});

document.getElementById("first-window").addEventListener("click", () => moveWindow(0));
document.getElementById("prev-page").addEventListener("click", () => moveWindow(windowOffset - windowSize));
document.getElementById("next-page").addEventListener("click", () => moveWindow(windowOffset + windowSize));
document.getElementById("last-window").addEventListener("click", () => moveWindow(lastWindowOffset()));

document.getElementById("window-size").addEventListener("change", (e) => {
  const size = normalizeWindowSize(parseInt(e.target.value, 10));
  // Keep the first record of the current window visible across the size change,
  // so the view stays roughly where the user was rather than jumping to the top.
  const anchor = windowOffset;
  windowSize = size;
  savePref(WINDOW_SIZE_KEY, size);
  windowOffset = Math.floor(anchor / size) * size;
  const filters = readFormFilters();
  writeStateToURL(filters);
  doSearch(filters);
});

// Window navigation from the keyboard, ignored while typing in the filter form.
document.addEventListener("keydown", (e) => {
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  const tag = e.target.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
  if (document.querySelector("dialog[open]")) return;
  // The calendar's own PageUp/PageDown page it by month; paging the results
  // window underneath at the same time would be nobody's intent.
  if (rangePicker.isOpen()) return;
  if (document.getElementById("tab-search").hidden) return;

  switch (e.key) {
    case "PageDown": moveWindow(windowOffset + windowSize); break;
    case "PageUp":   moveWindow(windowOffset - windowSize); break;
    case "Home":     moveWindow(0); break;
    case "End":      moveWindow(lastWindowOffset()); break;
    default: return;
  }
  e.preventDefault();
});

async function openDetail(id) {
  try {
    const record = await fetchEvidenceById(id);
    renderDetail(record);
    writeStateToURL(readFormFilters(), id);
  } catch (err) {
    alert(`Failed to load record: ${err.message}`);
  }
}

document.getElementById("results-body").addEventListener("click", (e) => {
  const row = e.target.closest("tr[data-id]");
  if (row) openDetail(row.dataset.id);
});

document.getElementById("inherited-body").addEventListener("click", (e) => {
  const row = e.target.closest("tr[data-id]");
  if (row) openDetail(row.dataset.id);
});

document.getElementById("close-detail").addEventListener("click", () => {
  document.getElementById("detail-dialog").close();
  writeStateToURL(readFormFilters());
});

// On the dialog itself rather than the close button: Escape closes it too, and
// the images a closed dialog was showing are worth handing back either way.
document.getElementById("detail-dialog").addEventListener("close", releaseImages);

window.addEventListener("popstate", () => {
  const { filters } = readStateFromURL();
  applyURLState(filters);
  // Going back may land on a different filter set, so the cached count no longer
  // applies.
  totalRecords = null;
  doSearch(filters);
});

// Reflects URL-derived state into the form and the window controls.
function applyURLState(filters) {
  populateFormFromFilters(filters);
  refreshResultSummary();
  document.getElementById("window-size").value = String(windowSize);
  // A deep link carrying an advanced filter opens the panel, so the constraint
  // that is shaping the results is never invisible.
  setAdvancedExpanded(activeAdvancedCount(filters) > 0);
}

// --- Tabs ---

document.querySelectorAll(".nav-tab").forEach(tab => {
  tab.addEventListener("click", (e) => {
    e.preventDefault();
    const target = tab.dataset.tab;
    document.querySelectorAll(".nav-tab").forEach(t => t.classList.remove("active"));
    tab.classList.add("active");
    document.querySelectorAll(".tab-content").forEach(s => s.hidden = true);
    document.getElementById(`tab-${target}`).hidden = false;
    if (target === "analytics") showAnalytics();
    if (target === "access") showAccess();
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
  // `observations` is the field DESIGN.md gives the manual evidence type for the
  // tester's own account of the run.
  const observations = form.observations.value.trim();
  if (observations) metadata.observations = observations;
  const location = form.location.value.trim();
  if (location) {
    metadata.location = location;
    // The margin belongs to the fix, not to the field: it is filed only while
    // the text is still the one the device produced. Typing over it — even to
    // correct it — makes it a place the tester named, and a metre count from a
    // reading that is no longer shown would be a claim about someone else's.
    const accuracy = Number(form.location.dataset.accuracyM);
    if (form.location.dataset.fromDevice === location && Number.isFinite(accuracy)) {
      metadata.location_accuracy_m = accuracy;
    }
  }
  const weather = form.weather_conditions.value.trim();
  if (weather) {
    metadata.weather_conditions = weather;
    // Which hour the reading was for is filed only while the line is still the
    // service's, the same rule the location's accuracy follows: an edited line
    // is the tester's account of the weather, and an hour attached to it would
    // dress that up as a reading nobody can go back and check.
    if (form.weather_conditions.dataset.fromService === weather &&
        form.weather_conditions.dataset.observedAt) {
      metadata.weather_observed_at = form.weather_conditions.dataset.observedAt;
    }
  }
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

  // The submission gets its identity here, at capture, whether or not it is
  // about to reach anything. If it is queued and sent days later, it goes with
  // the same token, so a retry files one record rather than two.
  //
  // Editing a queued record reuses its token: the corrected version is the
  // same submission, not a second one.
  const entry = newEntry(record, { id: editingEntryID || undefined, capturedBy: currentSubject });

  try {
    if (connectionState() === OFFLINE) {
      // Do not spend a timeout finding out what the header already says.
      await queueEntry(entry, feedback, andAnother, form);
      return;
    }

    const resp = await apiFetch(`${API_BASE}/evidence`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(entry.record),
    });
    const data = await resp.json();
    if (!resp.ok) {
      const msg = data.errors ? data.errors.map(e => e.message || e).join(", ") : (data.error || JSON.stringify(data));
      feedback.innerHTML = `<p class="feedback-error">${esc(msg)}</p>`;
      return;
    }
    // Filed, so it is not waiting any more. This matters when the record came
    // back out of the outbox to be corrected: the queued copy goes now.
    if (editingEntryID) {
      await outbox.remove(editingEntryID);
      editingEntryID = null;
      await refreshOutboxCount();
    }
    refreshDatalists();
    if (andAnother) {
      feedback.innerHTML = `<p class="feedback-ok">Created <code>${data.id}</code></p>`;
      resetFormForNext(form);
    } else {
      // Submit ends the sitting, and a weather line does not keep: it is a
      // reading for one hour, and the next record filled in on this form could
      // be days later. Leaving it in the box is how yesterday's sky ends up
      // filed as today's.
      clearWeather();
      feedback.innerHTML = `<p class="feedback-ok">Created <code>${data.id}</code> &mdash; switching to search...</p>`;
      setTimeout(() => {
        document.querySelector('[data-tab="search"]').click();
        feedback.innerHTML = "";
      }, 1000);
    }
  } catch (err) {
    // The post did not reach the store. That is not a reason to lose a record
    // somebody was standing in a field to write, so it goes in the queue and
    // the tester is told where it went. The token it already carries is what
    // makes sending it again safe, even if this attempt did in fact land.
    await queueEntry(entry, feedback, andAnother, form, err);
  } finally {
    btn.removeAttribute("aria-busy");
  }
}

// queueEntry puts a record in the outbox and says so plainly. A tester has to
// be able to tell "filed" from "waiting" at a glance — they are different
// states of the evidence, and only one of them is safe to walk away from.
async function queueEntry(entry, feedback, andAnother, form, err) {
  await outbox.save(entry);
  editingEntryID = null;
  await refreshOutboxCount();

  const why = err ? `Could not reach the store (${esc(err.message)}).` : "Offline.";
  feedback.innerHTML =
    `<p class="feedback-ok">${why} Saved here and it will be sent when there is a connection ` +
    `&mdash; <a href="#" class="outbox-open">see what is waiting</a>.</p>` +
    // Said at the moment the tester first relies on the queue, not buried in a
    // dialog they may never open. A private window is the usual reason, and
    // finding out afterwards means finding out by losing a day of evidence.
    (durability.level === "session"
      ? `<p class="feedback-error">This browser is not storing anything on disk &mdash; ` +
        `these records will be gone when you close this tab. Send them before you do.</p>`
      : "");
  feedback.querySelector(".outbox-open").addEventListener("click", event => {
    event.preventDefault();
    openOutbox();
  });

  if (andAnother) resetFormForNext(form);
}

// resetFormForNext clears what belongs to the run just filed and keeps what
// belongs to the sitting.
//
// Location and weather are deliberately kept: a tester filing several runs in a
// row is still standing where they were, under the same sky, and looking both
// up again for each one is how the fields stop being filled. The reading is an
// hour wide, which is longer than a burst of manual records takes.
function resetFormForNext(form) {
  const chosen = form.querySelector('[name="result"]:checked');
  if (chosen) chosen.checked = false;
  form.notes.value = "";
  form.observations.value = "";
  updateTestLogPreview();
  const currentTpl = document.getElementById("template-select").value;
  if (currentTpl) {
    applyTemplate(currentTpl);
  } else {
    document.getElementById("custom-fields-list").innerHTML = "";
  }
}

// --- Which build is answering ---

// Asked for directly rather than through apiFetch: /version is public, sits
// outside /api/v1, and sending a credential to it would be sending one where
// none is wanted.
//
// A failure is silent and leaves the footer reading just "Evidence Store".
// There is nothing for a tester to do about it, and an error line about a
// version number would be noise on a page that has just failed to reach its
// server for reasons they can already see in the header.
async function showServerVersion() {
  const el = document.getElementById("server-version");
  if (!el) return;
  try {
    const resp = await fetch("/version", { cache: "no-store" });
    if (!resp.ok) return;
    const { version, source } = await resp.json();
    if (!version) return;
    el.textContent = version;
    el.title = source === "build"
      ? "The build the server is running, by the minute it was built (UTC)"
      : source === "commit"
        ? "Built from a commit made at this time (UTC); the build itself was not stamped"
        : "This server was built from a working copy, so it matches no particular commit";
  } catch {
    // Offline, or the server is not answering. The header already says so.
  }
}

// --- The outbox ---

// Records written with nowhere to send them. See docs/offline-support-plan.md.
let outbox = null;
// Who is signed in, remembered so a queued record can record who wrote it.
let currentSubject = null;
// The queued record currently loaded back into the form, if any. Its token is
// reused on submit, so correcting a record replaces it rather than filing a
// second copy of the same run.
let editingEntryID = null;
// One sync at a time. Reconnecting, loading and pressing the button can all
// arrive together, and three syncs racing would report three different things.
let syncing = false;

// What this browser has promised about the queue, learned once at startup.
let durability = { level: "durable", persisted: true };

async function mountOutbox() {
  const store = await openStore();
  outbox = createOutbox(store);
  // Asked for before anything is queued, so the promise is in place by the
  // time there is something to keep.
  durability = await assessDurability(store);
  // Photos attached from now on are named and kept here rather than uploaded,
  // so a log written with no connection is finished when the tester writes it.
  useStash(outbox);
  // Bytes belonging to logs that were never filed, from some earlier sitting.
  outbox.sweepBlobs().catch(() => {});
  await refreshOutboxCount();

  document.getElementById("outbox-status").addEventListener("click", event => {
    event.preventDefault();
    openOutbox();
  });
  document.getElementById("close-outbox").addEventListener("click", () => {
    document.getElementById("outbox-dialog").close();
  });
  document.getElementById("outbox-send").addEventListener("click", async () => {
    await runSync({ announce: true });
    await renderOutbox();
  });

  // The moment a signal returns is rarely a moment anyone is looking at the
  // page — a laptop lid opening in a hotel lobby is the whole opportunity.
  onConnectionChange(state => {
    if (state !== OFFLINE) runSync();
  });
}

async function refreshOutboxCount() {
  const el = document.getElementById("outbox-status");
  if (!outbox || !el) return;
  const entries = await outbox.list();
  el.hidden = entries.length === 0;
  if (entries.length === 0) {
    // Cleared, not just hidden. Leaving "3 unsent for 45 days" behind an empty
    // queue means it reappears the moment one record is queued again, saying
    // something that stopped being true weeks ago.
    el.textContent = "";
    el.title = "";
    el.classList.remove("outbox-status-urgent");
    return;
  }

  const blocked = entries.filter(e => e.state === BLOCKED).length;
  const needs = n => `${n} need${n === 1 ? "s" : ""} attention`;
  if (blocked === 0) {
    el.textContent = `${entries.length} waiting to send`;
  } else if (blocked === entries.length) {
    // Saying "3 waiting, 3 need attention" reads as six records.
    el.textContent = needs(blocked);
  } else {
    el.textContent = `${entries.length} waiting, ${needs(blocked)}`;
  }

  // How long the oldest has been waiting, once that becomes the more useful
  // fact than how many there are. Nothing expires and nothing is refused; the
  // point is to say so while the evidence is still there to save.
  const stale = staleness(entries);
  el.classList.toggle("outbox-status-urgent", stale.level === "urgent");
  if (stale.level !== "none") {
    el.textContent = `${entries.length} unsent for ${stale.days} days`;
    el.title = stale.level === "urgent"
      ? `The oldest record here was written ${stale.days} days ago and has never reached the store.`
      : `Waiting ${stale.days} days. Send these while they are still here.`;
  } else {
    el.title = "Records written on this device that have not reached the store yet";
  }
}

function openOutbox() {
  document.getElementById("outbox-dialog").showModal();
  renderOutbox();
}

async function renderOutbox() {
  const list = document.getElementById("outbox-list");
  const explainer = document.getElementById("outbox-explainer");
  const entries = await outbox.list();

  if (entries.length === 0) {
    explainer.textContent = "";
    list.innerHTML = `<p class="test-log-empty">Nothing waiting. Everything filed here has reached the store.</p>`;
    return;
  }

  const held = await outbox.bytesHeld();
  explainer.className = "outbox-explainer";
  explainer.textContent = describeDurability(durability, held);
  if (durability.level === "session") explainer.classList.add("outbox-explainer-alarm");
  else if (durability.level === "evictable" || roomIsTight(durability)) {
    explainer.classList.add("outbox-explainer-warn");
  }

  list.innerHTML = entries.map(entry => {
    const r = entry.record;
    const digests = digestsInRecord(r);
    const held = heldFrom(entry, currentSubject);
    const heldClass = entry.state === BLOCKED ? "outbox-entry-error" : "outbox-entry-held";
    return `
      <div class="outbox-entry" data-id="${esc(entry.id)}">
        <div class="outbox-entry-head">
          ${resultBadge(r.result)}
          <span class="outbox-entry-procedure">${esc(r.procedure_ref || "(no procedure)")}</span>
        </div>
        <div class="outbox-entry-meta">
          ${esc(r.repo || "")} ${esc(r.branch || "")} ${esc((r.rcs_ref || "").slice(0, 12))}
          &middot; written ${formatTime(entry.capturedAt)}${describeAge(entry)}
        </div>
        ${digests.length ? `<div class="outbox-entry-photos" data-digests="${esc(digests.join(","))}"></div>` : ""}
        ${held ? `<div class="${heldClass}">${esc(held)}</div>` : ""}
        <div class="outbox-entry-actions">
          ${canLookUpWeather(r) ? `<button class="secondary outline outbox-weather">Look up weather</button>` : ""}
          <button class="secondary outline outbox-edit">Edit</button>
          <button class="secondary outline outbox-delete">Delete</button>
        </div>
      </div>`;
  }).join("");

  // The photographs, from the device's own copy. Seeing them is how a tester
  // knows the pictures are safe and not just the words about them.
  for (const holder of list.querySelectorAll(".outbox-entry-photos")) {
    for (const digest of holder.dataset.digests.split(",")) {
      const blob = await outbox.getBlob(digest);
      if (!blob) continue;
      const img = document.createElement("img");
      img.src = URL.createObjectURL(new Blob([blob.bytes], { type: blob.contentType }));
      img.alt = "attached photo";
      holder.appendChild(img);
    }
  }

  list.querySelectorAll(".outbox-weather").forEach(btn => {
    btn.addEventListener("click", () => lookUpQueuedWeather(btn.closest(".outbox-entry").dataset.id, btn));
  });
  list.querySelectorAll(".outbox-edit").forEach(btn => {
    btn.addEventListener("click", () => editQueued(btn.closest(".outbox-entry").dataset.id));
  });
  list.querySelectorAll(".outbox-delete").forEach(btn => {
    btn.addEventListener("click", () => deleteQueued(btn.closest(".outbox-entry").dataset.id));
  });
}

// describeDurability says plainly what this browser has promised about the
// queue, because a tester deciding whether to keep working in the field is
// entitled to know before they rely on it rather than after.
function describeDurability(assessment, bytesHeld) {
  const photos = bytesHeld ? ` Photos waiting here take up ${formatBytes(bytesHeld)}.` : "";

  if (assessment.level === "session") {
    return "This browser will not store anything on disk, so these records last only " +
      "as long as this tab. Send them before you close it, or file them somewhere else." + photos;
  }

  const base = "These are on this device only, until they are sent. They survive closing the " +
    "browser, and they go automatically when there is a connection." + photos;

  if (assessment.level === "evictable") {
    return base + " This browser has not promised to keep them, so it may reclaim the space " +
      "if the device runs low. Send them when you can.";
  }
  if (roomIsTight(assessment)) {
    return base + " This device is running low on space for them.";
  }
  return base;
}

// canLookUpWeather reports whether the reading is still worth offering.
//
// A record that already carries a weather line is left alone, whoever wrote it:
// overwriting a tester's own account of the sky with a model's would be exactly
// the wrong way round. What is needed is a point to ask about, which means the
// location has to be coordinates rather than "Lab 2, bay 4".
function canLookUpWeather(record) {
  const metadata = record.metadata || {};
  if (metadata.weather_conditions) return false;
  return !!parseCoordinates(metadata.location || "");
}

// lookUpQueuedWeather fills in the weather for a record written earlier.
//
// The lookup wants a point and an hour, and a queued record already carries
// both, so a reading fetched now is still a reading for the hour the test ran
// in. That is what lets weather_observed_at go on it: the record synced on
// Friday for a test run on Tuesday is not passed off as having been checked
// live, and a reader can still go back and check it.
//
// The bound worth knowing is that the service keeps a recent window. Outside
// it the answer is a 404 carrying the service's own account of why, which is
// shown as it stands — it tells a tester what to do next in a way "unavailable"
// cannot, and what to do next is write the weather down.
async function lookUpQueuedWeather(id, btn) {
  const entry = await outbox.get(id);
  if (!entry) return;

  const explainer = document.getElementById("outbox-explainer");
  btn.setAttribute("aria-busy", "true");
  btn.disabled = true;
  try {
    const point = parseCoordinates(entry.record.metadata.location);
    const when = entry.record.finished_at ? new Date(entry.record.finished_at) : null;
    const reading = await fetchWeather(point, when);

    const metadata = { ...entry.record.metadata, weather_conditions: reading.summary };
    if (reading.observed_at) metadata.weather_observed_at = reading.observed_at;
    await outbox.save({ ...entry, record: { ...entry.record, metadata } });

    explainer.textContent = `Weather filled in: ${reading.summary}`;
    await renderOutbox();
  } catch (err) {
    explainer.textContent = `${err.message} You can write the weather in by editing the record.`;
  } finally {
    btn.removeAttribute("aria-busy");
    btn.disabled = false;
  }
}

// editQueued loads a waiting record back into the Add Result form.
//
// The queued copy stays where it is until the tester submits. Taking it out of
// the queue on the way into the form would mean a closed tab loses the record,
// which is the one thing this whole feature exists to prevent.
async function editQueued(id) {
  const entry = await outbox.get(id);
  if (!entry) return;

  editingEntryID = id;
  fillFormFromRecord(entry.record);
  document.getElementById("outbox-dialog").close();
  document.querySelector('[data-tab="add"]').click();
  document.getElementById("add-feedback").innerHTML =
    `<p class="feedback-ok">Correcting a record that is waiting to send. ` +
    `Submitting replaces it rather than filing a second copy.</p>`;
}

async function deleteQueued(id) {
  const entry = await outbox.get(id);
  if (!entry) return;
  // No undo, and the record exists nowhere else. Worth a question.
  const what = entry.record.procedure_ref || "this record";
  if (!confirm(`Delete ${what}? It has not been sent, and this is the only copy.`)) return;

  await outbox.remove(id);
  await refreshOutboxCount();
  await renderOutbox();
}

// describeAge adds the waiting time to a row, once it is long enough to be the
// point. A record written this morning does not need telling.
function describeAge(entry) {
  const days = ageInDays(entry);
  if (days < STALE_WARN_DAYS) return "";
  const emphasis = days >= STALE_URGENT_DAYS ? "outbox-entry-error" : "outbox-entry-held";
  return ` &middot; <span class="${emphasis}">waiting ${days} days</span>`;
}

function fillFormFromRecord(record) {
  const form = document.getElementById("add-form");
  const metadata = record.metadata || {};

  for (const field of ["repo", "branch", "rcs_ref", "procedure_ref", "evidence_type", "source"]) {
    if (form[field]) form[field].value = record[field] || "";
  }
  const result = form.querySelector(`[name="result"][value="${record.result}"]`);
  if (result) result.checked = true;
  if (record.finished_at) {
    form.finished_at.value = formatTime(record.finished_at);
  }
  form.tags.value = (metadata.tags || []).join(", ");
  form.notes.value = metadata.notes || "";
  form.observations.value = metadata.observations || "";
  form.location.value = metadata.location || "";
  form.weather_conditions.value = metadata.weather_conditions || "";
  updateTestLogPreview();
}

// runSync drains the queue and says what happened.
//
// `announce` is on when a person pressed the button and is waiting for an
// answer. An automatic sync stays quiet unless something needs a person: the
// count in the header falling is the report, and a dialog that appears because
// a train came out of a tunnel is not.
async function runSync({ announce = false } = {}) {
  if (!outbox || syncing) return null;
  if (await outbox.count() === 0) return null;

  syncing = true;
  const button = document.getElementById("outbox-send");
  button.setAttribute("aria-busy", "true");
  try {
    const summary = await syncOutbox({
      outbox,
      subject: currentSubject,
      onProgress: showProgress,
      // A record that named a place but no sky can still gain the reading,
      // right up until it is filed and becomes something nobody can add to.
      lookUpWeather: async (location, finishedAt) => {
        const point = parseCoordinates(location);
        if (!point) return null;
        return fetchWeather(point, finishedAt ? new Date(finishedAt) : null, apiFetchNoRedirect);
      },
      // Uploading is a straight replay of bytes that are already named: the
      // store answers with the reference the browser worked out when the photo
      // was attached, and storing the same bytes twice is one object.
      putBlob: async blob => {
        const resp = await apiFetchNoRedirect(`${API_BASE}/blobs`, {
          method: "POST",
          headers: { "Content-Type": blob.contentType || "application/octet-stream" },
          body: blob.bytes,
        });
        return { ok: resp.ok, status: resp.status };
      },
      post: async (path, payload) => {
        const resp = await apiFetchNoRedirect(`${API_BASE}${path}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        const data = await resp.json().catch(() => null);
        return { ok: resp.ok, status: resp.status, data };
      },
    });

    // Photos whose records have now been filed are owed to nobody.
    await outbox.sweepBlobs();
    await refreshOutboxCount();
    clearProgress();
    const explainer = document.getElementById("outbox-explainer");
    if (announce || summary.authRequired || summary.blocked) {
      explainer.textContent = describeSync(summary);
    }
    return summary;
  } finally {
    syncing = false;
    clearProgress();
    button.removeAttribute("aria-busy");
  }
}

// showProgress reports a sync as it runs.
//
// A week of manual results is a few kilobytes of JSON behind a few hundred
// megabytes of photographs, over whatever link a hotel lobby provides. A tester
// who cannot see it moving has no way to tell an upload in progress from one
// that has quietly wedged — which is exactly when they close the laptop.
function showProgress(progress) {
  const el = document.getElementById("outbox-progress");
  if (!el) return;
  el.hidden = false;
  el.querySelector("progress").value = progressFraction(progress);
  el.querySelector(".outbox-progress-label").textContent = describeProgress(progress);
}

function clearProgress() {
  const el = document.getElementById("outbox-progress");
  if (el) el.hidden = true;
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

// --- Test log preview ---

// Markdown is only worth offering if the tester can see what it will look like
// before the record is filed; afterwards the log is immutable.
function updateTestLogPreview() {
  const preview = document.getElementById("test-log-preview");
  if (preview.hidden) return;
  const raw = document.querySelector('#add-form [name="observations"]').value;
  preview.innerHTML = raw.trim()
    ? renderMarkdown(raw)
    : `<p class="test-log-empty">Nothing written yet.</p>`;
  hydrateImages(preview);
}

document.getElementById("test-log-preview-toggle").addEventListener("click", (e) => {
  const preview = document.getElementById("test-log-preview");
  preview.hidden = !preview.hidden;
  e.target.setAttribute("aria-expanded", String(!preview.hidden));
  e.target.textContent = preview.hidden ? "Preview" : "Hide preview";
  updateTestLogPreview();
});

document.querySelector('#add-form [name="observations"]')
  .addEventListener("input", updateTestLogPreview);

// Pasting a screenshot is how a tester attaches one — the alternative is
// finding a file, naming it, and uploading it somewhere else first, which is
// how photos end up not being attached at all.
attachImageUploads(
  document.querySelector('#add-form [name="observations"]'),
  msg => {
    document.getElementById("add-feedback").innerHTML =
      `<p class="feedback-error">${esc(msg)}</p>`;
  },
);

document.getElementById("fill-now").addEventListener("click", () => {
  const input = document.querySelector('#add-form [name="finished_at"]');
  // Undoably: a button that fills a field in is one a tester may want to take
  // back, and taking it back is what Cmd-Z is for (issue #83).
  setValue(input, formatTime(new Date().toISOString()));
  updateUtcPreview(input);
});

// --- Location ---

// The button fills the field rather than replacing it: what it writes is text
// the tester can correct, add a bay number to, or throw away. The field works
// with the button never pressed, which is also what happens on a desktop with
// no receiver in it.
document.getElementById("fill-location").addEventListener("click", async (e) => {
  const input = document.querySelector('#add-form [name="location"]');
  const status = document.getElementById("location-status");
  const btn = e.currentTarget;

  status.classList.remove("location-status-error");
  status.textContent = "Locating…";
  btn.setAttribute("aria-busy", "true");
  btn.disabled = true;

  try {
    const { lat, lon, accuracy } = await requestPosition();
    setValue(input, formatCoordinates(lat, lon));
    input.dataset.fromDevice = input.value;
    input.dataset.accuracyM = accuracy;
    status.textContent = formatAccuracy(accuracy)
      ? `This device's position, ${formatAccuracy(accuracy)}`
      : "This device's position";
  } catch (err) {
    status.textContent = err.message;
    status.classList.add("location-status-error");
  } finally {
    btn.removeAttribute("aria-busy");
    btn.disabled = false;
  }
});

// An edited field is the tester's own account of the place again, so the
// device's margin stops applying to it and the note about where it came from
// stops being true.
document.querySelector('#add-form [name="location"]').addEventListener("input", (e) => {
  if (e.target.dataset.fromDevice === e.target.value) return;
  delete e.target.dataset.fromDevice;
  delete e.target.dataset.accuracyM;
  const status = document.getElementById("location-status");
  status.textContent = "";
  status.classList.remove("location-status-error");
});

// --- Weather ---

// The lookup is asked for the place and hour the record already names, so it
// wants a location and a finish time filled in first — but neither is required,
// and neither is worth refusing over: an empty location falls back to the
// device, and an empty finish time means the run is happening now, which is
// what the record itself would say.
document.getElementById("fill-weather").addEventListener("click", async (e) => {
  const input = document.querySelector('#add-form [name="weather_conditions"]');
  const status = document.getElementById("weather-status");
  const btn = e.currentTarget;

  status.classList.remove("location-status-error");

  if (connectionState() === OFFLINE) {
    // The lookup is a server call by design (DESIGN.md §4.5), so there is
    // nothing to wait for. Saying so beats a spinner that ends in a timeout.
    status.textContent = "No connection, so there is nobody to ask. Write down what you can see instead.";
    status.classList.add("location-status-error");
    document.getElementById("weather-compose").hidden = false;
    updateComposePreview();
    document.getElementById("wc-description").focus();
    return;
  }

  status.textContent = "Looking up the weather…";
  btn.setAttribute("aria-busy", "true");
  btn.disabled = true;

  try {
    const location = document.querySelector('#add-form [name="location"]').value.trim();
    const point = await weatherPoint(location, requestPosition);

    // The record's own finish time, not now: a tester filing yesterday
    // evening's run wants yesterday evening's weather, and today's would be a
    // plausible-looking untruth. An unparseable box is left to the field's own
    // preview to complain about; the lookup just asks about now.
    const rawFinished = document.querySelector('#add-form [name="finished_at"]').value.trim();
    const when = rawFinished ? parseUserDateTime(rawFinished) : null;

    const reading = await fetchWeather(point, when);
    setValue(input, reading.summary);
    input.dataset.fromService = reading.summary;
    if (reading.observed_at) input.dataset.observedAt = reading.observed_at;
    status.textContent = describeReading(reading, point.source);
  } catch (err) {
    status.textContent = err.message;
    status.classList.add("location-status-error");
  } finally {
    btn.removeAttribute("aria-busy");
    btn.disabled = false;
  }
});

// --- Writing the weather down ---

// The lookup needs a server; the sky does not. Offline the button says so and
// the tester writes what they can see, which on a proving ground is better
// information anyway: they are standing in it.
const composeFields = {
  description: "wc-description",
  temperatureC: "wc-temperature",
  windKph: "wc-wind",
  humidity: "wc-humidity",
  precipitationMm: "wc-precipitation",
};

function readComposeFields() {
  const values = {};
  for (const [key, elementID] of Object.entries(composeFields)) {
    values[key] = document.getElementById(elementID).value;
  }
  return values;
}

function updateComposePreview() {
  const line = composeWeather(readComposeFields());
  document.getElementById("wc-preview").textContent = line
    ? `Will be filed as: ${line}`
    : "Fill in whatever you can see. Anything you leave empty is left out.";

  // Live, into the field itself. There is no Apply button because there is
  // nothing to apply: the boxes are a way of typing the line, and the line is
  // what is filed.
  const input = document.querySelector('#add-form [name="weather_conditions"]');
  // A plain assignment, unlike the buttons above, and deliberately. This runs on
  // every keystroke in the composer boxes, and an undoable write has to focus
  // the field it writes to — which would pull the caret out of the box being
  // typed in and back again, dozens of times, breaking IME along the way. The
  // composer's own boxes keep their undo; the line they compose is not
  // somewhere anybody is typing.
  input.value = line;
  // A written line is the tester's own account and never a reading, so the hour
  // an actual reading would have carried stays off it. That difference is what
  // lets a reader tell the two apart months later.
  delete input.dataset.fromService;
  delete input.dataset.observedAt;
}

document.getElementById("compose-weather").addEventListener("click", () => {
  const panel = document.getElementById("weather-compose");
  panel.hidden = !panel.hidden;
  if (panel.hidden) return;

  const status = document.getElementById("weather-status");
  status.classList.remove("location-status-error");
  status.textContent = "";
  updateComposePreview();
  document.getElementById("wc-description").focus();
});

for (const elementID of Object.values(composeFields)) {
  document.getElementById(elementID).addEventListener("input", updateComposePreview);
}

// Editing the line makes it the tester's account of the weather again — which
// is the point of leaving it editable, since the person who was standing there
// outranks a model that put the hailstorm two valleys away. The hour the
// service read stops applying to it at the same moment.
document.querySelector('#add-form [name="weather_conditions"]').addEventListener("input", (e) => {
  if (e.target.dataset.fromService === e.target.value) return;
  delete e.target.dataset.fromService;
  delete e.target.dataset.observedAt;
  const status = document.getElementById("weather-status");
  status.textContent = "";
  status.classList.remove("location-status-error");
});

// clearWeather empties the field and everything that was true about it.
function clearWeather() {
  const input = document.querySelector('#add-form [name="weather_conditions"]');
  input.value = "";
  delete input.dataset.fromService;
  delete input.dataset.observedAt;
  const status = document.getElementById("weather-status");
  status.textContent = "";
  status.classList.remove("location-status-error");

  const panel = document.getElementById("weather-compose");
  panel.hidden = true;
  for (const elementID of Object.values(composeFields)) {
    document.getElementById(elementID).value = "";
  }
}

function updateUtcPreview(input) {
  const preview = document.querySelector(`.utc-preview[data-preview-for="${input.name}"]`);
  if (!preview) return;
  const raw = input.value.trim();
  if (!raw) {
    preview.textContent = "";
    preview.classList.remove("utc-preview-error");
    return;
  }
  const d = parseUserDateTime(raw);
  if (!d) {
    preview.textContent = "unparseable — expected e.g. 2026-03-30 14:00";
    preview.classList.add("utc-preview-error");
    return;
  }
  preview.textContent = `= ${formatTime(d.toISOString())} UTC`;
  preview.classList.remove("utc-preview-error");
}

function wireUtcPreviews() {
  document.querySelectorAll("input[data-utc-preview]").forEach(input => {
    input.addEventListener("input", () => updateUtcPreview(input));
    updateUtcPreview(input);
  });
}

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

document.getElementById("auth-logout")?.addEventListener("click", async (e) => {
  e.preventDefault();
  await logout();
});

document.getElementById("close-login-choice")?.addEventListener("click", () => {
  document.getElementById("login-choice-dialog").close();
});

document.getElementById("auth-login")?.addEventListener("click", (e) => {
  e.preventDefault();
  // Where there is an identity provider, that is what "log in" means. The API
  // key path stays for CI, for scripts, and for anyone who reaches this page
  // holding a key rather than an account.
  if (ssoAvailable) {
    goToLogin();
    return;
  }
  promptForAPIKey("Enter your API key:");
});

// --- Init ---

// Asking the server who we are is what lets the page offer only what this
// caller can actually do. A store with nothing configured answers
// "not authenticated", which means open rather than locked out.
// ssoAvailable is what the "log in" button branches on.
let ssoAvailable = false;

// /auth/config answers whether there is anywhere to log in. It has to be a
// separate request from /me because /me refuses an anonymous caller — which is
// precisely the caller asking the question.
async function loadAuthConfig() {
  try {
    const resp = await fetch("/auth/config");
    if (resp.ok) return await resp.json();
  } catch { /* offline or mid-restart; fall through */ }
  return { sso_enabled: false };
}

// The Source box used to be a free-text field asking a tester to type their
// own name, which the server has refused to take on trust since the source
// binding landed: anyone without source:any may only file under their own
// subject. Now that the page knows who it is, it fills the box in and locks it
// rather than letting somebody type a name that will come back a 403.
//
// A caller holding source:any — a build robot, or an admin who also holds ci —
// is left alone: writing a source that is not its own name is exactly what
// that permission is for.
function pinSourceToCaller(me) {
  const input = document.querySelector('#add-form [name="source"]');
  if (!input || !me.authenticated) return;
  if ((me.permissions || []).includes("source:any")) return;

  input.value = me.subject;
  input.readOnly = true;
  input.title = `Filed as ${me.subject}, the account you are signed in as`;

  const label = input.closest("label");
  const hint = label?.querySelector("small");
  if (hint) hint.textContent = "(you)";
}

async function loadIdentity() {
  try {
    // Plain fetch, not apiFetch: a 401 here is the ordinary state of a page
    // nobody has logged into yet, and bouncing it straight to the identity
    // provider would make the store impossible to look at anonymously — or to
    // reach with an API key.
    const key = getStoredAPIKey();
    const resp = await fetch(`${API_BASE}/me`, {
      headers: key ? { Authorization: `Bearer ${key}` } : {},
    });
    if (resp.ok) return await resp.json();
  } catch { /* offline or mid-restart; fall through */ }
  return { authenticated: false, permissions: [] };
}

(async function init() {
  startConnectionIndicator();
  showServerVersion();
  // Not awaited: the page has nothing to wait for. The worker takes over on
  // the next load, and a tester who installs this today is covered tomorrow.
  registerServiceWorker();

  const [authConfig, me] = await Promise.all([loadAuthConfig(), loadIdentity()]);
  ssoAvailable = !!authConfig.sso_enabled;
  setAuthMode({
    sso: ssoAvailable,
    session: me.via_session,
    methods: authConfig.login_methods,
  });
  mountAccess(me);
  pinSourceToCaller(me);
  currentSubject = me.authenticated ? me.subject : null;

  // After the identity is known, so a queued record is attributed to whoever
  // is actually signed in, and so a sync does not send somebody else's records
  // under this name.
  await mountOutbox();
  runSync();
  refreshTemplateDropdown();
  refreshDatalists();
  document.querySelector('#add-form [name="finished_at"]').value = formatTime(new Date().toISOString());

  const { filters, detail } = readStateFromURL();
  applyURLState(filters);
  wireUtcPreviews();

  // Always search. The window is a view onto the whole result set, so an empty
  // filter set is a legitimate query — "everything" — not a prompt to fill the
  // form in. Only showing results once a filter is set used to leave any link
  // without one, including a shared deep link, rendering an empty table.
  await doSearch(filters);

  if (detail) {
    try {
      renderDetail(await fetchEvidenceById(detail));
    } catch { /* record may have been deleted; leave the window as it is */ }
  }
})();
