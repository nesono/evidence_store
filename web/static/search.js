// The Search tab: what is being asked for, what came back, and the record a row
// opens.
//
// Filter state lives in the URL rather than in the module, so a search is a
// link somebody can send. The window into the result set — offset, size, sort,
// and the cached total — lives here, because nothing outside this tab has any
// use for it.
//
// Moved out of app.js (issue #124). The wiring that used to run on import now
// runs in mountSearch.

import { API_BASE, apiFetch, esc, formatTime, resultBadge } from "./common.js";
import { attachRangePicker } from "./datepicker.js";
import { parseUserDateTime } from "./datetime.js";
import { updateUtcPreview } from "./utcpreview.js";
import { renderMarkdown } from "./markdown.js";
import { hydrateImages, releaseImages } from "./images.js";
import { EVIDENCE_TYPES, evidenceTypeLabel } from "./evidencetype.js";
import { formatCoordinates, parseCoordinates } from "./location.js";
import { OFFLINE, connectionState } from "./offline.js";

// The DOM handles the wiring keeps hold of, assigned in mountSearch.
let finishedRange = null;
let rangePicker = null;
let resultDropdown = null;

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

// parseSearchState reads a query string into the state a search runs from.
//
// Pure, and separated from readStateFromURL below for that reason: everything
// awkward about an old link lives here — a `ref` folded out of `branch` or
// `rcs_ref`, an evidence type the dropdown no longer offers, a window size that
// is not one of the sizes — and none of it is a fact about `window`.
export function parseSearchState(search, { savedWindowSize = DEFAULT_WINDOW_SIZE } = {}) {
  const params = new URLSearchParams(search);
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
  const limit = parseInt(params.get("limit"), 10);

  // A `cursor=` from an older link is deliberately ignored. It is an opaque
  // position marker: it cannot say which window it refers to, so the view would
  // have no range to show and no way back. Those links open at the first window.
  return {
    filters,
    detail: params.get("detail"),
    windowOffset: Number.isFinite(offset) && offset > 0 ? offset : 0,
    windowSize: Number.isFinite(limit)
      ? normalizeWindowSize(limit)
      : normalizeWindowSize(savedWindowSize),
    sortColumn: params.get("sort") || "",
    sortDesc: params.get("order") === "desc",
  };
}

// searchStateToQuery writes that state back out — the other half of the round
// trip, and pure for the same reason.
export function searchStateToQuery(filters, {
  windowOffset = 0, windowSize, sortColumn = "", sortDesc = false, detail = null,
} = {}) {
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
  return `?${params}`;
}

export function readStateFromURL() {
  const state = parseSearchState(window.location.search, {
    savedWindowSize: loadPref(WINDOW_SIZE_KEY, DEFAULT_WINDOW_SIZE),
  });
  windowOffset = state.windowOffset;
  windowSize = state.windowSize;
  sortColumn = state.sortColumn;
  sortDesc = state.sortDesc;
  return { filters: state.filters, detail: state.detail };
}

function writeStateToURL(filters, detail) {
  history.pushState(null, "", searchStateToQuery(filters, {
    windowOffset, windowSize, sortColumn, sortDesc, detail,
  }));
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

export function activeAdvancedCount(filters) {
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

export async function fetchEvidenceById(id) {
  const resp = await apiFetch(`${API_BASE}/evidence/${id}`);
  if (!resp.ok) throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  return resp.json();
}

// evidence_type is not here any more: it is a closed set of three offered as a
// dropdown, so there is nothing to complete from what the store happens to hold.
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

export function renderDetail(record) {
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

export async function doSearch(filters, { clamped = false } = {}) {
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

async function openDetail(id) {
  try {
    const record = await fetchEvidenceById(id);
    renderDetail(record);
    writeStateToURL(readFormFilters(), id);
  } catch (err) {
    alert(`Failed to load record: ${err.message}`);
  }
}

// Reflects URL-derived state into the form and the window controls.
export function applyURLState(filters) {
  populateFormFromFilters(filters);
  refreshResultSummary();
  document.getElementById("window-size").value = String(windowSize);
  // A deep link carrying an advanced filter opens the panel, so the constraint
  // that is shaping the results is never invisible.
  setAdvancedExpanded(activeAdvancedCount(filters) > 0);
}

// mountSearch wires the tab up. Nothing here runs on import: the module can
// then be loaded by a test, and the page decides when the document is ready.
export function mountSearch() {
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

  finishedRange = document.getElementById("finished-range");

  rangePicker = attachRangePicker({
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

  resultDropdown = document.getElementById("result-dropdown");

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
}
