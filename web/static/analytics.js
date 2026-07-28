// The Analytics view: three sub-views over /api/v1/analytics/*.
//
// Marks are plain HTML/CSS rather than SVG or a charting library. The page has
// no build step, and the shapes needed here — a stacked part-to-whole bar and a
// row of meters — are a flex box and a width each. That keeps the page working
// offline with nothing to vendor, and sidesteps viewBox scaling entirely.
//
// Result colours are the app's status palette, and they never carry meaning on
// their own: every segment and every meter is accompanied by a written value or
// a legend entry.

import { API_BASE, apiFetch, esc, formatTime } from "./common.js";

const FILTER_FIELDS = ["repo", "branch", "finished_after", "finished_before"];

// Ranked answers to the questions the analytics page exists to ask. Each is
// just a sort key — the table is one view, not four.
const PRESETS = {
  flakiest: { label: "Flakiest", sort: "flip_rate", desc: true },
  never_fails: { label: "Never fails", sort: "pass_rate_lower", desc: true },
  always_fails: { label: "Always fails", sort: "fail_rate", desc: true },
  infra: { label: "Most infra errors", sort: "error_rate", desc: true },
};

const LABEL_TEXT = {
  stable: "Stable",
  always_failing: "Always failing",
  flaky: "Flaky",
  infra_heavy: "Infra-heavy",
  sparse: "Sparse",
};

const PAGE_SIZE = 50;

let loaded = false;
let view = "overview";
let sortKey = "fail_rate";
let sortDesc = true;
let offset = 0;
let total = 0;

// --- Helpers ---

function pct(v, digits = 1) {
  return `${(v * 100).toFixed(digits)}%`;
}

function num(n) {
  return n.toLocaleString("en-US");
}

function readFilters() {
  const form = document.getElementById("analytics-filter");
  const filters = {};
  for (const field of FILTER_FIELDS) {
    const value = form.elements[field]?.value.trim();
    if (value) filters[field] = value;
  }
  return filters;
}

function query(extra = {}) {
  const params = new URLSearchParams(readFilters());
  for (const [k, v] of Object.entries(extra)) {
    if (v !== undefined && v !== null && v !== "") params.set(k, v);
  }
  return params;
}

async function getJSON(path, params) {
  const resp = await apiFetch(`${API_BASE}${path}?${params}`);
  const body = await resp.json();
  if (!resp.ok) {
    throw new Error(body.error || `request failed (${resp.status})`);
  }
  return body;
}

function showError(message) {
  const box = document.getElementById("an-error");
  box.textContent = message;
  box.hidden = false;
}

function clearError() {
  document.getElementById("an-error").hidden = true;
}

function loading(containerId, message = "Loading...") {
  document.getElementById(containerId).innerHTML =
    `<p class="empty-state">${esc(message)}</p>`;
}

// --- Marks ---

const RESULT_ORDER = ["pass", "fail", "error", "skipped"];

// A part-to-whole bar. Segments are flex-grown by count, so the bar is exact at
// any width without measuring anything. Every segment is also written out in the
// legend below, so the colours are never the only carrier of meaning.
function stackedResultBar(counts) {
  const totalRuns = RESULT_ORDER.reduce((sum, k) => sum + (counts[k] || 0), 0);
  if (totalRuns === 0) return `<p class="empty-state">No records in this window</p>`;

  const segments = RESULT_ORDER
    .filter(k => counts[k] > 0)
    .map(k => {
      const share = counts[k] / totalRuns;
      return `<div class="an-seg an-seg-${k}" style="flex-grow:${counts[k]}"
                   title="${k.toUpperCase()}: ${num(counts[k])} (${pct(share)})">
                <span class="an-seg-label">${num(counts[k])}</span>
              </div>`;
    }).join("");

  const legend = RESULT_ORDER.map(k => `
    <li class="an-legend-item">
      <span class="an-swatch an-seg-${k}" aria-hidden="true"></span>
      <span class="an-legend-name">${k.toUpperCase()}</span>
      <span class="an-legend-value">${num(counts[k] || 0)}</span>
      <span class="an-legend-share">${pct((counts[k] || 0) / totalRuns)}</span>
    </li>`).join("");

  return `
    <div class="an-stack" role="img"
         aria-label="Result distribution: ${RESULT_ORDER.map(k => `${k} ${counts[k] || 0}`).join(", ")}">
      ${segments}
    </div>
    <ul class="an-legend">${legend}</ul>`;
}

