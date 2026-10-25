import test from "node:test";
import assert from "node:assert/strict";
import { createCampaignAPI } from "../static/campaigns.js";
import { MAX_CAMPAIGN_BYTES, matchingScenarios, packageInfoHTML, packageListHTML, procedurePage, procedureListHTML, procedureHTML, resolveAssetImages, uploadProblem } from "../static/campaignview.js";
import { tabFromHash } from "../static/offline.js";

test("campaign upload validates selection, empty files and the compressed limit", () => {
  assert.match(uploadProblem(null), /Choose/);
  assert.match(uploadProblem({ size: 0 }), /empty/);
  assert.match(uploadProblem({ size: MAX_CAMPAIGN_BYTES + 1 }), /16 MiB/);
  assert.equal(uploadProblem({ size: MAX_CAMPAIGN_BYTES }), "");
  assert.equal(tabFromHash("#campaigns"), "campaigns");
});

test("campaign key, imported UUID, and archive digest have distinct labels", () => {
  const row = { id: "f5c0b62a-70c6-4f3d-8327-d0ba2edaf3dc", digest: `sha256:${"b".repeat(64)}`,
    imported_at: "2026-10-25T10:00:00Z", imported_by: "user:tester",
    definition: { manifest: { campaign: { id: "equipment-patrol", name: "Equipment patrol" },
      source: { repository: "https://example.org/scenarios" } }, scenarios: [] } };
  const html = packageInfoHTML(row);
  assert.match(html, /<dt>Campaign key<\/dt><dd>equipment-patrol<\/dd>/);
  assert.ok(html.includes(`<dt>Imported package UUID</dt><dd><code>${row.id}</code>`));
  assert.ok(html.includes(`<dt>Package digest</dt><dd><code>${row.digest}</code>`));
  assert.ok(!html.includes("<dt>Campaign ID</dt>"));
});

test("API sends raw bytes and distinguishes duplicate imports", async () => {
  const calls = [];
  const file = new Blob(["archive"], { type: "application/octet-stream" });
  const api = createCampaignAPI(async (url, options) => {
    calls.push({ url, options });
    return new Response(JSON.stringify({ id: "existing" }), { status: 200 });
  });
  assert.deepEqual(await api.upload(file), { row: { id: "existing" }, duplicate: true });
  assert.equal(calls[0].options.body, file);
  assert.equal(calls[0].options.method, "POST");
  assert.equal(calls[0].options.headers["Content-Type"], "application/gzip");
  assert.equal(calls[0].url, "/api/v1/campaigns/import");
  await api.list(25);
  assert.equal(calls[1].url, "/api/v1/campaigns?limit=25&offset=25");
  await assert.rejects(api.upload({ size: MAX_CAMPAIGN_BYTES + 1 }), /16 MiB/);
  assert.equal(calls.length, 2, "invalid file must not reach the server");
});

test("API surfaces server diagnostics and supports authenticated binary download", async () => {
  const failed = createCampaignAPI(async () => new Response(JSON.stringify({ error: "invalid manifest" }), { status: 422 }));
  await assert.rejects(failed.get("id"), /invalid manifest/);
  const plainError = createCampaignAPI(async () => new Response("gateway unavailable", { status: 503 }));
  await assert.rejects(plainError.list(0), /HTTP 503/);
  let path;
  const success = createCampaignAPI(async url => { path = url; return new Response("archive"); });
  assert.equal(await (await success.download("a/b")).text(), "archive");
  assert.equal(path, "/api/v1/campaigns/a%2Fb/package");
});

test("procedure filtering is case insensitive across title, identity and reference", () => {
  const rows = [{ definition: { id: "display", title: "Status screen", procedure_ref: "patrol/display" } },
    { definition: { id: "record", title: "Recording", procedure_ref: "patrol/record" } }];
  assert.deepEqual(matchingScenarios(rows, " STATUS "), [rows[0]]);
  assert.deepEqual(matchingScenarios(rows, "patrol/record"), [rows[1]]);
  assert.deepEqual(matchingScenarios(rows, "none"), []);
});

test("procedure pages bound the visible list and search the full campaign", () => {
  const rows = Array.from({ length: 23 }, (_, i) => ({ definition: { id: `check-${i}`, title: `Check ${i}`, procedure_ref: `patrol/${i}` } }));
  assert.deepEqual(procedurePage(rows, "", 0), { rows: rows.slice(0, 10), total: 23 });
  assert.deepEqual(procedurePage(rows, "", 20), { rows: rows.slice(20), total: 23 });
  assert.deepEqual(procedurePage(rows, "patrol/22"), { rows: [rows[22]], total: 1 });
  const html = procedureListHTML([{ definition: { title: "<script>", procedure_ref: 'test/ref" autofocus' } }], null);
  assert.ok(!html.includes("<script>"));
  assert.ok(html.includes("&quot;"));
  assert.ok(html.includes('aria-pressed="false"'));
});

test("asset paths resolve relative to the description and only to valid CAS digests", () => {
  const digest = `sha256:${"a".repeat(64)}`;
  assert.equal(resolveAssetImages("![Setup](images/setup.png)", "scenarios/display/procedure.md", {
    "scenarios/display/images/setup.png": digest,
  }), `![Setup](/api/v1/blobs/${digest})`);
  assert.equal(resolveAssetImages("![Unknown](missing.png)", "procedure.md", {}), "![Unknown](missing.png)");
  assert.equal(resolveAssetImages("![Unsafe](image.png)", "procedure.md", { "image.png": "javascript:alert(1)" }), "![Unsafe](image.png)");
});

test("package labels and control defaults are escaped; preview never supplies live result controls", () => {
  const rows = [{ id: 'id" autofocus="x', digest: "sha256:test", imported_at: "2026-10-25T10:00:00Z", scenario_count: 1,
    manifest: { campaign: { id: "demo", name: '<img src=x onerror="alert(1)">' } } }];
  const list = packageListHTML(rows, "");
  assert.ok(!list.includes('<img src=x'));
  assert.ok(list.includes("&quot;"));
  const html = procedureHTML({ definition: { title: "Example", procedure_ref: "test/example", description: "procedure.md", defaults: {} },
    assets: {}, procedure: { blocks: [
      { kind: "field", field: { id: "note", type: "text", label: "Note", default: '\" onfocus=\"alert(1)' } },
      { kind: "check", check: { id: "ready", label: "Ready", behavior: "expect" } },
      { kind: "field", field: { id: "result", type: "choice", label: "Result", bind: "execution.result", options: ["PASS", "FAIL", "ERROR", "SKIPPED"] } },
      { kind: "markdown", markdown: '<script>alert(1)</script>' },
    ] } }, "scenario.yaml");
  assert.match(html, /Definition preview/);
  assert.ok(!html.includes("<script>"));
  assert.ok(!html.includes('value="" onfocus='));
  assert.equal((html.match(/<(?:input|select|textarea)[^>]* disabled/g) || []).length, 3);
  assert.ok(!html.includes("<option selected>PASS"));
  assert.ok(!html.includes("<option selected>FAIL"));
  assert.ok(!html.includes('name="result"'));
});
