import test from "node:test";
import assert from "node:assert/strict";
import { campaignStore, createCampaignOffline } from "../static/campaignoffline.js";
import { digestOf } from "../static/blobref.js";

function memory() {
  const rows = new Map();
  return {
    async get(kind, id) { return structuredClone(rows.get(`${kind}:${id}`)); },
    async put(kind, id, value) { rows.set(`${kind}:${id}`, structuredClone(value)); },
    async remove(kind, id) { rows.delete(`${kind}:${id}`); },
    async all(kind) { return [...rows].filter(([key]) => key.startsWith(`${kind}:`)).map(([,value]) => structuredClone(value)); },
  };
}
async function fixture() {
  const store = memory(), sent = [], server = new Map(), calls = [];
  const bytes = new TextEncoder().encode("image").buffer, archive = new TextEncoder().encode("archive").buffer;
  const digest = await digestOf(bytes), packageDigest = await digestOf(archive);
  const execution = { id: "run", package_id: "pkg", name: "Run", repo: "repo", branch: "main", rcs_ref: "abc", procedures: [{ procedure_ref: "check" }] };
  const pkg = { id: "pkg", digest: packageDigest, definition: { scenarios: [{ definition: { procedure_ref: "check", defaults: { evidence_type: "manual_test" } }, assets: { "setup.png": digest }, procedure: { blocks: [{ field: { id: "input", default: 0 } }] } }] } };
  let who = "alice", fail = null, remoteStart = null;
  const request = async (path, method = "GET", data) => {
    calls.push({ path, method, data: structuredClone(data) });
    if (path === "/me") return { subject: who, authenticated: true };
    if (path === "/campaign-executions/run") return structuredClone(execution);
    if (path === "/campaigns/pkg") return structuredClone(pkg);
    if (path === "/campaign-attempts") {
      if (execution.closed_at && !server.has(data.id)) throw Object.assign(new Error("campaign changed or is closed"), { status: 409 });
      if (remoteStart) return structuredClone(remoteStart);
      if (!server.has(data.id)) server.set(data.id, { ...data, campaign_id: "pkg", owner: who, revision: 1, data: {}, created_at: new Date().toISOString() });
      if (fail === "create") { fail = null; throw new TypeError("lost response"); }
      return structuredClone(server.get(data.id));
    }
    const id = path.split("/")[2], row = server.get(id);
    if (method === "GET") {
      if (!row) throw Object.assign(new Error("attempt or campaign not found"), { status: 404 });
      return structuredClone(row);
    }
    if (path.endsWith("/finalize")) {
      if (!row.finalized_at) { assert.equal(data.revision, row.revision); row.finalized_at = "now"; row.evidence_id = "evidence"; }
      if (fail === "finalize") { fail = null; throw new TypeError("lost response"); }
      return structuredClone(row);
    }
    if (row.last_operation !== data.operation_id) {
      if (data.revision !== row.revision || row.finalized_at) throw Object.assign(new Error("revision conflict"), { status: 409 });
      row.data = structuredClone(data.data); row.last_operation = data.operation_id; row.revision++;
    }
    sent.push(structuredClone(data));
    if (fail === "save") { fail = null; throw new TypeError("lost response"); }
    return structuredClone(row);
  };
  const options = { me: { subject: "alice" }, store, request, lock: fn => fn(),
    fetchBlob: async () => new Response(bytes), fetchArchive: async () => new Response(archive),
    uploadBlob: async blob => { calls.push({ path: "upload", digest: blob.digest }); return { ok: true }; } };
  const api = createCampaignOffline(options);
  return { api, options, store, server, calls, sent, bytes, digest, execution, pkg,
    who: value => { who = value; }, fail: value => { fail = value; }, remote: value => { remoteStart = value; } };
}

test("preparation verifies every digest and only marks a complete campaign ready", async () => {
  const f = await fixture();
  const broken = createCampaignOffline({ ...f.options, fetchBlob: async () => new Response("wrong bytes") });
  await assert.rejects(broken.prepare("run"), /digest mismatch/);
  assert.equal(await broken.prepared("run"), undefined);
  await f.api.prepare("run");
  assert.equal((await f.api.prepared("run")).pkg.digest, f.pkg.digest);
  assert.deepEqual((await f.api.getBlob(f.digest)).bytes, f.bytes);
});