// A rate as a length plus its written value. The number is always present, so
// the bar is a fast comparison aid rather than the only way to read the cell.
function meter(rate, kind) {
  const width = Math.max(0, Math.min(1, rate)) * 100;
  return `
    <div class="an-meter-cell">
      <div class="an-meter" title="${pct(rate, 1)}">
        <div class="an-meter-fill an-meter-${kind}" style="width:${width}%"></div>
      </div>
      <span class="an-num">${pct(rate, 1)}</span>
    </div>`;
}

// Only the column being sorted on is drawn as a meter. A bar in every rate cell
// is three little charts per row competing for attention; a bar in the column the
// reader chose to rank by is the comparison they actually asked for. The other
// cells keep their numbers, so nothing is lost.
function rateCell(rate, kind, emphasised) {
  if (!emphasised) return `<span class="an-num an-num-plain">${pct(rate, 1)}</span>`;
  const width = Math.max(0, Math.min(1, rate)) * 100;
  return `
    <div class="an-rate" title="${pct(rate, 1)}">
      <span class="an-num">${pct(rate, 1)}</span>
      <div class="an-meter">
        <div class="an-meter-fill an-meter-${kind}" style="width:${width}%"></div>
      </div>
    </div>`;
}

function statTile(label, value, hint) {
  return `
    <div class="an-tile">
      <div class="an-tile-value">${value}</div>
      <div class="an-tile-label">${esc(label)}</div>
      ${hint ? `<div class="an-tile-hint">${esc(hint)}</div>` : ""}
    </div>`;
}

function labelChips(labels) {
  if (!labels || labels.length === 0) return `<span class="an-muted">—</span>`;
  return labels
    .map(l => `<span class="an-chip an-chip-${l}">${esc(LABEL_TEXT[l] || l)}</span>`)
    .join(" ");
}

// --- Overview ---

async function loadOverview() {
  loading("an-overview");
  const data = await getJSON("/analytics/summary", query());

  const counts = {
    pass: data.pass, fail: data.fail, error: data.error, skipped: data.skipped,
  };
  const passRate = data.runs > 0 ? data.pass / data.runs : 0;

  const span = data.first_seen && data.last_seen
    ? `${formatTime(data.first_seen)} — ${formatTime(data.last_seen)} UTC`
    : "no records in this window";

  document.getElementById("an-overview").innerHTML = `
    <div class="an-hero">
      <div class="an-hero-value">${pct(passRate)}</div>
      <div class="an-hero-label">of ${num(data.runs)} runs passed</div>
      <div class="an-hero-span">${esc(span)}</div>
    </div>

    <div class="an-tiles">
      ${statTile("Tests", num(data.tests))}
      ${statTile("Commits", num(data.commits))}
      ${statTile("Repos", num(data.repos))}
      ${statTile("Fail rate", pct(data.fail_rate), "of PASS + FAIL")}
      ${statTile("Infra error rate", pct(data.error_rate), "of executed runs")}
    </div>

    <h5 class="an-section-title">Result distribution</h5>
    ${stackedResultBar(counts)}`;
}

// --- Tests ---

const COLUMNS = [
  { key: "procedure_ref", label: "Test", sortable: true, cls: "an-col-test" },
  { key: null, label: "Labels", cls: "an-col-labels" },
  { key: "runs", label: "Runs", sortable: true, cls: "an-col-num" },
  { key: "fail_rate", label: "Fail rate", sortable: true, cls: "an-col-rate" },
  { key: "error_rate", label: "Infra errors", sortable: true, cls: "an-col-rate" },
  { key: "flip_rate", label: "Flip rate", sortable: true, cls: "an-col-rate" },
  { key: "last_seen", label: "Last seen", sortable: true, cls: "an-col-time" },
];

function headerHTML() {
  return COLUMNS.map(c => {
    if (!c.sortable) return `<th class="${c.cls}">${esc(c.label)}</th>`;
    const active = c.key === sortKey;
    const arrow = active ? (sortDesc ? " ▾" : " ▴") : "";
    return `<th class="${c.cls} an-sortable${active ? " an-sorted" : ""}"
                data-sort="${c.key}">${esc(c.label)}${arrow}</th>`;
  }).join("");
}

