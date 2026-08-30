// What the three tabs do in a real browser, run with:
//
//   node --test web/smoke/
//
// This is not a unit test and does not run in CI. It needs a server on
// SMOKE_BASE_URL (default http://localhost:8000) and a Chrome, and it files
// records against that server — see web/smoke/README.md.
//
// It exists because of what happened during #124. Splitting app.js into nine
// modules produced four real breaks, and `node --check`, `go test ./...` and
// `bazel test //...` passed every one of them. A missing import, a stray brace,
// a function dragged into the wrong file: all of them are invisible until the
// page runs. These checks are what caught them, so they are in the repo now
// rather than in somebody's scratch directory.

import test, { after, before, beforeEach } from "node:test";
import assert from "node:assert/strict";

import { BASE, connect, launchChrome, serverIsUp } from "./driver.mjs";

// One repo name for everything filed here, so what a run leaves behind is easy
// to find and easy to delete. There is no DELETE endpoint; the README says how.
const REPO = "smoke/browser-check";

let chrome;
let page;

before(async () => {
  assert.ok(await serverIsUp(BASE),
    `nothing answering at ${BASE}. Start one with: docker compose up -d --build app`);
  chrome = await launchChrome();
  page = await connect(BASE);
  await page.goto(BASE);
  await page.reset();
});

// Every check starts on a page nobody has touched.
//
// These share one browser, and sharing state as well made the failures move
// about: a check that broke the one before it turned into three red tests and
// no obvious cause. A reload costs about a second and buys a failure that means
// what it says.
beforeEach(async () => {
  await page.offline(false);
  await page.goto(BASE);
  await page.waitFor(`document.querySelectorAll('.nav-tab').length === 4`,
    { what: "the page to finish starting up" });
  await page.waitFor(`document.querySelectorAll('#results-body tr[data-id]').length > 0`,
    { what: "the first search to come back" });
  page.problems.length = 0;
});

after(async () => {
  page?.close();
  chrome?.close();
});

// Nothing may have gone wrong quietly. Three of the four breaks in #124 showed
// up only here — the page half-wired, no test failing, an exception in the
// console nobody was reading.
function noProblems(t) {
  assert.deepEqual(page.problems, [],
    `the page reported errors during "${t.name}"`);
}

// --- The page comes up at all ---

test("the page starts up with every tab wired", async t => {
  const state = await page.eval(`({
    tabs: [...document.querySelectorAll('.nav-tab')].map(a => a.dataset.tab),
    version: document.getElementById('server-version').textContent,
    rows: document.querySelectorAll('#results-body tr[data-id]').length,
  })`);

  assert.deepEqual(state.tabs, ["search", "analytics", "add", "access"]);
  assert.match(state.version, /^\d{4}\.\d{2}\.\d{2}\.\d{2}\.\d{2}$|^dev$/,
    "the footer should name the build");
  assert.ok(state.rows > 0, "the first search runs on load");
  noProblems(t);
});

// --- Search ---

test("a filter narrows the results and lands in the URL", async t => {
  // Filtered on a repo taken off the screen rather than a name written in here.
  // A smoke test that knows what is in the database is a smoke test that breaks
  // when somebody reseeds it.
  const repo = await page.eval(
    `document.querySelector('#results-body tr[data-id] .col-repo, #results-body tr[data-id] td:nth-child(3)')
       ?.textContent.trim()`);
  assert.ok(repo, "there should be a record on screen to take a repo from");

  await page.eval(`(() => {
    const f = document.getElementById('filter-form');
    f.querySelector('[name="repo"]').value = ${JSON.stringify(repo)};
    f.querySelector('button[type="submit"]').click();
  })()`);
  await page.waitFor(`location.search.includes('repo=')`, { what: "the filter to reach the URL" });
  await page.waitFor(`document.querySelectorAll('#results-body tr[data-id]').length > 0`,
    { what: "the filtered results" });
  noProblems(t);
});

test("paging moves the window and says so in the URL", async t => {
  const first = await page.eval(`document.querySelector('#results-body tr[data-id]')?.dataset.id`);
  await page.eval(`document.getElementById('next-page').click()`);
  await page.waitFor(`/offset=/.test(location.search)`, { what: "the offset to reach the URL" });

  const second = await page.eval(`document.querySelector('#results-body tr[data-id]')?.dataset.id`);
  assert.notEqual(second, first, "a different window should be showing");
  noProblems(t);
});

test("a row opens the record, and the back button returns to the list", async t => {
  await page.eval(`document.querySelector('#results-body tr[data-id]').click()`);
  await page.waitFor(`document.getElementById('detail-dialog').open`, { what: "the record dialog" });
  assert.ok(await page.eval(`/detail=/.test(location.search)`),
    "an open record should be linkable");

  await page.eval(`document.getElementById('close-detail').click()`);
  await page.eval(`history.back()`);
  await page.waitFor(`document.querySelectorAll('#results-body tr[data-id]').length > 0`,
    { what: "the list to come back" });
  noProblems(t);
});