test("unfinished local attempts and attachments survive reopening; uploads precede saves and finalization", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check");
  assert.equal(row.data.answers.input, 0);
  row = await f.api.save(row, { ...row.data, notes: "field notes", metadata: { observations: `![image](/api/v1/blobs/${f.digest}.png)` } });
  const reopened = createCampaignOffline(f.options);
  assert.equal((await reopened.get(row.id)).data.notes, "field notes");
  row = await reopened.complete(await reopened.get(row.id), "2026-11-04T12:00:00Z");
  assert.equal(row.local_complete, true); assert.equal(row.evidence_id, undefined);
  await reopened.sync();
  const result = await reopened.get(row.id);
  assert.equal(result.evidence_id, "evidence"); assert.equal(result.local_pending, false);
  const upload = f.calls.findIndex(c => c.path === "upload"), save = f.calls.findIndex(c => c.method === "PUT"), finalize = f.calls.findIndex(c => c.path.endsWith("/finalize"));
  assert.ok(upload < save && save < finalize);
});

test("lost save responses replay the same operation before later edits", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check");
  row = await f.api.save(row, { ...row.data, notes: "first" });
  f.fail("save"); await assert.rejects(f.api.sync(), /lost response/);
  row = await f.api.save(await f.api.get(row.id), { ...row.data, notes: "second" });
  await createCampaignOffline(f.options).sync();
  assert.deepEqual(f.sent[0], f.sent[1]);
  assert.equal(f.sent[2].data.notes, "second");
  assert.equal(f.server.get(row.id).revision, 3);
});

test("lost finalization responses preserve the exact revision and finish time across reload", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check");
  row = await f.api.save(row, row.data);
  await f.api.complete(row, "2026-11-04T12:00:00Z");
  f.fail("finalize"); await assert.rejects(f.api.sync(), /lost response/);
  await createCampaignOffline(f.options).sync();
  const finals = f.calls.filter(c => c.path.endsWith("/finalize"));
  assert.deepEqual(finals[0].data, finals[1].data);
  assert.equal((await f.api.get(row.id)).local_pending, false);
});

test("a different reconnecting principal sends no writes", async () => {
  const f = await fixture(); await f.api.prepare("run");
  const row = await f.api.start("run", "check");
  f.who("bob"); await assert.rejects(f.api.sync(), /Sign in as alice/);
  assert.equal(f.calls.filter(c => c.method === "POST").length, 0);
  assert.equal((await f.api.get(row.id)).local_pending, true);
});

test("conflicting edits stop retries, retain both versions and require explicit resolution", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check"); await f.api.sync(); row = await f.api.get(row.id);
  row = await f.api.save(row, { ...row.data, notes: "local" });
  const server = f.server.get(row.id); server.revision++; server.data = { notes: "remote" };
  await f.api.sync();
  let conflict = await f.api.get(row.id);
  assert.match(conflict.local_error, /conflict/);
  assert.equal(conflict.data.notes, "local"); assert.equal(conflict.local_conflict.data.notes, "remote");
  const count = f.calls.length; await f.api.sync(); assert.equal(f.calls.length, count + 1);
  await f.api.resolve(row.id, "local"); await f.api.sync();
  assert.equal(server.data.notes, "local");
  assert.equal((await f.store.all("archive")).length, 1);
});

test("a concurrent start is a conflict rather than silently overwriting a colleague's draft", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check"); row = await f.api.save(row, { ...row.data, notes: "mine" });
  const remote = { ...row, id: "colleague", data: { notes: "theirs" }, revision: 2 };
  f.remote(remote); f.server.set(remote.id, remote);
  await f.api.sync();
  assert.equal((await f.api.get(row.id)).local_conflict.id, "colleague");
  assert.equal(f.sent.length, 0);
  const resolved = await f.api.resolve(row.id, "remote");
  assert.equal(resolved.id, "colleague"); assert.equal(await f.api.get(row.id), undefined);
});