function testRowHTML(t) {
  return `
    <tr data-repo="${esc(t.repo)}" data-procedure="${esc(t.procedure_ref)}"
        title="Show the matching records in Search">
      <td class="an-col-test">
        <span class="an-test-name">${esc(t.procedure_ref)}</span>
        <span class="an-test-repo">${esc(t.repo)}</span>
      </td>
      <td class="an-col-labels">${labelChips(t.labels)}</td>
      <td class="an-col-num">${num(t.runs)}</td>
      <td class="an-col-rate">${rateCell(t.fail_rate, "fail", sortKey === "fail_rate")}</td>
      <td class="an-col-rate">${rateCell(t.error_rate, "error", sortKey === "error_rate")}</td>
      <td class="an-col-rate">${rateCell(t.flip_rate, "flip", sortKey === "flip_rate")}</td>
      <td class="an-col-time">${formatTime(t.last_seen)}</td>
    </tr>`;
}

async function loadTests() {
  loading("an-tests-body-wrap");
  const data = await getJSON("/analytics/tests", query({
    sort: sortKey,
    order: sortDesc ? "desc" : "asc",
    limit: PAGE_SIZE,
    offset,
  }));

  total = data.total;

  const rows = data.tests.length
    ? data.tests.map(testRowHTML).join("")
    : `<tr><td colspan="${COLUMNS.length}" class="empty-state">No tests match these filters</td></tr>`;

  document.getElementById("an-tests-body-wrap").innerHTML = `
    <table class="an-table">
      <thead><tr>${headerHTML()}</tr></thead>
      <tbody>${rows}</tbody>
    </table>`;

  const shown = data.tests.length;
  document.getElementById("an-tests-range").textContent = total === 0
    ? "No tests"
    : `${num(offset + 1)}–${num(offset + shown)} of ${num(total)} tests`;

  document.getElementById("an-prev").disabled = offset === 0;
  document.getElementById("an-next").disabled = offset + shown >= total;

  document.querySelectorAll("#an-tests-body-wrap .an-sortable").forEach(th => {
    th.addEventListener("click", () => {
      const key = th.dataset.sort;
      // Re-clicking the active column flips it; a new column starts descending,
      // which is the interesting end for every rate here.
      if (key === sortKey) sortDesc = !sortDesc;
      else { sortKey = key; sortDesc = true; }
      offset = 0;
      refresh();
    });
  });

  // Drilling into the raw records is a link into the Search view, which already
  // restores its filters from the URL — so this needs no plumbing on that side.
  document.querySelectorAll("#an-tests-body-wrap tbody tr[data-procedure]").forEach(tr => {
    tr.addEventListener("click", () => {
      const params = new URLSearchParams({
        repo: tr.dataset.repo,
        procedure_ref: tr.dataset.procedure,
      });
      const filters = readFilters();
      if (filters.finished_after) params.set("finished_after", filters.finished_after);
      if (filters.finished_before) params.set("finished_before", filters.finished_before);
      window.location.search = params.toString();
    });
  });
}

// --- Clusters ---

function clusterCard(c) {
  const members = c.members
    .map(m => `<li title="${esc(m.repo)}">${esc(m.procedure_ref)}</li>`)
    .join("");
  return `
    <div class="an-cluster">
      <header class="an-cluster-head">
        <span class="an-cluster-title">Cluster ${c.id}</span>
        <span class="an-cluster-stat">${c.size} tests</span>
        <span class="an-cluster-stat">${num(c.covers_runs)} failing runs</span>
        <span class="an-cluster-stat" title="Mean pairwise similarity — a chain scores lower than a group that all fail together">
          cohesion ${c.cohesion.toFixed(2)}
        </span>
      </header>
      <ul class="an-cluster-members">${members}</ul>
    </div>`;
}

function coverRowHTML(step, index) {
  return `
    <tr>
      <td class="an-col-num">${index + 1}</td>
      <td class="an-col-test">
        <span class="an-test-name">${esc(step.test.procedure_ref)}</span>
        <span class="an-test-repo">${esc(step.test.repo)}</span>
      </td>
      <td class="an-col-num">+${num(step.new_runs)}</td>
      <td class="an-col-rate">${meter(step.coverage, "cover")}</td>
    </tr>`;
}

