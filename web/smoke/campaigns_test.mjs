// Self-contained browser checks with deterministic API responses. Run separately
// from the live-server smoke suite: node --test web/smoke/campaigns_test.mjs
import test, { before, after } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { connect, launchChrome } from "./driver.mjs";

const digest = `sha256:${"a".repeat(64)}`;
function packageRow(id) {
  const entry = { procedure_ref: "patrol/display", path: "scenarios/display/scenario.yaml" };
  const definition = { title: "Display status", procedure_ref: "patrol/display", description: "procedure.md", defaults: { evidence_type: "manual_test" } };
  return { id, digest, imported_at: "2026-10-25T10:00:00Z", imported_by: "user:tester", archive_size: 123,
    definition: { manifest: { campaign: { id: `patrol-${id}`, name: `Patrol ${id}` }, source: { repository: "https://example.org/scenarios" }, scenarios: [entry] },
      scenarios: [{ definition, assets: { "scenarios/display/setup.png": digest }, procedure: { blocks: [
        { kind: "markdown", markdown: "# Setup\n![Setup](setup.png)\n<script>window.unsafeCampaign = true</script>" },
        { kind: "check", check: { id: "status", label: "Display visible", behavior: "expect" } },
        { kind: "field", field: { id: "operator", type: "text", label: "Operator", required: true } },
      ] } }] } };
}
let httpServer, chrome, page, base;
let role = "writer", listFails = false, uploads = 0, imageRequests = 0, downloads = 0, delayA = false;
const attempts=new Map();
const rows = Array.from({ length: 26 }, (_, i) => packageRow(`package-${i}`));
// A campaign larger than one procedure page exercises incremental filtering
// over the whole package, rather than only the visible list.
for (const row of rows) {
  const original = row.definition.scenarios[0];
  for (let i = 1; i <= 12; i++) {
    row.definition.scenarios.push({ ...original, definition: { ...original.definition, title: `Equipment check ${i}`, procedure_ref: `patrol/check-${i}` } });
    row.definition.manifest.scenarios.push({ procedure_ref: `patrol/check-${i}`, path: "scenarios/display/scenario.yaml" });
  }
}
const permissions = ["evidence:read", "blob:read", "analytics:read", "evidence:write", "blob:write"];
const json = (res, value, status = 200) => { res.writeHead(status, { "Content-Type": "application/json" }); res.end(JSON.stringify(value)); };