test("finalized remote work cannot be overwritten and stale local tabs cannot replace newer drafts", async () => {
  const f = await fixture(); await f.api.prepare("run");
  const original = await f.api.start("run", "check");
  const newer = await f.api.save(original, { ...original.data, notes: "newer" });
  await assert.rejects(f.api.save(original, { ...original.data, notes: "stale" }), /another tab/);
  await f.api.sync();
  const row = await f.api.save(await f.api.get(newer.id), { ...newer.data, notes: "local" });
  f.server.get(row.id).finalized_at = "now";
  await f.api.sync();
  await assert.rejects(f.api.resolve(row.id, "local"), /finalized/);
  assert.equal((await f.api.get(row.id)).data.notes, "local");
});

test("a failed disk write is never acknowledged as saved", async () => {
  const f = await fixture(); await f.api.prepare("run");
  const row = await f.api.start("run", "check");
  const broken = createCampaignOffline({ ...f.options, store: { ...f.store, put: async () => { throw new Error("quota exceeded"); } } });
  await assert.rejects(broken.save(row, { ...row.data, notes: "not persisted" }), /quota exceeded/);
  assert.equal((await f.api.get(row.id)).data.notes, "");
});

test("refreshing a server revision invalidates an older local editor", async () => {
  const f = await fixture(); await f.api.prepare("run");
  const row = await f.api.start("run", "check"); await f.api.sync();
  const before = await f.api.get(row.id);
  const remote = { ...f.server.get(row.id), revision: 9, data: { notes: "colleague" } };
  await f.api.remember(remote);
  await assert.rejects(f.api.save(before, { notes: "stale" }), /another tab/);
  assert.equal((await f.api.get(row.id)).data.notes, "colleague");
});

test("a lost creation response replays the UUID and preserves queued answers", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check");
  row = await f.api.save(row, { ...row.data, notes: "offline answers" });
  f.fail("create"); await assert.rejects(f.api.sync(), /lost response/);
  await createCampaignOffline(f.options).sync();
  const creates = f.calls.filter(c => c.path === "/campaign-attempts");
  assert.deepEqual(creates[0].data, creates[1].data);
  assert.equal(f.server.size, 1);
  assert.equal(f.server.get(row.id).data.notes, "offline answers");
  assert.equal((await f.api.get(row.id)).local_pending, false);
});

for (const finalized of [false, true]) test(`replayed creation detects intervening ${finalized ? "finalization" : "edits"} before uploading answers`, async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check");
  row = await f.api.save(row, { ...row.data, notes: "offline answers" });
  f.fail("create"); await assert.rejects(f.api.sync(), /lost response/);
  const remote = f.server.get(row.id);
  remote.revision++; remote.data = { notes: "colleague's answers" };
  if (finalized) remote.finalized_at = "now";
  await createCampaignOffline(f.options).sync();
  const conflict = await f.api.get(row.id);
  assert.equal(remote.data.notes, "colleague's answers");
  assert.equal(f.sent.length, 0);
  assert.equal(conflict.data.notes, "offline answers");
  assert.equal(conflict.local_conflict.data.notes, "colleague's answers");
  assert.ok(conflict.local_error);
  assert.equal(conflict.local_create_failed, false, "a recovered server row uses normal conflict resolution");
  if (finalized) await assert.rejects(f.api.resolve(row.id, "local"), /finalized/);
  else {
    await f.api.resolve(row.id, "local"); await f.api.sync();
    assert.equal(remote.data.notes, "offline answers");
  }
});

test("creation refused by a closed campaign can be explicitly retried after reopening", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check");
  row = await f.api.save(row, { ...row.data, notes: "finished offline" });
  await f.api.complete(row, "2026-11-04T12:00:00Z");
  f.execution.closed_at = "now";
  await f.api.sync();
  assert.equal((await f.api.get(row.id)).local_create_failed, true);
  const count = f.calls.length;
  const reloaded = await f.api.reloadConflict(row.id);
  assert.match(reloaded.local_error, /Campaign is closed/);
  assert.deepEqual(f.calls.slice(count).map(c => c.path), ["/me", "/campaign-executions/run"]);
  await assert.rejects(f.api.retryCreate(row.id), /still closed/);
  f.execution.closed_at = null;
  await f.api.sync();
  assert.equal(f.server.has(row.id), false, "reopening does not silently clear the refusal");
  assert.match((await f.api.reloadConflict(row.id)).local_error, /Campaign is open/);
  const retried = await createCampaignOffline(f.options).retryCreate(row.id);
  assert.equal(retried.id, row.id);
  assert.equal(retried.local_pending, false);
  assert.equal(retried.data.notes, "finished offline");
  assert.equal(retried.evidence_id, "evidence");
  const creates = f.calls.filter(c => c.path === "/campaign-attempts");
  assert.deepEqual(creates[0].data, creates[1].data);
  assert.equal(f.calls.find(c => c.path.endsWith("/finalize")).data.finished_at, "2026-11-04T12:00:00Z");
});