async function loadClusters() {
  loading("an-clusters");

  const controls = document.getElementById("analytics-cluster-controls");
  const data = await getJSON("/analytics/clusters", query({
    threshold: controls.elements.threshold.value,
    run_key: controls.elements.run_key.value,
    include_errors: controls.elements.include_errors.checked ? "true" : "",
  }));

  if (data.failing_runs === 0) {
    document.getElementById("an-clusters").innerHTML =
      `<p class="empty-state">Nothing failed in this window — no clusters to find.</p>`;
    return;
  }

  // The headline is a sentence, not a chart: "how few tests still catch the
  // failures" is one number and it deserves to be read directly.
  // The headline number is "how few tests would do", so lead with the smallest
  // set that still catches the overwhelming majority rather than the full cover.
  const enough = data.minimal_set.findIndex(s => s.coverage >= 0.9);
  const leadCount = enough >= 0 ? enough + 1 : data.minimal_set.length;
  const leadCoverage = enough >= 0 ? data.minimal_set[enough].coverage : 1;
  const headline =
    `of ${num(data.tests)} failing tests cover ${pct(leadCoverage, 0)} of ${num(data.failing_runs)} failing runs`;

  const clusters = data.clusters.length
    ? data.clusters.map(clusterCard).join("")
    : `<p class="empty-state">No test failed together with another often enough to cluster at this threshold.</p>`;

  document.getElementById("an-clusters").innerHTML = `
    <div class="an-hero">
      <div class="an-hero-value">${leadCount}</div>
      <div class="an-hero-label">${esc(headline)}</div>
      <div class="an-hero-span">grouped by ${esc(data.run_key)}${data.include_errors ? ", errors included" : ""}</div>
    </div>

    <h5 class="an-section-title">Minimal covering set</h5>
    <p class="an-note">
      Greedy selection, most-covering first. Each entry catches failing runs no
      entry above it caught. This is a good set, not a provably smallest one.
    </p>
    <table class="an-table">
      <thead>
        <tr>
          <th class="an-col-num">#</th>
          <th class="an-col-test">Test</th>
          <th class="an-col-num">New runs</th>
          <th class="an-col-rate">Coverage</th>
        </tr>
      </thead>
      <tbody>${data.minimal_set.map(coverRowHTML).join("")}</tbody>
    </table>

    <h5 class="an-section-title">Co-failure clusters</h5>
    <p class="an-note">
      Tests whose failures overlap by at least the similarity threshold. Members
      of a cluster tend to fail for the same underlying reason.
    </p>
    ${clusters}`;
}

// --- View switching ---

function setView(next) {
  view = next;
  document.querySelectorAll("#an-views button").forEach(b => {
    const active = b.dataset.view === view;
    b.classList.toggle("active", active);
    b.setAttribute("aria-selected", String(active));
  });
  document.querySelectorAll(".an-view").forEach(section => {
    section.hidden = section.dataset.view !== view;
  });
  refresh();
}

async function refresh() {
  clearError();
  try {
    if (view === "overview") await loadOverview();
    else if (view === "tests") await loadTests();
    else await loadClusters();
  } catch (err) {
    showError(err.message);
    document.getElementById(
      view === "overview" ? "an-overview" : view === "tests" ? "an-tests-body-wrap" : "an-clusters",
    ).innerHTML = "";
  }
}

// Called by the tab handler in app.js. The first activation loads; later ones
// leave whatever the user was looking at alone.
export function showAnalytics() {
  if (loaded) return;
  loaded = true;
  setView(view);
}

// --- Wiring ---

document.getElementById("analytics-filter")?.addEventListener("submit", e => {
  e.preventDefault();
  offset = 0;
  refresh();
});

document.getElementById("an-clear")?.addEventListener("click", () => {
  document.getElementById("analytics-filter").reset();
  offset = 0;
  refresh();
});

document.getElementById("an-views")?.addEventListener("click", e => {
  const btn = e.target.closest("button[data-view]");
  if (btn) setView(btn.dataset.view);
});

document.getElementById("an-presets")?.addEventListener("click", e => {
  const btn = e.target.closest("button[data-preset]");
  if (!btn) return;
  const preset = PRESETS[btn.dataset.preset];
  sortKey = preset.sort;
  sortDesc = preset.desc;
  offset = 0;
  document.querySelectorAll("#an-presets button").forEach(b => {
    b.classList.toggle("active", b === btn);
  });
  refresh();
});

document.getElementById("an-prev")?.addEventListener("click", () => {
  offset = Math.max(0, offset - PAGE_SIZE);
  refresh();
});

document.getElementById("an-next")?.addEventListener("click", () => {
  if (offset + PAGE_SIZE < total) {
    offset += PAGE_SIZE;
    refresh();
  }
});

document.getElementById("analytics-cluster-controls")?.addEventListener("change", () => {
  if (view === "clusters") refresh();
});