async function handle(req, res) {
  const url = new URL(req.url, base || "http://localhost");
  if (url.pathname === "/healthz") return json(res, { ok: true });
  if (url.pathname === "/version") return json(res, { version: "dev" });
  if (url.pathname === "/auth/config") return json(res, { sso_enabled: false });
  if (url.pathname === "/api/v1/me") return json(res, { authenticated: true, subject: "user:tester", permissions: role === "writer" ? permissions : permissions.slice(0, 3) });
  if (url.pathname === "/api/v1/weather") return json(res,{summary:"Rain, 8 °C",observed_at:"2026-10-24T09:00:00Z"});
  if(url.pathname.startsWith("/api/v1/campaign-attempts")) {
    if(req.method==="POST" || req.method==="PUT") {
      const chunks=[];for await(const chunk of req) chunks.push(chunk);
      const body=JSON.parse(Buffer.concat(chunks));
      if(url.pathname.endsWith('/finalize')) {
        const a=attempts.get(url.pathname.split('/').at(-2));
        if(a.revision!==body.revision) return json(res,{error:"draft changed"},409);
        a.finalized_at=new Date().toISOString();a.finished_at=body.finished_at;a.evidence_id="example-evidence";a.revision++;
        return json(res,a,201);
      }
      if(req.method==="POST") {
        const a={...body,owner:"user:tester",revision:1,created_at:"2026-10-24T09:15:23.123456Z",updated_at:new Date().toISOString(),data:{repo:"",branch:"",rcs_ref:"",notes:"",result:"",answers:{}}};
        attempts.set(a.id,a);return json(res,a,201);
      }
      const a=attempts.get(url.pathname.split("/").pop());
      if(a.revision!==body.revision) return json(res,{error:"draft changed"},409);
      a.data=body.data;a.revision++;return json(res,a);
    }
    if(url.pathname==="/api/v1/campaign-attempts") return json(res,{attempts:[...attempts.values()].filter(a=>a.campaign_id===url.searchParams.get("campaign_id") && a.procedure_ref===url.searchParams.get("procedure_ref")),has_more:false});
    return json(res,attempts.get(url.pathname.split("/").pop()));
  }
  if (url.pathname === "/api/v1/campaigns/import") {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    if (Buffer.concat(chunks).toString() === "bad") return json(res, { error: "invalid manifest" }, 422);
    uploads++;
    return json(res, rows[0], uploads === 1 ? 201 : 200);
  }
  if (url.pathname === "/api/v1/campaigns") {
    if (listFails) return json(res, { error: "temporarily unavailable" }, 503);
    const offset = Number(url.searchParams.get("offset"));
    return json(res, { campaigns: rows.slice(offset, offset + 25).map(row => ({ ...row, manifest: row.definition.manifest, scenario_count: 1 })), has_more: offset === 0 });
  }
  if (url.pathname.startsWith("/api/v1/campaigns/")) {
    if (url.pathname.endsWith("/package")) { downloads++; res.writeHead(200, { "Content-Type": "application/gzip" }); return res.end("archive bytes"); }
    const id = url.pathname.split("/").pop();
    const row = rows.find(r => r.id === id);
    if (delayA && id === rows[0].id) await new Promise(resolve => setTimeout(resolve, 250));
    return json(res, row);
  }
  if (url.pathname.endsWith("/url")) { imageRequests++; return json(res, { url: `${base}/test-image.png` }); }
  if (url.pathname === "/test-image.png") {
    res.writeHead(200, { "Content-Type": "image/png" });
    return res.end(Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRz8AAAAASUVORK5CYII=", "base64"));
  }
  if (url.pathname === "/api/v1/evidence/distinct") return json(res, { values: [] });
  if (url.pathname.startsWith("/api/")) return json(res, { records: [], evidence: [], total: 0 });
  const name = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
  if (name.includes("/") || name.includes("..")) { res.writeHead(404); return res.end(); }
  try {
    const data = await readFile(new URL(`../static/${name}`, import.meta.url));
    const type = name.endsWith(".js") ? "text/javascript" : name.endsWith(".css") ? "text/css" : name.endsWith(".html") ? "text/html" : "application/octet-stream";
    res.writeHead(200, { "Content-Type": type }); res.end(data);
  } catch { res.writeHead(404); res.end(); }
}

before(async () => {
  httpServer = createServer((req, res) => handle(req, res).catch(error => json(res, { error: error.message }, 500)));
  await new Promise(resolve => httpServer.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${httpServer.address().port}`;
  chrome = await launchChrome();
  page = await connect(base);
  await page.goto(`${base}/#campaigns`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 25`);
});
after(async () => { page?.close(); chrome?.close(); if (httpServer) await new Promise(resolve => httpServer.close(resolve)); });

async function upload(content) {
  await page.eval(`(() => {
    const transfer = new DataTransfer();
    transfer.items.add(new File([${JSON.stringify(content)}], 'campaign.tar.gz', {type:'application/gzip'}));
    document.getElementById('campaign-file').files = transfer.files;
    document.getElementById('campaign-import').click();
  })()`);
}

test("campaign browser paginates, previews controls/images, filters, and ignores late selections", async () => {
  assert.equal(await page.eval(`document.getElementById('tab-campaigns').hidden`), false);
  await page.eval(`document.getElementById('campaign-next').click()`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 1`);
  await page.eval(`document.getElementById('campaign-prev').click()`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 25`);
  delayA = true;
  await page.eval(`document.querySelectorAll('.campaign-package')[0].click(); document.querySelectorAll('.campaign-package')[1].click()`);
  await page.waitFor(`document.querySelector('#campaign-package-info h3')?.textContent === 'Patrol package-1'`);
  await new Promise(resolve => setTimeout(resolve, 350));
  assert.equal(await page.eval(`document.querySelector('#campaign-package-info h3').textContent`), "Patrol package-1");
  assert.equal(await page.eval(`document.getElementById('campaign-procedure-context').textContent.includes('Patrol package-1')`), true);
  assert.equal(await page.eval(`document.querySelector('.campaign-overview #campaign-procedure-search') === null`), true);
  assert.equal(await page.eval(`document.querySelector('.campaign-procedure-detail #campaign-procedure-search') !== null`), true);
  assert.equal(await page.eval(`document.querySelector('.campaign-provenance').open`), false);
  assert.equal(await page.eval(`document.querySelectorAll('.campaign-procedure-item').length`), 10);
  assert.deepEqual(await page.eval(`[...document.querySelectorAll('#campaign-procedure-table th')].map(th => th.textContent)`), ["Title", "Procedure reference"]);
  assert.equal(await page.eval(`document.querySelectorAll('#campaign-procedure-list tr:first-child td').length`), 2);
  await assertTableGeometry(false);
  assert.equal(await page.eval(`document.getElementById('campaign-procedure-inspection').hidden`), true);
  await page.eval(`document.getElementById('campaign-procedure-next').click()`);
  assert.equal(await page.eval(`document.querySelectorAll('.campaign-procedure-item').length`), 3);
  await page.eval(`document.getElementById('campaign-procedure-search').value='DISPLAY'; document.getElementById('campaign-procedure-search').dispatchEvent(new Event('input'))`);
  assert.equal(await page.eval(`document.querySelectorAll('.campaign-procedure-item').length`), 1);
  assert.equal(await page.eval(`document.getElementById('campaign-procedure-prev').disabled`), true);
  await page.eval(`document.querySelector('#campaign-procedure-list .campaign-procedure-reference').click()`);
  assert.equal(await page.eval(`document.activeElement.id`), "campaign-inspection-title");
  assert.equal(await page.eval(`document.querySelectorAll('#campaign-procedure-preview input:disabled, #campaign-procedure-preview select:disabled').length`), 2);
  assert.equal(await page.eval(`!!window.unsafeCampaign`), false);
  await page.waitFor(`document.querySelector('#campaign-procedure-preview img')?.getAttribute('src')`);
  assert.ok(imageRequests > 0);
  await page.eval(`document.getElementById('campaign-procedure-search').value='missing'; document.getElementById('campaign-procedure-search').dispatchEvent(new Event('input'))`);
  assert.equal(await page.eval(`document.querySelectorAll('.campaign-procedure-item').length`), 0);
  assert.equal(await page.eval(`document.getElementById('campaign-procedure-inspection').hidden`), true);
  await page.eval(`document.getElementById('campaign-procedure-search').value='DISPLAY'; document.getElementById('campaign-procedure-search').dispatchEvent(new Event('input'))`);
  assert.equal(await page.eval(`document.querySelectorAll('.campaign-procedure-item').length`), 1);
  await page.eval(`document.querySelector('.campaign-procedure-item').click(); document.getElementById('campaign-procedure-close').click()`);
  assert.equal(await page.eval(`document.activeElement.classList.contains('campaign-procedure-item')`), true);
  await page.send("Browser.setDownloadBehavior", { behavior: "deny" });
  await page.eval(`document.getElementById('campaign-download').click()`);
  await page.waitFor(`!document.getElementById('campaign-download').disabled`);
  assert.equal(downloads, 1, "download must use the API rather than an unauthenticated link");
  assert.deepEqual(page.problems, []);
});

test("uploads report validation failures and duplicates, and list errors allow retry", async () => {
  delayA = false;
  await upload("bad");
  await page.waitFor(`document.getElementById('campaign-import-status').textContent.includes('invalid manifest')`);
  await upload("good");
  await page.waitFor(`document.getElementById('campaign-import-status').textContent.startsWith('Package imported')`);
  await page.waitFor(`!document.getElementById('campaign-import').disabled`);
  await upload("good");
  await page.waitFor(`document.getElementById('campaign-import-status').textContent.includes('already imported')`);
  await page.waitFor(`!document.getElementById('campaign-refresh').disabled`);
  listFails = true;
  await page.eval(`document.getElementById('campaign-refresh').click()`);
  await page.waitFor(`document.getElementById('campaign-list-status').textContent.includes('Could not load')`);
  listFails = false;
  await page.eval(`document.getElementById('campaign-refresh').click()`);
  await page.waitFor(`document.getElementById('campaign-list-status').textContent === ''`);
  assert.deepEqual(page.problems, []);
});

test("attempts save typed answers, survive reload, and keep unsaved edits on conflict", async () => {
  await page.eval(`document.querySelector('.campaign-procedure-item').click()`);
  await page.waitFor(`document.getElementById('campaign-attempt-list-status').textContent.includes('No saved drafts')`);
  await page.eval(`document.getElementById('campaign-attempt-start').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
  await page.eval(`document.getElementById('campaign-finished-created').click()`);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-at').value`), "2026-10-24T09:15:23.123456Z");
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.procedure_ref.readOnly`),true);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.source.readOnly`),true);
  await page.eval(`(() => {
    window.savedGeolocation = navigator.geolocation;
    Object.defineProperty(navigator, 'geolocation', {configurable:true, value:{getCurrentPosition(success) {window.pendingPosition=success;}}});
    document.getElementById('campaign-draft-form').elements.location.value='52.50000, 13.40000';
    document.getElementById('draft-locate').click();
    document.getElementById('draft-weather-lookup').click();
  })()`);
  assert.match(await page.eval(`document.getElementById('draft-location-status').textContent`), /Locating/);
  await page.waitFor(`document.getElementById('campaign-draft-form').elements.weather_conditions.value === 'Rain, 8 °C'`);
  await page.eval(`window.pendingPosition({coords:{latitude:52.5,longitude:13.4,accuracy:3}})`);
  await page.waitFor(`document.getElementById('draft-location-status').textContent.includes('Device position')`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.location.dataset.accuracyM`), '3', 'weather lookup must not invalidate pending GPS request');
  await page.eval(`(() => {
    Object.defineProperty(navigator, 'geolocation', {configurable:true, value:{getCurrentPosition(success, failure) {failure({code:1});}}});
    document.getElementById('draft-locate').click();
  })()`);
  await page.waitFor(`document.getElementById('draft-location-status').textContent.includes('permission was denied')`);
  assert.equal(await page.eval(`document.getElementById('draft-locate').disabled`), false);
  await page.eval(`(() => {
    document.getElementById('campaign-draft-form').elements.location.value='Test track';
    document.getElementById('draft-weather-lookup').click();
  })()`);
  await page.waitFor(`document.getElementById('draft-weather-status').textContent.includes('permission was denied')`);
  await page.eval(`(() => {
    Object.defineProperty(navigator, 'geolocation', {configurable:true, value:window.savedGeolocation});
    const input=document.getElementById('campaign-draft-form').elements.location;
    input.value='52.5, 13.4';input.dispatchEvent(new Event('input',{bubbles:true}));
  })()`);
  await page.eval(`(() => {const f=document.getElementById('campaign-draft-form');f.elements.tags.value='vehicle, regression';f.elements.observations.value='Test log narrative';document.getElementById('draft-custom-add').click();document.querySelector('.draft-custom-key').value='vehicle_id';document.querySelector('.draft-custom-value').value='vehicle-a';})()`);
  await page.eval(`(() => {const f=document.getElementById('campaign-draft-form');f.elements.repo.value='example/software';f.querySelector('[data-answer=status]').value='FAIL';f.querySelector('[data-answer=operator]').value='tester';f.dispatchEvent(new Event('input'));f.requestSubmit();})()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('revision 2')`);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-at').value`), "2026-10-24T09:15:23.123456Z", "saving must preserve the selected finish time");
  await page.eval(`document.getElementById('campaign-draft-close').click()`);
  await page.goto(`${base}/?resume=1#campaigns`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 25`);
  await page.eval(`document.querySelector('.campaign-package').click()`);
  await page.waitFor(`document.querySelector('.campaign-procedure-item')`);
  await page.eval(`document.querySelector('.campaign-procedure-item').click()`);
  await page.waitFor(`document.querySelector('[data-attempt-id]')`);
  await page.eval(`document.querySelector('[data-attempt-id]').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
  assert.equal(await page.eval(`document.querySelector('#campaign-draft-form [data-answer=status]').value`),"FAIL");
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.repo.value`),"example/software");
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.location.value`),"52.5, 13.4");
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.weather_conditions.value`),"Rain, 8 °C");
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.observations.value`),"Test log narrative");
  assert.equal(await page.eval(`document.querySelector('.draft-custom-value').value`),"vehicle-a");
  [...attempts.values()][0].revision++;
  await page.eval(`const f=document.getElementById('campaign-draft-form');f.elements.notes.value='keep my edit';f.dispatchEvent(new Event('input'));f.requestSubmit()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Not saved')`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.notes.value`),"keep my edit");
  await page.eval(`window.confirm=()=>true;document.getElementById('campaign-draft-reload').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('revision 3')`);
  await page.eval(`document.getElementById('campaign-draft-finalize').click()`);
  assert.match(await page.eval(`document.getElementById('campaign-finalize-status').textContent`), /actual repository, branch and deployed revision/);
  assert.equal(await page.eval(`document.getElementById('draft-software-context').open`),true);
  await page.eval(`(() => {const f=document.getElementById('campaign-draft-form');f.elements.branch.value='main';f.elements.rcs_ref.value='deployed-build';f.dispatchEvent(new Event('input'));document.getElementById('campaign-draft-finalize').click();})()`);
  assert.match(await page.eval(`document.getElementById('campaign-finalize-status').textContent`), /Save draft first/);
  await page.eval(`document.getElementById('campaign-draft-form').requestSubmit()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('revision 4')`);
  await page.eval(`document.getElementById('campaign-draft-finalize').click()`);
  assert.match(await page.eval(`document.getElementById('campaign-finalize-status').textContent`), /overall Result/);
  await page.eval(`document.querySelector('#campaign-draft-form [name=result][value=FAIL]').click();document.getElementById('campaign-draft-form').requestSubmit()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('revision 5')`);
  await page.eval(`document.getElementById('campaign-finished-now').click()`);
  assert.ok(Math.abs(Date.now() - Date.parse(await page.eval(`document.getElementById('campaign-finished-at').value`))) < 5000);
  await page.eval(`document.getElementById('campaign-finished-picker').value='2026-10-25T12:30';document.getElementById('campaign-finished-picker').dispatchEvent(new Event('input'));`);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-at').value`), "2026-10-25T12:30:00.000Z");
  await page.eval(`document.getElementById('campaign-draft-finalize').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Finalized as immutable')`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-fields').disabled`),true);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-save').disabled`),true);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-finalize').disabled`),true);
  assert.equal(await page.eval(`['campaign-finished-now','campaign-finished-created','campaign-finished-picker'].every(id=>document.getElementById(id).disabled)`),true);
  assert.equal(await page.eval(`document.getElementById('campaign-final-evidence').hidden`),false);
  assert.match(await page.eval(`document.getElementById('campaign-final-evidence').href`),/detail=example-evidence/);
  await page.eval(`document.getElementById('campaign-draft-close').click()`);
  assert.deepEqual(page.problems,[]);
});