test("retrying a refused creation still detects a colleague's concurrent start", async () => {
  const f = await fixture(); await f.api.prepare("run");
  const row = await f.api.start("run", "check");
  f.execution.closed_at = "now"; await f.api.sync();
  f.execution.closed_at = null;
  const remote = { ...row, id: "colleague", revision: 1, data: { notes: "theirs" } };
  f.remote(remote); f.server.set(remote.id, remote);
  const conflict = await f.api.retryCreate(row.id);
  assert.equal(conflict.local_conflict.id, "colleague");
  assert.equal(conflict.local_create_failed, false);
  assert.equal(f.sent.length, 0);
});

test("archiving a refused creation retains answers and attachments without needing a server row", async () => {
  const f = await fixture(); await f.api.prepare("run");
  let row = await f.api.start("run", "check");
  row = await f.api.save(row, { ...row.data, notes: "keep me", metadata: { observations: `![photo](/api/v1/blobs/${f.digest}.png)` } });
  f.execution.closed_at = "now"; await f.api.sync();
  const count = f.calls.length;
  const broken = createCampaignOffline({ ...f.options, store: { ...f.store, put: async () => { throw new Error("quota exceeded"); } } });
  await assert.rejects(broken.archiveFailedCreate(row.id), /quota exceeded/);
  assert.equal((await f.api.get(row.id)).local_pending, true);
  await f.api.archiveFailedCreate(row.id);
  assert.equal(f.calls.length, count, "archiving is a local recovery operation");
  assert.equal(await f.api.get(row.id), undefined);
  assert.deepEqual(await f.api.attempts("run"), []);
  assert.equal((await f.store.all("archive"))[0].operations.at(-1).data.notes, "keep me");
  assert.deepEqual((await f.api.getBlob(f.digest)).bytes, f.bytes);
  await assert.rejects(f.api.save(row, row.data), /changed in another tab/);
});

test("online unprepared campaigns work without Web Locks", async t => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: {} });
  t.after(() => descriptor ? Object.defineProperty(globalThis, "navigator", descriptor) : delete globalThis.navigator);
  const f = await fixture();
  const api = createCampaignOffline({ ...f.options, lock: undefined });
  assert.equal(await api.prepared("run"), undefined);
  assert.deepEqual(await api.attempts("run"), []);
  assert.equal(await api.get("unknown"), undefined);
  await api.updateExecution(f.execution);
  await api.sync();
  await assert.rejects(api.prepare("run"), /Web Locks/);
  assert.equal(f.calls.length, 0);
});

for (const disabled of ["missing", "denied"]) test(`online campaigns work with ${disabled} IndexedDB`, async () => {
  const f = await fixture();
  const store = campaignStore("alice", disabled === "missing" ? null : { open() { throw new Error("SecurityError"); } });
  const api = createCampaignOffline({ ...f.options, store });
  assert.equal(await api.available(), false);
  assert.equal(await api.prepared("run"), undefined);
  assert.deepEqual(await api.campaigns(), []);
  assert.deepEqual(await api.attempts("run"), []);
  await api.updateExecution(f.execution); await api.sync();
  await api.uploadAttachments({ notes: "online draft" });
  await assert.rejects(api.prepare("run"), /Offline storage is unavailable/);
  assert.deepEqual(f.calls.map(c => c.path), ["/me"]);
});

test("campaign-wide attempt lookup reads the journal once and excludes other executions", async () => {
  const f = await fixture(); await f.api.prepare("run");
  const row = await f.api.start("run", "check");
  await f.store.put("attempt", "other", { row: { ...row, id: "other", execution_id: "other-run" }, operations: [], version: 0 });
  const kinds = [];
  const api = createCampaignOffline({ ...f.options, store: { ...f.store, all: async kind => { kinds.push(kind); return f.store.all(kind); } } });
  assert.deepEqual((await api.attempts("run")).map(a => a.id), [row.id]);
  assert.deepEqual(kinds, ["attempt"]);
});
