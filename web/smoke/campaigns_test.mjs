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
    if(url.pathname==="/api/v1/campaign-attempts") return json(res,{attempts:[...attempts.values()].filter(a=>(url.searchParams.has("execution_id") ? a.execution_id===url.searchParams.get("execution_id") : a.campaign_id===url.searchParams.get("campaign_id")) && a.procedure_ref===url.searchParams.get("procedure_ref")),has_more:false});
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
  await page.waitFor(`document.getElementById('campaign-packages')`);
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
  assert.match(await page.eval(`document.getElementById('campaign-finalize-status').textContent`), /overall Result/);
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
  await page.waitFor(`document.getElementById('campaign-finalize-status').textContent.includes('response interrupted')`);
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

test("prepared campaign survives offline reload, captures attachments and synchronizes completed results", async () => {
  role = "writer";
  const e = { id: crypto.randomUUID(), package_id: rows[0].id, name: "Offline proving ground", repo: "vehicle", branch: "main", rcs_ref: "build-offline", revision: 1 };
  executions.set(e.id, e);
  const url = `${base}/?campaign_execution=${e.id}#campaigns`;
  await page.goto(url);
  await page.waitFor(`navigator.serviceWorker.controller && document.getElementById('campaign-work-title').textContent === 'Offline proving ground'`);
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