test("phone navigation opens campaigns; viewer can browse but cannot import", async () => {
  role = "viewer";
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  await page.goto(`${base}/?viewer=1#campaigns`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 25`);
  assert.equal(await page.eval(`document.getElementById('campaign-import-form').hidden`), true);
  await page.eval(`document.querySelector('[data-phone=more]').click(); document.querySelector('#more-sheet [data-tab=campaigns]').click()`);
  assert.equal(await page.eval(`document.getElementById('tab-campaigns').hidden`), false);
  assert.equal(await page.eval(`document.querySelector('[data-phone=more]').classList.contains('active')`), true);
  await page.eval(`document.querySelector('.campaign-package').click()`);
  await page.waitFor(`!document.getElementById('campaign-detail').hidden`);
  await assertTableGeometry(true);
  await page.eval(`document.querySelector('.campaign-procedure-item').click()`);
  assert.equal(await page.eval(`document.getElementById('campaign-attempt-tools').hidden`),true);
  assert.equal(await page.eval(`document.documentElement.scrollWidth <= window.innerWidth`), true);
  assert.deepEqual(page.problems, []);
});

async function assertTableGeometry(phone) {
  const geometry = await page.eval(`(() => {
    const table = document.getElementById('campaign-procedure-table');
    const textRect = el => { const range = document.createRange(); range.selectNodeContents(el); const r = range.getBoundingClientRect(); return { x: r.x, y: r.y, height: r.height }; };
    const headers = [...table.querySelectorAll('th')].map(textRect);
    const rows = [...table.querySelectorAll('tbody tr[data-procedure-ref]')].map(row => ({
      height: row.getBoundingClientRect().height,
      cells: [...row.cells].map(cell => textRect(cell.querySelector('button') || cell)),
      padding: getComputedStyle(row.querySelector('button')).padding,
    }));
    return { headers, rows };
  })()`);
  for (const row of geometry.rows) {
    assert.equal(row.padding, '0px', 'cell spacing must not be doubled by button padding');
    row.cells.forEach((cell, i) => assert.ok(Math.abs(cell.x - geometry.headers[i].x) < 1,
      `column ${i + 1} text must align with its header`));
    assert.ok(Math.abs((row.cells[0].y + row.cells[0].height / 2) - (row.cells[1].y + row.cells[1].height / 2)) < 2,
      'title and reference text must be vertically aligned');
    assert.ok(phone ? row.height >= 44 : row.height < 34, 'row height should be touch-sized on phones and compact on desktop');
  }
}