test("clearing the filters empties the box and searches again", async t => {
  // Set one first: clearing nothing proves nothing.
  const repo = await page.eval(
    `document.querySelector('#results-body tr[data-id] td:nth-child(3)')?.textContent.trim()`);
  await page.eval(`(() => {
    const f = document.getElementById('filter-form');
    f.querySelector('[name="repo"]').value = ${"REPO_PLACEHOLDER"};
    f.querySelector('button[type="submit"]').click();
  })()`.replace("REPO_PLACEHOLDER", JSON.stringify(repo)));
  await page.waitFor(`location.search.includes('repo=')`, { what: "the filter to apply" });

  await page.eval(`document.getElementById('clear-filters').click()`);
  await page.waitFor(`document.querySelector('#filter-form [name="repo"]').value === ''`,
    { what: "the filter box to clear" });
  await page.waitFor(`document.querySelectorAll('#results-body tr[data-id]').length > 0`,
    { what: "the unfiltered results to come back" });
  noProblems(t);
});

// --- Add Result ---

// Everything the form fills in on the tester's behalf, in one record: the
// current time and its UTC preview, a custom metadata row, markdown in the log,
// and a weather line written by hand rather than fetched.
test("the form files a record with everything it fills in", async t => {
  const filled = await page.eval(`(async () => {
    document.querySelector('[data-tab="add"]').click();

    document.getElementById('add-custom-field').click();
    await new Promise(r => setTimeout(r, 100));
    const row = document.querySelector('.custom-field-row');
    row.querySelector('.cf-key').value = 'rig';
    row.querySelector('.cf-value').value = 'bench-2';

    document.getElementById('test-log-preview-toggle').click();
    const log = document.querySelector('#add-form [name="observations"]');
    log.value = '**wet surface**\\n- locked at 60';
    log.dispatchEvent(new Event('input', { bubbles: true }));

    document.getElementById('compose-weather').click();
    const set = (id, v) => {
      const el = document.getElementById(id);
      el.value = v;
      el.dispatchEvent(new Event('input', { bubbles: true }));
    };
    set('wc-description', 'Overcast'); set('wc-temperature', '8'); set('wc-wind', '20');
    await new Promise(r => setTimeout(r, 200));

    const weather = document.querySelector('#add-form [name="weather_conditions"]');
    return {
      finishedAt: document.querySelector('#add-form [name="finished_at"]').value,
      utcPreview: document.querySelector('.utc-preview[data-preview-for="finished_at"]').textContent,
      previewHTML: document.getElementById('test-log-preview').innerHTML,
      weather: weather.value,
      weatherClaimsToBeAReading: 'observedAt' in weather.dataset,
    };
  })()`);

  assert.match(filled.finishedAt, /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/, "the form opens on the current time");
  assert.match(filled.utcPreview, /UTC$/, "with the UTC line under it");
  assert.ok(filled.previewHTML.includes("<strong>"), "the log preview renders markdown");
  assert.equal(filled.weather, "Overcast, 8 °C, wind 20 km/h",
    "the composer writes the line Reading.Summary() would have");
  assert.equal(filled.weatherClaimsToBeAReading, false,
    "a line written by hand must not carry an observed hour");

  const procedure = `manual/smoke-${Date.now()}`;
  await page.eval(`(() => {
    const f = document.getElementById('add-form');
    f.repo.value = ${JSON.stringify(REPO)};
    f.branch.value = 'main';
    f.rcs_ref.value = 'smoke01';
    f.procedure_ref.value = ${JSON.stringify(procedure)};
    f.evidence_type.value = 'manual_test';
    f.source.value = 'smoke-test';
    f.querySelector('[name="result"][value="PASS"]').checked = true;
    f.querySelector('button[type="submit"]').click();
  })()`);

  await page.waitFor(
    `fetch('/api/v1/evidence?repo=${encodeURIComponent(REPO)}&limit=20')
       .then(r => r.json())
       .then(d => (d.records || []).some(x => x.procedure_ref === ${JSON.stringify(procedure)}))`,
    { what: "the record to reach the store" });

  const stored = await page.eval(`(async () => {
    const d = await (await fetch('/api/v1/evidence?repo=${encodeURIComponent(REPO)}&limit=20')).json();
    const r = (d.records || []).find(x => x.procedure_ref === ${JSON.stringify(procedure)});
    return { rig: r.metadata.rig, weather: r.metadata.weather_conditions,
             observations: r.metadata.observations, observedAt: r.metadata.weather_observed_at ?? null };
  })()`);

  assert.equal(stored.rig, "bench-2", "the custom metadata row is filed");
  assert.equal(stored.weather, "Overcast, 8 °C, wind 20 km/h");
  assert.equal(stored.observedAt, null, "and is still not passed off as a reading");
  assert.match(stored.observations, /wet surface/);
  noProblems(t);
});

// --- Templates ---

