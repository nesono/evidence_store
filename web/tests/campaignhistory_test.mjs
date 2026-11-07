import test from "node:test";
import assert from "node:assert/strict";
import { attemptHistory } from "../static/campaigndrafts.js";

test("prepared campaigns retain paginated online history and pin pending work without duplicate IDs", async () => {
  const server = Array.from({ length: 60 }, (_, i) => ({ id: `server-${i}`, data: { notes: `server ${i}` } }));
  const local = [
    { ...server[0], data: { notes: "stale snapshot" } },
    { ...server[27], data: { notes: "unsynchronized edit" }, local_pending: true },
    { id: "new-local", local_pending: true },
  ];
  const queries = [];
  const options = {
    executionID: "run", ref: "check",
    offline: { prepared: async () => ({}), attempts: async () => local },
    request: async path => {
      const query = new URL(path, "https://example.test").searchParams;
      queries.push(query);
      const offset = Number(query.get("offset"));
      return { attempts: server.slice(offset, offset + 25), has_more: offset + 25 < server.length };
    },
  };
  const pages = await Promise.all([0, 25, 50].map(offset => attemptHistory({ ...options, offset })));
  const all = pages.flatMap(p => p.attempts);
  assert.equal(all.length, 61);
  assert.equal(new Set(all.map(a => a.id)).size, 61);
  assert.equal(all.find(a => a.id === "server-0").data.notes, "server 0");
  assert.equal(pages[0].attempts[0].data.notes, "unsynchronized edit");
  assert.deepEqual(pages.map(p => p.has_more), [true, true, false]);
  assert.deepEqual(queries.map(q => q.get("offset")), ["0", "25", "50"]);
  assert.ok(queries.every(q => q.get("execution_id") === "run" && q.get("procedure_ref") === "check"));
});

test("transport failures use explicitly partial prepared history", async () => {
  const local = Array.from({ length: 30 }, (_, i) => ({ id: `local-${i}` }));
  const options = {
    executionID: "run", ref: "check", offset: 25,
    offline: { prepared: async () => ({}), attempts: async () => local },
    request: async () => { throw new TypeError("offline"); },
  };
  assert.deepEqual(await attemptHistory(options), { attempts: local.slice(25), has_more: false, local: true });
  for (const status of [401, 403, 404, 500]) {
    await assert.rejects(attemptHistory({ ...options, request: async () => { throw Object.assign(new Error("server refusal"), { status }); } }), /server refusal/);
  }
});

test("unprepared and standalone histories use the server without requiring local storage", async () => {
  const expected = { attempts: [{ id: "online" }], has_more: false };
  const options = { packageID: "pkg", ref: "check", request: async () => expected };
  assert.deepEqual(await attemptHistory(options), expected);
  assert.deepEqual(await attemptHistory({ ...options, executionID: "run", offline: {
    prepared: async () => undefined, attempts: () => { throw new Error("must not read offline attempts"); },
  } }), expected);
  await assert.rejects(attemptHistory({ ...options, request: async () => { throw new TypeError("offline"); } }), /offline/);
});
