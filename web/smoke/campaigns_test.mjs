// Self-contained browser checks with deterministic API responses. Run separately
// from the live-server smoke suite: node --test web/smoke/campaigns_test.mjs
import test, { before, after } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { connect, launchChrome } from "./driver.mjs";

const imageBytes = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRz8AAAAASUVORK5CYII=", "base64");
const digest = `sha256:${createHash("sha256").update(imageBytes).digest("hex")}`;
const archiveDigest = `sha256:${createHash("sha256").update("archive bytes").digest("hex")}`;
function packageRow(id) {
  const entry = { procedure_ref: "patrol/display", path: "scenarios/display/scenario.yaml" };
  const definition = { title: "Display status", procedure_ref: "patrol/display", description: "procedure.md", defaults: { evidence_type: "manual_test" } };
  return { id, digest: archiveDigest, imported_at: "2026-10-25T10:00:00Z", imported_by: "user:tester", archive_size: 123,
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
let draftWrites = 0, failFinalResponse = false;
let blobUploads = 0;
const executions = new Map();
let failDraftSave = false;
function executionView(e) {
  const pkg = rows.find(p => p.id === e.package_id);
  return { ...e, procedures: pkg.definition.scenarios.map(s => {
    const list = [...attempts.values()].filter(a => a.execution_id === e.id && a.procedure_ref === s.definition.procedure_ref).reverse();
    const draft = list.find(a => !a.finalized_at), latest = list.find(a => a.finalized_at);
    return { procedure_ref: s.definition.procedure_ref, title: s.definition.title, attempts: list.length,
      draft_id: draft?.id, latest_id: latest?.id, result: latest?.data.result || "" };
  }) };
}
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
  if (url.pathname.startsWith("/api/v1/campaign-executions")) {
    if (req.method === "POST" || req.method === "PUT") {
      const chunks=[];for await(const chunk of req) chunks.push(chunk);
      const body=JSON.parse(Buffer.concat(chunks));
      if (req.method === "POST") {
        const e={...body,revision:1}; executions.set(e.id,e); return json(res,executionView(e),201);
      }
      const e=executions.get(url.pathname.split("/").at(-2));
      if (e.revision !== body.revision) return json(res,{error:"campaign changed"},409);
      if (body.closed) {
        for (const p of executionView(e).procedures) {
          if (!p.draft_id && p.latest_id) continue;
          const a=p.draft_id ? attempts.get(p.draft_id) : {id:crypto.randomUUID(),execution_id:e.id,procedure_ref:p.procedure_ref,owner:"user:tester",revision:1,data:{repo:e.repo,branch:e.branch,rcs_ref:e.rcs_ref,notes:"",answers:{}}};
          a.data.result="SKIPPED";a.finalized_at=new Date().toISOString();a.evidence_id="skipped-evidence";
          attempts.set(a.id,a);
        }
      }
      e.closed_at=body.closed?new Date().toISOString():null;e.revision++;
      return json(res,executionView(e));
    }
    if (url.pathname === "/api/v1/campaign-executions") {
      const query=(url.searchParams.get("q")||"").toLowerCase(),state=url.searchParams.get("state");
      return json(res,{campaigns:[...executions.values()].filter(e=>e.name.toLowerCase().includes(query) && (state==="all" || (state==="completed")==!!e.closed_at)).map(executionView),has_more:false});
    }
    return json(res,executionView(executions.get(url.pathname.split("/").pop())));
  }
  if(url.pathname.startsWith("/api/v1/campaign-attempts")) {
    if(req.method==="POST" || req.method==="PUT") {
      const chunks=[];for await(const chunk of req) chunks.push(chunk);
      const body=JSON.parse(Buffer.concat(chunks));
      if(url.pathname.endsWith('/finalize')) {
        const a=attempts.get(url.pathname.split('/').at(-2));
        if(a.finalized_at && a.finalized_revision===body.revision && a.finished_at===body.finished_at) return json(res,a);
        if(a.revision!==body.revision) return json(res,{error:"draft changed"},409);
        a.finalized_revision=a.revision;
        a.finalized_at=new Date().toISOString();a.finished_at=body.finished_at;a.evidence_id="example-evidence";a.revision++;
        if(failFinalResponse) {failFinalResponse=false;return json(res,{error:"response interrupted"},503);}
        return json(res,a,201);
      }
      if(req.method==="POST") {
        if (attempts.has(body.id)) return json(res, attempts.get(body.id));
        if (body.execution_id) {
          if (executions.get(body.execution_id).closed_at) return json(res, {error:"campaign changed or is closed"}, 409);
          const prior = [...attempts.values()].find(a => a.execution_id === body.execution_id && a.procedure_ref === body.procedure_ref && !a.finalized_at);
          if (prior) return json(res, prior);
        }
        const a={...body,owner:"user:tester",revision:1,created_at:"2026-10-24T09:15:23.123456Z",updated_at:new Date().toISOString(),data:{repo:"",branch:"",rcs_ref:"",notes:"",result:"",answers:{}}};
        if(body.execution_id) {const e=executions.get(body.execution_id);Object.assign(a.data,{repo:e.repo,branch:e.branch,rcs_ref:e.rcs_ref});}
        attempts.set(a.id,a);return json(res,a,201);
      }
      const a=attempts.get(url.pathname.split("/").pop());
      draftWrites++;
      if(failDraftSave) return json(res,{error:"draft changed; reload"},409);
      if(a.finalized_at) return json(res,{error:"already finalized"},409);
      if(a.revision!==body.revision) return json(res,{error:"draft changed"},409);
      a.data=body.data;a.revision++;return json(res,a);
    }
    if(url.pathname==="/api/v1/campaign-attempts") {
      const rows = [...attempts.values()].filter(a=>(url.searchParams.has("execution_id") ? a.execution_id===url.searchParams.get("execution_id") : a.campaign_id===url.searchParams.get("campaign_id")) && a.procedure_ref===url.searchParams.get("procedure_ref")).reverse();
      const offset = Number(url.searchParams.get("offset"));
      return json(res,{attempts:rows.slice(offset, offset + 25),has_more:rows.length > offset + 25});
    }
    const row = attempts.get(url.pathname.split("/").pop());
    return row ? json(res, row) : json(res, {error:"attempt or campaign not found"}, 404);
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
  if (url.pathname === "/api/v1/blobs" && req.method === "POST") {
    const chunks = []; for await (const chunk of req) chunks.push(chunk);
    const digest = `sha256:${createHash("sha256").update(Buffer.concat(chunks)).digest("hex")}`;
    blobUploads++;
    return json(res, { digest, ref: `/api/v1/blobs/${digest}.png` });
  }
  if (url.pathname.startsWith("/api/v1/blobs/") || url.pathname === "/test-image.png") {
    res.writeHead(200, { "Content-Type": "image/png" });
    return res.end(imageBytes);
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
  await page.waitFor(`document.getElementById('tab-campaigns')?.hidden === false`);
  await page.eval(`document.getElementById('campaign-packages').click()`);
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

test("campaign search updates while typing and clearing restores the list", async () => {
  const id = crypto.randomUUID();
  executions.set(id, { id, package_id: rows[0].id, name: "Search regression campaign", repo: "example", branch: "main", rcs_ref: "abc", revision: 1 });
  await page.eval(`document.getElementById('campaign-packages-back').click()`);
  await page.waitFor(`document.querySelector('#campaign-runs [data-execution="${id}"]')`);
  await page.eval(`document.getElementById('campaign-search').value = 'zzzzzzzz'; document.getElementById('campaign-search').dispatchEvent(new Event('input', { bubbles: true }))`);
  await page.waitFor(`document.getElementById('campaign-runs-status').textContent.includes('No matching campaigns')`);
  assert.equal(await page.eval(`document.querySelectorAll('#campaign-runs [data-execution]').length`), 0);
  await page.eval(`document.getElementById('campaign-search').value = ''; document.getElementById('campaign-search').dispatchEvent(new Event('input', { bubbles: true }))`);
  await page.waitFor(`document.querySelector('#campaign-runs [data-execution="${id}"]')`);
  executions.delete(id);
  await page.eval(`document.getElementById('campaign-packages').click()`);
});

test("campaign browser paginates, previews controls/images, filters, and ignores late selections", async () => {
  assert.equal(await page.eval(`document.getElementById('tab-campaigns').hidden`), false);
  await assertPackageLayout(false);
  await page.eval(`document.getElementById('campaign-next').click()`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 1`);
  await page.eval(`document.getElementById('campaign-prev').click()`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 25`);
  delayA = true;
  await page.eval(`document.querySelectorAll('.campaign-package')[0].click(); document.querySelectorAll('.campaign-package')[1].click()`);
  await page.waitFor(`document.querySelector('#campaign-package-info h3')?.textContent === 'Patrol package-1'`);
  await new Promise(resolve => setTimeout(resolve, 350));
  assert.equal(await page.eval(`document.querySelector('#campaign-package-info h3').textContent`), "Patrol package-1");
  await assertPackageLayout(true);
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

test("closing package details restores the full list and ignores an outstanding selection", async () => {
  await page.eval(`document.getElementById('campaign-package-close').click()`);
  await assertPackageLayout(false);
  assert.equal(await page.eval(`document.activeElement.dataset.campaignId`), rows[1].id);
  await page.eval(`document.querySelector('#campaign-list .campaign-package-row time').click(); document.getElementById('campaign-package-close').click()`);
  await new Promise(resolve => setTimeout(resolve, 350));
  await assertPackageLayout(false);
  assert.equal(await page.eval(`document.querySelector('#campaign-list [aria-pressed="true"]') === null`), true);
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
  // Reproduce the upgrade failure: old draft JS cached against current HTML.
  // The old renderer addressed the now-removed required-fields list.
  await page.waitFor(`!!navigator.serviceWorker.controller`);
  await page.eval(`(async () => {
    const names = await caches.keys();
    const cache = await caches.open(names.find(name => name.startsWith('evidence-shell-')));
    await cache.put('/campaigndrafts.js', new Response(
      'export function mountCampaignDrafts() { document.getElementById("campaign-finalize-blockers").replaceChildren(); }',
      {headers:{'Content-Type':'text/javascript'}}));
  })()`);
  await page.goto(`${base}/?upgrade=1#campaigns`);
  await page.eval(`document.getElementById('campaign-packages').click()`);
  await page.waitFor(`document.querySelectorAll('.campaign-package').length === 25`);
  await page.eval(`document.querySelector('.campaign-package').click()`);
  await page.waitFor(`document.querySelector('.campaign-procedure-item')`);
  await page.eval(`document.querySelector('.campaign-procedure-item').click()`);
  await page.waitFor(`document.getElementById('campaign-attempt-list-status').textContent.includes('No saved drafts')`);
  await page.eval(`document.getElementById('campaign-attempt-start').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
  await page.eval(`document.getElementById('campaign-finished-created').click()`);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-at').value`), "2026-10-24T09:15:23.123456Z");
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.procedure_ref.readOnly`),true);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.source.readOnly`),true);
  assert.match(await page.eval(`document.querySelector('#campaign-overall-result legend').textContent`), /Overall procedure result/);
  assert.equal(await page.eval(`document.querySelector('#campaign-step-results-title').textContent`), "Procedure steps and individual checks");
  await page.eval(`(() => { const check=document.querySelector('#campaign-draft-controls [data-type=check]');check.value='PASS';check.dispatchEvent(new Event('input',{bubbles:true})); })()`);
  assert.equal(await page.eval(`document.querySelector('#campaign-overall-result input:checked')`),null,"passing a check must not select the overall result");
  assert.match(await page.eval(`document.querySelector('#campaign-draft-controls [data-type=check]').selectedOptions[0].textContent`), /This check: passed/);
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
  await page.eval(`document.getElementById('campaign-packages').click()`);
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
  assert.equal(await page.eval(`document.getElementById('campaign-finalize-blockers')`),null);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.rcs_ref.getAttribute('aria-invalid')`),"true");
  assert.equal(await page.eval(`getComputedStyle(document.getElementById('campaign-draft-form').elements.rcs_ref).borderTopWidth`),"2px");
  assert.equal(await page.eval(`document.getElementById('campaign-draft-dialog').open`),true);
  await page.eval(`(() => {const f=document.getElementById('campaign-draft-form');f.elements.branch.value='main';f.elements.rcs_ref.value='deployed-build';f.dispatchEvent(new Event('input'));document.getElementById('campaign-draft-finalize').click();})()`);
  assert.match(await page.eval(`document.getElementById('campaign-draft-status').textContent`), /overall Result/);
  assert.equal(await page.eval(`document.getElementById('campaign-overall-result').getAttribute('aria-invalid')`),"true");
  await page.eval(`document.querySelector('#campaign-draft-form [name=result][value=FAIL]').click()`);
  assert.equal(await page.eval(`document.getElementById('campaign-overall-result').hasAttribute('aria-invalid')`),false);
  await page.eval(`document.getElementById('campaign-finished-now').click()`);
  assert.ok(Math.abs(Date.now() - Date.parse(await page.eval(`document.getElementById('campaign-finished-at').value`))) < 5000);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-picker-panel').hidden`), true);
  await page.eval(`document.getElementById('campaign-finished-pick').click()`);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-picker-panel').hidden`), false);
  await page.eval(`document.getElementById('campaign-finished-picker').value='2026-10-25T12:30';document.getElementById('campaign-finished-picker').dispatchEvent(new Event('input'));document.getElementById('campaign-finished-picker-done').click();`);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-picker-panel').hidden`), true);
  assert.equal(await page.eval(`document.getElementById('campaign-finished-at').value`), "2026-10-25T12:30:00.000Z");
  await page.eval(`window.finalizeConfirmations=0;window.confirm=()=>{window.finalizeConfirmations++;return false;}`);
  const writesBefore = draftWrites;
  failFinalResponse = true;
  await page.eval(`document.getElementById('campaign-draft-finalize').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('response interrupted')`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-dialog').open`),true);
  assert.equal(draftWrites,writesBefore+1,"current edits must be saved automatically");
  assert.equal([...attempts.values()][0].data.rcs_ref,"deployed-build");
  assert.equal([...attempts.values()][0].data.result,"FAIL");
  await page.eval(`document.getElementById('campaign-draft-finalize').click()`);
  await page.waitFor(`!document.getElementById('campaign-draft-dialog').open`);
  assert.equal(draftWrites,writesBefore+1,"retry after lost finalization response must not save again");
  assert.equal(await page.eval(`window.finalizeConfirmations`),0,"finalize must not require another confirmation");
  await page.waitFor(`document.querySelector('[data-attempt-id]')?.textContent.startsWith('Final: ')`);
  await page.eval(`document.querySelector('[data-attempt-id]').click()`);
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
  await page.eval(`document.getElementById('campaign-packages').click()`);
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

test("shared campaign workspace starts, saves on navigation, retains conflicts and closes/reopens", async () => {
  role = "writer";
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  await page.goto(`${base}/#campaigns`);
  await page.waitFor(`!document.getElementById('campaign-home').hidden`);
  await page.eval(`document.getElementById('campaign-new').click()`);
  await page.waitFor(`document.querySelector('#campaign-package-picker [data-package]')`);
  await page.eval(`document.querySelector('#campaign-package-picker [data-package]').click()`);
  await page.waitFor(`!document.getElementById('campaign-start-form').hidden`);
  await page.eval(`(() => {const f=document.getElementById('campaign-start-form');f.elements.name.value='Shared field campaign';f.repo.value='org/product';f.branch.value='main';f.rcs_ref.value='deployed-build';f.requestSubmit();})()`);
  await page.waitFor(`document.querySelectorAll('#campaign-work-procedures [data-procedure]').length===13`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-dialog').matches(':modal')`), false);
  assert.equal(await page.eval(`document.querySelector('#campaign-draft-form [name=rcs_ref]').readOnly`), true);
  assert.equal(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), true);
  await page.eval(`(() => {const input=document.querySelector('#campaign-draft-form [name=notes]');input.value='Keep my observations';input.dispatchEvent(new Event('input',{bubbles:true}));document.getElementById('campaign-work-next').click();})()`);
  await page.waitFor(`document.getElementById('campaign-draft-title').textContent.includes('Equipment check 1')`);
  const first=[...attempts.values()].find(a=>a.execution_id && a.procedure_ref==='patrol/display');
  assert.equal(first.data.notes,'Keep my observations');
  failDraftSave = true;
  await page.eval(`(() => {const input=document.querySelector('#campaign-draft-form [name=notes]');input.value='Conflict stays here';input.dispatchEvent(new Event('input',{bubbles:true}));document.getElementById('campaign-work-next').click();})()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Not saved')`);
  assert.equal(await page.eval(`document.querySelector('#campaign-draft-form [name=notes]').value`),'Conflict stays here');
  assert.equal(await page.eval(`document.getElementById('campaign-draft-title').textContent.includes('Equipment check 1')`),true);
  failDraftSave = false;
  await page.eval(`window.confirm=()=>true;document.getElementById('campaign-work-close').click()`);
  await page.waitFor(`document.getElementById('campaign-work-close').textContent==='Reopen campaign'`);
  assert.match(await page.eval(`document.getElementById('campaign-work-progress').textContent`),/13 skipped/);
  await page.eval(`document.getElementById('campaign-work-close').click()`);
  await page.waitFor(`document.getElementById('campaign-work-close').textContent==='Finish campaign'`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open && document.getElementById('campaign-draft-title').textContent.startsWith('Finalized:') && !document.getElementById('campaign-attempt-start').disabled`);
  await page.eval(`document.getElementById('campaign-work-history').open=true;document.getElementById('campaign-attempt-start').click()`);
  await page.waitFor(`!document.getElementById('campaign-draft-fields').disabled`);
  assert.equal([...attempts.values()].filter(a=>a.execution_id && a.procedure_ref==='patrol/display').length,2);
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  assert.equal(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), true);
  assert.deepEqual(page.problems, []);
});

test("starting another campaign clears setup and never reuses the earlier execution", async () => {
  role = "writer";
  await page.eval(`document.getElementById('campaign-work-back').click()`);
  await page.waitFor(`!document.getElementById('campaign-home').hidden`);
  await page.eval(`document.getElementById('campaign-new').click()`);
  assert.equal(await page.eval(`document.getElementById('campaign-start-form').hidden`), true);
  await page.eval(`document.querySelector('#campaign-package-picker [data-package]').click()`);
  await page.waitFor(`!document.getElementById('campaign-start-form').hidden`);
  assert.equal(await page.eval(`document.getElementById('campaign-start-form').repo.value`), "");
  const before = executions.size;
  await page.eval(`(() => {const f=document.getElementById('campaign-start-form');f.elements.name.value='Shared field campaign';f.repo.value='org/product';f.branch.value='main';f.rcs_ref.value='deployed-build';f.requestSubmit();})()`);
  await page.waitFor(`!document.getElementById('campaign-workspace').hidden && document.querySelectorAll('#campaign-work-procedures [data-procedure]').length===13`);
  assert.equal(executions.size, before + 1);
  assert.deepEqual(page.problems, []);
});

test("a colleague closing first cannot turn Finish into Reopen", async () => {
  const id = await page.eval(`new URLSearchParams(location.search).get('campaign_execution')`);
  const execution = executions.get(id);
  execution.closed_at = new Date().toISOString(); execution.revision++;
  await page.eval(`document.getElementById('campaign-work-close').click()`);
  await page.waitFor(`document.getElementById('campaign-work-status').textContent.includes('already closed')`);
  assert.ok(execution.closed_at);
  assert.equal(await page.eval(`document.getElementById('campaign-work-close').textContent`), "Reopen campaign");
  assert.deepEqual(page.problems, []);
});

test("read-only collaborators can inspect shared attempt history", async () => {
  role = "viewer";
  const e = [...executions.values()].find(e => [...attempts.values()].some(a => a.execution_id === e.id));
  await page.goto(`${base}/?campaign_execution=${e.id}#campaigns`);
  await page.waitFor(`document.querySelector('#campaign-work-procedures [data-procedure]')`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
  assert.equal(await page.eval(`document.getElementById('campaign-attempt-tools').hidden`), false);
  assert.equal(await page.eval(`document.getElementById('campaign-attempt-start').hidden`), true);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-fields').disabled`), true);
  await page.waitFor(`document.querySelectorAll('#campaign-attempt-list [data-attempt-id]').length===2`);
  assert.deepEqual(page.problems, []);
});

test("import offers an explicit execution start with product commit defaults, including duplicates", async () => {
  role = "writer";
  rows[0].definition.manifest.product = {repository: "https://example.org/product", branch: "release", commit: "a".repeat(40)};
  const count = executions.size;
  await page.goto(`${base}/#campaigns`);
  await page.eval(`document.getElementById('campaign-new').click()`);
  await page.waitFor(`document.querySelectorAll('#campaign-package-picker .campaign-package-row').length === 25`);
  const pickerRows = await page.eval(`[...document.querySelectorAll('#campaign-package-picker .campaign-package-row')].map(row => [...row.children].map(el => el.textContent))`);
  await page.eval(`document.getElementById('campaign-new-back').click()`);
  await page.eval(`document.getElementById('campaign-packages').click()`);
  await page.waitFor(`document.querySelectorAll('#campaign-list .campaign-package').length === 25`);
  assert.deepEqual(await page.eval(`[...document.querySelectorAll('#campaign-list .campaign-package-row')].map(row => [...row.children].map(el => el.textContent))`), pickerRows);
  await upload("good");
  await page.waitFor(`document.getElementById('campaign-import-status').textContent.includes('already imported')`);
  assert.equal(executions.size, count, "import must not start an execution");
  const listing = await page.eval(`(() => {
    const row = document.querySelector('#campaign-list .campaign-package-row');
    const children = [...row.children];
    return { text: children.map(el => el.textContent), repo: row.querySelector('a').href,
      tops: children.map(el => el.getBoundingClientRect().top), nowrap: getComputedStyle(children[0]).whiteSpace };
  })()`);
  assert.deepEqual(listing.text, ['2026-10-25 10:00', 'Patrol package-0', '1', 'product', 'aaaaaaaa']);
  assert.equal(listing.repo, 'https://example.org/product');
  assert.equal(listing.nowrap, 'nowrap');
  assert.ok(Math.max(...listing.tops) - Math.min(...listing.tops) < 4, 'all five fields share one line');
  for (const id of ['campaign-list', 'campaign-package-picker']) {
    const table = await page.eval(`(() => {
      const body = document.getElementById('${id}');
      const table = body.closest('table');
      return { headers: [...table.querySelectorAll('th')].map(h => h.textContent),
        columns: [...table.querySelectorAll('th')].map(h => h.getBoundingClientRect().left),
        rows: [...body.querySelectorAll('tr')].slice(0, 3).map(row => [...row.cells].map(cell => cell.getBoundingClientRect().left)) };
    })()`);
    assert.deepEqual(table.headers, ['Added', 'Name', 'Procedures', 'Repository', 'Product revision']);
    for (const cells of table.rows) assert.deepEqual(cells, table.columns, 'each cell aligns with its column header');
  }
  await page.eval(`navigator.clipboard.writeText = async value => { window.copiedPackageCommit = value; }; document.querySelector('#campaign-list [data-copy-commit]').click()`);
  assert.equal(await page.eval(`window.copiedPackageCommit`), 'a'.repeat(40));
  assert.equal(await page.eval(`document.getElementById('campaign-new-view').hidden`), true);
  await page.eval(`document.getElementById('campaign-package-start').click()`);
  await page.waitFor(`!document.getElementById('campaign-start-form').hidden`);
  assert.deepEqual(await page.eval(`['repo','branch','rcs_ref'].map(n => document.getElementById('campaign-start-form').elements[n].value)`),
    ["https://example.org/product", "release", "a".repeat(40)]);
  assert.equal(executions.size, count, "offering start must not submit it");
  await page.eval(`document.getElementById('campaign-start').click()`);
  await page.waitFor(`!document.getElementById('campaign-workspace').hidden && document.getElementById('campaign-work-context').textContent.includes('${"a".repeat(40)}')`);
  assert.equal(executions.size, count + 1);
  assert.deepEqual(page.problems, []);
});

test("Details deliberately unlocks defaults and keeps overridden fields highlighted after saving", async () => {
  const e = [...executions.values()].at(-1);
  await page.eval(`document.getElementById('campaign-work-back').click()`);
  await page.waitFor(`document.querySelector('#campaign-runs [data-execution="${e.id}"]')`);
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
  const filterBottoms = await page.eval(`['campaign-search','campaign-state','campaign-runs-refresh'].map(id => document.getElementById(id).getBoundingClientRect().bottom)`);
  assert.ok(Math.max(...filterBottoms) - Math.min(...filterBottoms) < 2, `campaign filters and Refresh share a baseline: ${filterBottoms}`);
  const table = await page.eval(`(() => {
    const row = document.querySelector('#campaign-runs [data-execution="${e.id}"]').closest('tr');
    return { headers: [...row.closest('table').querySelectorAll('th')].map(el => el.textContent),
      cells: [...row.cells].map(el => el.textContent.trim()), href: row.cells[1].querySelector('a').href,
      state: row.querySelector('.procedure-state').getAttribute('aria-label') };
  })()`);
  assert.deepEqual(table.headers, ['Name','Repo','Branch','Commit','Exercised','Passed','Skipped']);
  assert.deepEqual(table.cells.slice(1), ['product','release','aaaaaaaa','0/13','0/13','0/13']);
  assert.equal(table.href, 'https://example.org/product');
  assert.equal(table.state, 'In progress');
  await page.eval(`document.querySelector('#campaign-runs [data-execution="${e.id}"]').closest('tr').querySelector('[data-copy-commit]').click()`);
  assert.equal(await page.eval(`window.copiedPackageCommit`), e.rcs_ref);
  assert.equal(await page.eval(`document.getElementById('campaign-home').hidden`), false);
  await page.eval(`document.querySelector('#campaign-runs [data-execution="${e.id}"]').click()`);
  await page.waitFor(`document.querySelector('#campaign-work-procedures [data-procedure]')`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
  const layout = await page.eval(`(() => {
    const actions = [...document.querySelectorAll('.campaign-draft-actions > button')];
    const time = [...document.querySelectorAll('.campaign-finish-tools > input, .campaign-finish-tools > button')];
    const procedure = document.querySelector('#campaign-work-procedures .campaign-procedure-run');
    return { labels: actions.map(b => b.textContent), actionTops: actions.map(b => b.getBoundingClientRect().top),
      timeTops: time.map(b => b.getBoundingClientRect().top),
      statusTop: document.getElementById('campaign-draft-status').getBoundingClientRect().top,
      actionBottom: actions[0].getBoundingClientRect().bottom,
      lines: [...procedure.querySelectorAll('strong,small')].map(el => ({align:getComputedStyle(el).textAlign, nowrap:getComputedStyle(el).whiteSpace})),
      state: procedure.querySelector('.procedure-state').getAttribute('aria-label') };
  })()`);
  assert.deepEqual(layout.labels, ['Save draft', 'Reload draft', 'Finalize']);
  assert.ok(Math.max(...layout.actionTops) - Math.min(...layout.actionTops) < 2);
  assert.ok(Math.max(...layout.timeTops) - Math.min(...layout.timeTops) < 5);
  assert.ok(layout.statusTop >= layout.actionBottom);
  assert.deepEqual(layout.lines, [{align:'left',nowrap:'nowrap'},{align:'left',nowrap:'nowrap'}]);
  assert.equal(layout.state, 'In progress');
  assert.equal(await page.eval(`document.getElementById('campaign-draft-fields').firstElementChild.id`), 'draft-software-context');
  assert.equal(await page.eval(`document.getElementById('draft-software-context').nextElementSibling.id`), 'draft-environment');
  assert.equal(await page.eval(`document.getElementById('draft-software-context').open`), false);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.rcs_ref.readOnly`), true);
  await page.eval(`document.getElementById('draft-software-summary').click()`);
  await page.waitFor(`!document.getElementById('campaign-draft-form').elements.rcs_ref.readOnly`);
  await page.eval(`(() => { const input = document.getElementById('campaign-draft-form').elements.rcs_ref; input.value = 'deliberate-override'; input.dispatchEvent(new Event('input', {bubbles:true})); })()`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.rcs_ref.classList.contains('draft-overridden')`), true);
  await page.eval(`document.getElementById('campaign-draft-save').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Saved on server')`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.rcs_ref.classList.contains('draft-overridden')`), true);
  assert.notEqual(e.rcs_ref, 'deliberate-override', 'override must not modify the shared execution');
  await page.eval(`(() => { const input = document.getElementById('campaign-draft-form').elements.rcs_ref; input.value = '${e.rcs_ref}'; input.dispatchEvent(new Event('input', {bubbles:true})); })()`);
  assert.equal(await page.eval(`document.getElementById('campaign-draft-form').elements.rcs_ref.classList.contains('draft-overridden')`), false);
  await page.eval(`document.getElementById('campaign-draft-save').click()`);
  await page.waitFor(`!document.getElementById('campaign-draft-save').disabled`);
});

test("prepared campaign survives offline reload, captures attachments and synchronizes completed results", async () => {
  role = "writer";
  const e = { id: crypto.randomUUID(), package_id: rows[0].id, name: "Offline proving ground", repo: "vehicle", branch: "main", rcs_ref: "build-offline", revision: 1 };
  executions.set(e.id, e);
  const url = `${base}/?campaign_execution=${e.id}#campaigns`;
  await page.goto(url);
  await page.waitFor(`navigator.serviceWorker.controller && document.getElementById('campaign-work-title').textContent === 'Offline proving ground' && !document.getElementById('campaign-work-prepare').disabled`);
  await page.eval(`document.getElementById('campaign-work-prepare').click()`);
  await page.waitFor(`document.getElementById('campaign-offline-status').textContent.startsWith('Prepared ')`);
  await page.offline(true);
  await page.goto(url);
  await page.waitFor(`document.querySelector('#campaign-work-procedures [data-procedure]')`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
  await page.waitFor(`document.querySelector('#campaign-draft-controls img')?.src.startsWith('blob:')`);
  await page.eval(`(() => {
    const form = document.getElementById('campaign-draft-form');
    form.elements.notes.value = 'Offline notes survive reload';
    form.elements.notes.dispatchEvent(new Event('input', {bubbles:true}));
    const transfer = new DataTransfer();
    transfer.items.add(new File([Uint8Array.from(atob('${imageBytes.toString("base64")}'), c => c.charCodeAt(0))], 'field.png', {type:'image/png'}));
    const input = document.getElementById('draft-photo-input'); input.files = transfer.files; input.dispatchEvent(new Event('change'));
  })()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Saved on this device')`);
  await page.goto(url);
  await page.waitFor(`document.querySelector('#campaign-work-procedures [data-procedure]')`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-form').elements.notes.value === 'Offline notes survive reload'`);
  assert.match(await page.eval(`document.getElementById('campaign-draft-form').elements.observations.value`), /field.*\/api\/v1\/blobs\//);
  await page.eval(`(() => {
    const form = document.getElementById('campaign-draft-form');
    form.querySelector('[data-answer="status"]').value = 'PASS';
    form.querySelector('[data-answer="operator"]').value = 'Field tester';
    form.querySelector('[name="result"][value="PASS"]').checked = true;
    form.dispatchEvent(new Event('input', {bubbles:true}));
    document.getElementById('campaign-finished-now').click();
    document.getElementById('campaign-draft-finalize').click();
  })()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Completed locally')`);
  assert.equal([...attempts.values()].some(a => a.execution_id === e.id), false);
  await page.offline(false);
  await page.eval(`document.getElementById('campaign-work-sync').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Finalized as immutable evidence')`);
  const finalized = [...attempts.values()].filter(a => a.execution_id === e.id);
  assert.equal(finalized.length, 1);
  assert.equal(finalized[0].data.notes, 'Offline notes survive reload');
  assert.equal(finalized[0].data.result, 'PASS');
  assert.ok(finalized[0].evidence_id);
  // A later shared edit must surface both versions and require a real choice.
  await page.eval(`document.querySelectorAll('#campaign-work-procedures [data-procedure]')[1].click()`);
  await page.waitFor(`document.getElementById('campaign-draft-form').elements.procedure_ref.value === 'patrol/check-1'`);
  await page.eval(`document.getElementById('campaign-work-sync').click()`);
  await page.waitFor(`!document.getElementById('campaign-work-sync').disabled`);
  const shared = [...attempts.values()].find(a => a.execution_id === e.id && a.procedure_ref === 'patrol/check-1');
  shared.data.notes = 'Colleague changed this'; shared.revision++;
  await page.eval(`(() => { const input = document.getElementById('campaign-draft-form').elements.notes; input.value = 'Local competing notes'; input.dispatchEvent(new Event('input', {bubbles:true})); })()`);
  await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Saved on this device')`);
  await page.eval(`document.getElementById('campaign-work-sync').click()`);
  await page.waitFor(`!document.getElementById('campaign-draft-conflict').hidden`);
  const versions = await page.eval(`document.getElementById('campaign-conflict-versions').textContent`);
  assert.match(versions, /Local competing notes/); assert.match(versions, /Colleague changed this/);
  await page.eval(`window.confirm = () => true; document.getElementById('campaign-conflict-local').click()`);
  await page.waitFor(`document.getElementById('campaign-draft-conflict').hidden`);
  await page.eval(`document.getElementById('campaign-work-sync').click()`);
  await page.waitFor(`!document.getElementById('campaign-work-sync').disabled`);
  assert.equal(shared.data.notes, 'Local competing notes');
  assert.deepEqual(page.problems, []);
});

test("failed offline creations can be retried after reopening or archived from the conflict panel", async () => {
  for (const action of ["retry", "archive"]) {
    const e = { id: crypto.randomUUID(), package_id: rows[0].id, name: `Recover ${action}`, repo: "vehicle", branch: "main", rcs_ref: "build", revision: 1 };
    executions.set(e.id, e);
    await page.goto(`${base}/?campaign_execution=${e.id}#campaigns`);
    await page.waitFor(`document.getElementById('campaign-work-title').textContent === '${e.name}' && !document.getElementById('campaign-work-prepare').disabled`);
    await page.eval(`document.getElementById('campaign-work-prepare').click()`);
    await page.waitFor(`document.getElementById('campaign-offline-status').textContent.startsWith('Prepared ')`);
    await page.offline(true);
    await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
    await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
    await page.eval(`(() => { const input = document.getElementById('campaign-draft-form').elements.notes; input.value = 'Keep these offline answers'; input.dispatchEvent(new Event('input', {bubbles:true})); })()`);
    await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Saved on this device')`);
    const id = await page.eval(`document.querySelector('#campaign-attempt-list [data-attempt-id]').dataset.attemptId`);
    e.closed_at = new Date().toISOString(); e.revision++;
    await page.offline(false);
    await page.eval(`document.getElementById('campaign-work-sync').click()`);
    await page.waitFor(`!document.getElementById('campaign-draft-conflict').hidden && document.getElementById('campaign-work-close').textContent === 'Reopen campaign'`);
    assert.equal(await page.eval(`document.getElementById('campaign-conflict-local').hidden`), true);
    assert.equal(await page.eval(`document.getElementById('campaign-conflict-${action}').hidden`), false);
    assert.equal(attempts.has(id), false);
    if (action === "retry") {
      await page.eval(`document.getElementById('campaign-conflict-retry').click()`);
      await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('still closed')`);
      await page.eval(`document.getElementById('campaign-work-close').click()`);
      await page.waitFor(`document.getElementById('campaign-work-close').textContent === 'Finish campaign'`);
      await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
      await page.waitFor(`document.getElementById('campaign-draft-dialog').open && !document.getElementById('campaign-conflict-retry').hidden`);
      await page.eval(`document.getElementById('campaign-conflict-retry').click()`);
      await page.waitFor(`document.getElementById('campaign-draft-conflict').hidden && document.getElementById('campaign-draft-status').textContent.includes('Saved on server')`);
      assert.equal(attempts.get(id).data.notes, "Keep these offline answers");
    } else {
      await page.eval(`window.confirm = () => true; document.getElementById('campaign-conflict-archive').click()`);
      await page.waitFor(`!document.getElementById('campaign-draft-dialog').open && document.getElementById('campaign-offline-status').textContent.includes('0 attempts waiting')`);
      const archived = await page.eval(`(async () => { const {campaignStore} = await import('./campaignoffline.js'); return (await campaignStore('user:tester').all('archive')).find(e => e.row.id === '${id}'); })()`);
      assert.equal(archived.operations.at(-1).data.notes, "Keep these offline answers");
      assert.equal(attempts.has(id), false);
    }
  }
  assert.deepEqual(page.problems, []);
});

test("prepared campaigns expose full paginated history online and label the offline subset", async () => {
  const e = { id: crypto.randomUUID(), package_id: rows[0].id, name: "Historical attempts", repo: "vehicle", branch: "main", rcs_ref: "build", revision: 1 };
  executions.set(e.id, e);
  for (let i = 0; i < 30; i++) {
    const id = crypto.randomUUID();
    attempts.set(id, { id, execution_id: e.id, campaign_id: e.package_id, procedure_ref: "patrol/display", owner: "user:tester", revision: 2,
      created_at: "2026-11-04T10:00:00Z", updated_at: "2026-11-04T10:00:00Z", finalized_at: "2026-11-04T10:00:00Z", evidence_id: `evidence-${i}`,
      data: { repo: e.repo, branch: e.branch, rcs_ref: e.rcs_ref, answers: {}, notes: `Historical attempt ${i}`, result: "PASS" } });
  }
  const url = `${base}/?campaign_execution=${e.id}#campaigns`;
  await page.goto(url);
  await page.waitFor(`document.getElementById('campaign-work-title').textContent === '${e.name}' && !document.getElementById('campaign-work-prepare').disabled`);
  await page.eval(`document.getElementById('campaign-work-prepare').click()`);
  await page.waitFor(`document.getElementById('campaign-offline-status').textContent.startsWith('Prepared ')`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.querySelectorAll('#campaign-attempt-list [data-attempt-id]').length === 25`);
  await page.eval(`document.getElementById('campaign-attempt-next').click()`);
  await page.waitFor(`document.querySelectorAll('#campaign-attempt-list [data-attempt-id]').length === 5`);
  assert.equal(await page.eval(`document.getElementById('campaign-attempt-next').disabled`), true);
  await page.offline(true);
  await page.goto(url);
  await page.waitFor(`document.querySelector('#campaign-work-procedures [data-procedure]')`);
  await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
  await page.waitFor(`document.getElementById('campaign-attempt-list-status').textContent.startsWith('Offline:')`);
  assert.equal(await page.eval(`document.querySelectorAll('#campaign-attempt-list [data-attempt-id]').length`), 1);
  await page.offline(false);
  assert.deepEqual(page.problems, []);
});

test("IndexedDB enumeration bounds the query before reading media or another principal's data", async () => {
  const result = await page.eval(`(async () => {
    const {campaignStore} = await import('./campaignoffline.js');
    const scope = crypto.randomUUID(), own = campaignStore(scope), other = campaignStore(scope + '-other');
    await own.put('attempt', 'one', {id:'one'});
    await own.put('blob', 'large', {bytes:new Uint8Array(1024 * 1024).buffer});
    await other.put('attempt', 'two', {id:'two'});
    const original = IDBObjectStore.prototype.getAll;
    let bounded = false;
    IDBObjectStore.prototype.getAll = function(range, ...args) {
      // Reconnection may also be syncing the application's own principal.
      if (!range || range.lower?.[0] === scope) {
        bounded = range instanceof IDBKeyRange && range.includes([scope, 'attempt', 'one'])
          && !range.includes([scope, 'blob', 'large']) && !range.includes([scope + '-other', 'attempt', 'two']);
        if (!bounded) throw new Error('Unbounded campaign query');
      }
      return original.call(this, range, ...args);
    };
    try { return {rows:await own.all('attempt'), bounded}; }
    finally {
      IDBObjectStore.prototype.getAll = original;
      await own.remove('attempt', 'one'); await own.remove('blob', 'large'); await other.remove('attempt', 'two');
    }
  })()`);
  assert.deepEqual(result, { rows: [{ id: "one" }], bounded: true });
});

test("online capture, attachments and campaign lifecycle work without offline browser capabilities", async () => {
  const overrides = [
    `Object.defineProperty(navigator, 'locks', {value:undefined});`,
    `Object.defineProperty(window, 'indexedDB', {value:undefined});`,
    `indexedDB.open = () => { throw new DOMException('Storage denied', 'SecurityError'); };`,
  ];
  for (const source of overrides) {
    const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", { source });
    try {
      const e = { id: crypto.randomUUID(), package_id: rows[0].id, name: "Online only", repo: "vehicle", branch: "main", rcs_ref: "build", revision: 1 };
      executions.set(e.id, e);
      await page.goto(`${base}/?campaign_execution=${e.id}#campaigns`);
      await page.waitFor(`document.querySelector('#campaign-work-procedures [data-procedure]') && document.getElementById('campaign-work-prepare').disabled`);
      await page.eval(`document.querySelector('#campaign-work-procedures [data-procedure]').click()`);
      await page.waitFor(`document.getElementById('campaign-draft-dialog').open`);
      const before = blobUploads;
      await page.eval(`(() => {
        const form = document.getElementById('campaign-draft-form');
        form.elements.notes.value = 'Online-only notes';
        form.querySelector('[data-answer="status"]').value = 'PASS';
        form.querySelector('[data-answer="operator"]').value = 'Online tester';
        form.querySelector('[name="result"][value="PASS"]').checked = true;
        form.dispatchEvent(new Event('input', {bubbles:true}));
        const transfer = new DataTransfer();
        transfer.items.add(new File([Uint8Array.from(atob('${imageBytes.toString("base64")}'), c => c.charCodeAt(0))], 'online.png', {type:'image/png'}));
        const input = document.getElementById('draft-photo-input'); input.files = transfer.files; input.dispatchEvent(new Event('change'));
      })()`);
      await page.waitFor(`document.getElementById('campaign-draft-form').elements.observations.value.includes('/api/v1/blobs/')`);
      assert.ok(blobUploads > before);
      await page.eval(`document.getElementById('campaign-draft-save').click()`);
      await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Saved on server') && !document.getElementById('campaign-draft-save').disabled`);
      await page.eval(`document.getElementById('campaign-finished-now').click(); document.getElementById('campaign-draft-finalize').click()`);
      await page.waitFor(`document.getElementById('campaign-draft-status').textContent.includes('Finalized as immutable evidence') && !document.getElementById('campaign-work-close').disabled`);
      const attempt = [...attempts.values()].find(a => a.execution_id === e.id);
      assert.equal(attempt.data.notes, "Online-only notes");
      assert.ok(attempt.evidence_id);
      await page.eval(`window.confirm = () => true; document.getElementById('campaign-work-close').click()`);
      await page.waitFor(`document.getElementById('campaign-work-close').textContent === 'Reopen campaign'`);
      await page.eval(`document.getElementById('campaign-work-close').click()`);
      await page.waitFor(`document.getElementById('campaign-work-close').textContent === 'Finish campaign'`);
    } finally { await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier }); }
  }
  assert.deepEqual(page.problems, []);
});

async function assertPackageLayout(open) {
  const layout = await page.eval(`(() => {
    const root = document.getElementById('campaign-package-layout');
    const list = root.querySelector('aside').getBoundingClientRect();
    const pane = document.getElementById('campaign-package-pane');
    const rect = pane.getBoundingClientRect();
    return {width:root.getBoundingClientRect().width, listWidth:list.width, listRight:list.right,
      paneLeft:rect.left, hidden:pane.hidden, split:root.classList.contains('detail-open'), screen:innerWidth};
  })()`);
  assert.equal(layout.hidden, !open);
  assert.equal(layout.split, open);
  if (!open || layout.screen < 1024) assert.ok(Math.abs(layout.width-layout.listWidth) < 2, 'plain listing uses full available width');
  else assert.ok(layout.paneLeft > layout.listRight && layout.listWidth < layout.width, 'selected package details appear to the right of the list');
}

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