test("a template saves what the form holds and puts it back", async t => {
  await page.eval(`(() => {
    document.querySelector('[data-tab="add"]').click();
    const f = document.getElementById('add-form');
    f.repo.value = ${JSON.stringify(REPO)};
    f.branch.value = 'main';
    f.procedure_ref.value = 'manual/from-a-template';
    f.source.value = 'smoke-test';
  })()`);

  await page.eval(`(async () => {
    document.getElementById('template-save-current').click();
    await new Promise(r => setTimeout(r, 300));
    document.querySelector('#template-dialog-content input').value = 'Smoke bench';
    [...document.querySelectorAll('#template-dialog-content button')]
      .find(b => /save/i.test(b.textContent)).click();
  })()`);
  await page.waitFor(
    `[...document.getElementById('template-select').options].some(o => o.textContent === 'Smoke bench')`,
    { what: "the template to appear in the dropdown" });

  const reapplied = await page.eval(`(async () => {
    const f = document.getElementById('add-form');
    f.repo.value = ''; f.procedure_ref.value = '';
    const sel = document.getElementById('template-select');
    sel.value = [...sel.options].find(o => o.textContent === 'Smoke bench').value;
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    await new Promise(r => setTimeout(r, 200));
    return { repo: f.repo.value, procedure: f.procedure_ref.value };
  })()`);

  assert.equal(reapplied.repo, REPO, "applying a template refills the form");
  assert.equal(reapplied.procedure, "manual/from-a-template");
  noProblems(t);
});

// --- The outbox ---
//
// The feature the offline work exists for, and the one with the most moving
// parts: filing with no connection, correcting what is waiting, and sending it.

test("a record filed offline waits, can be corrected, and sends itself", async t => {
  // One test rather than three, because this is one story and each step only
  // means anything after the one before it.

  // Filed with nothing to send it to.
  await page.offline(true);
  await page.waitFor(`document.getElementById('health-status').textContent.includes('Offline')`,
    { timeout: 15000, what: "the header to notice there is no connection" });

  const feedback = await page.eval(`(async () => {
    document.querySelector('[data-tab="add"]').click();
    const f = document.getElementById('add-form');
    f.repo.value = ${JSON.stringify(REPO)};
    f.branch.value = 'main';
    f.rcs_ref.value = 'smoke02';
    f.procedure_ref.value = 'manual/smoke-offline';
    f.evidence_type.value = 'manual_test';
    f.source.value = 'smoke-test';
    f.observations.value = 'Filed with nothing to send it to.';
    f.querySelector('[name="result"][value="FAIL"]').checked = true;
    f.querySelector('button[type="submit"]').click();
    await new Promise(r => setTimeout(r, 600));
    return document.getElementById('add-feedback').textContent;
  })()`);
  assert.match(feedback, /Saved here/, "the tester is told where the record went");
  assert.ok(await page.eval(`!document.getElementById('outbox-status').hidden`),
    "and the header says something is waiting");

  // Corrected before it goes.
  await page.eval(`document.getElementById('outbox-status').click()`);
  await page.waitFor(`document.getElementById('outbox-dialog').open`, { what: "the outbox dialog" });
  await page.eval(`document.querySelector('.outbox-edit').click()`);
  await page.waitFor(`!document.getElementById('outbox-dialog').open`,
    { what: "the dialog to close on Edit" });

  const form = await page.eval(`({
    tab: document.querySelector('.nav-tab.active').dataset.tab,
    procedure: document.querySelector('#add-form [name="procedure_ref"]').value,
    feedback: document.getElementById('add-feedback').textContent,
  })`);
  assert.equal(form.tab, "add", "editing lands on the form");
  assert.equal(form.procedure, "manual/smoke-offline");
  assert.match(form.feedback, /Correcting a record/);

  // The point of correcting rather than refiling: one record, not two.
  const queued = await page.eval(`(async () => {
    document.querySelector('#add-form [name="rcs_ref"]').value = 'smoke02-corrected';
    document.querySelector('#add-form button[type="submit"]').click();
    await new Promise(r => setTimeout(r, 600));
    const { createOutbox, openStore } = await import('/outbox.js');
    const box = createOutbox(await openStore());
    return (await box.list()).map(e => e.record.rcs_ref);
  })()`);
  assert.deepEqual(queued, ["smoke02-corrected"], "the correction replaced the record it came from");

  // And sends itself once there is a connection.
  await page.offline(false);
  await page.waitFor(
    `(async () => {
       const { createOutbox, openStore } = await import('/outbox.js');
       return (await createOutbox(await openStore()).count()) === 0;
     })()`,
    { timeout: 20000, what: "the queue to drain" });

  const filed = await page.eval(
    `fetch('/api/v1/evidence?repo=${encodeURIComponent(REPO)}&limit=50')
       .then(r => r.json())
       .then(d => (d.records || []).some(x => x.rcs_ref === 'smoke02-corrected'))`);
  assert.ok(filed, "the corrected record reached the store");
  assert.ok(await page.eval(`document.getElementById('outbox-status').hidden`),
    "and the counter is gone");
  noProblems(t);
});
