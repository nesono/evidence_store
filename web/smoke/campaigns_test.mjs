// Self-contained browser checks with deterministic API responses. Run separately
// from the live-server smoke suite: node --test web/smoke/campaigns_test.mjs
import test, { before, after } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { connect, launchChrome } from "./driver.mjs";

const digest = `sha256:${"a".repeat(64)}`;
function packageRow(id) {
  const entry = { id: "display", path: "scenarios/display/scenario.yaml" };
  const definition = { id: "display", title: "Display status", procedure_ref: "patrol/display", description: "procedure.md", defaults: { evidence_type: "manual_test" } };
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
const rows = Array.from({ length: 26 }, (_, i) => packageRow(`package-${i}`));
// A campaign larger than one procedure page exercises incremental filtering
// over the whole package, rather than only the visible list.
for (const row of rows) {
  const original = row.definition.scenarios[0];
  for (let i = 1; i <= 12; i++) {
    row.definition.scenarios.push({ ...original, definition: { ...original.definition, id: `check-${i}`, title: `Equipment check ${i}`, procedure_ref: `patrol/check-${i}` } });
    row.definition.manifest.scenarios.push({ id: `check-${i}`, path: "scenarios/display/scenario.yaml" });
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
  assert.equal(await page.eval(`document.getElementById('campaign-procedure-inspection').hidden`), true);
  await page.eval(`document.getElementById('campaign-procedure-next').click()`);
  assert.equal(await page.eval(`document.querySelectorAll('.campaign-procedure-item').length`), 3);
  await page.eval(`document.getElementById('campaign-procedure-search').value='DISPLAY'; document.getElementById('campaign-procedure-search').dispatchEvent(new Event('input'))`);
  assert.equal(await page.eval(`document.querySelectorAll('.campaign-procedure-item').length`), 1);
  assert.equal(await page.eval(`document.getElementById('campaign-procedure-prev').disabled`), true);
  await page.eval(`document.querySelector('.campaign-procedure-item').click()`);
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
  await page.eval(`document.querySelector('.campaign-procedure-item').click()`);
  assert.equal(await page.eval(`document.documentElement.scrollWidth <= window.innerWidth`), true);
  assert.deepEqual(page.problems, []);
});
