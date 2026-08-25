// Unit tests for web/static/sync.js, run with `node --test`.
//
// Every case here is a way a sync can go wrong on a bad link, and what the
// queue is left holding afterwards. The rule the tests are really checking is
// one: nothing leaves the queue until the store has said what became of it.

import test from "node:test";
import assert from "node:assert/strict";

import { BLOCKED, QUEUED, createOutbox, memoryStore, newEntry } from "../static/outbox.js";
import { describeSync, syncOutbox } from "../static/sync.js";

const record = {
  repo: "org/firmware", branch: "main", rcs_ref: "abc123",
  procedure_ref: "manual/brake-test", evidence_type: "manual_test",
  source: "jdoe", result: "PASS", finished_at: "2026-08-25T10:00:00Z",
};

async function outboxWith(...entries) {
  const outbox = createOutbox(memoryStore());
  for (const entry of entries) await outbox.save(entry);
  return outbox;
}

// A fake server. `reply` decides what comes back for each batch it receives.
function fakePost(reply) {
  const sent = [];
  const post = async (path, payload) => {
    sent.push({ path, payload });
    const answer = reply(payload, sent.length);
    if (answer instanceof Error) throw answer;
    return answer;
  };
  post.sent = sent;
  return post;
}

const allCreated = payload => ({
  ok: true, status: 201,
  data: { results: payload.records.map((_, index) => ({ index, id: `id-${index}`, status: "created" })) },
});

// --- The ordinary case ---

test("a queue that goes through empties", async () => {
  const outbox = await outboxWith(
    newEntry(record, { id: "a", capturedBy: "jdoe" }),
    newEntry(record, { id: "b", capturedBy: "jdoe" }),
  );
  const post = fakePost(allCreated);

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(summary.filed, 2);
  assert.equal(await outbox.count(), 0);
  assert.equal(post.sent[0].path, "/evidence/batch");
  assert.equal(post.sent[0].payload.records[0].client_record_id, "a",
    "the token goes with the record; it is what makes a resend safe");
});

// --- The case the token exists for ---

test("a record the store already has leaves the queue", async () => {
  const outbox = await outboxWith(newEntry(record, { id: "sent-before", capturedBy: "jdoe" }));
  const post = fakePost(() => ({
    ok: true, status: 201,
    data: { results: [{ index: 0, id: "existing", status: "duplicate" }] },
  }));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  // An earlier attempt got through and its response did not. That is exactly
  // what the token is for, and the record is filed — so it goes.
  assert.equal(summary.duplicate, 1);
  assert.equal(summary.filed, 0);
  assert.equal(await outbox.count(), 0);
  assert.match(describeSync(summary), /already filed/);
});

// --- Ways it fails ---

test("a link that drops keeps everything", async () => {
  const outbox = await outboxWith(
    newEntry(record, { id: "a", capturedBy: "jdoe" }),
    newEntry(record, { id: "b", capturedBy: "jdoe" }),
  );
  const post = fakePost(() => new Error("Failed to fetch"));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(summary.retryable, 2);
  assert.equal(summary.filed, 0);
  assert.equal(await outbox.count(), 2, "the link failing says nothing about the records");
  assert.equal((await outbox.list())[0].state, QUEUED);
});

test("a store having a bad day keeps everything", async () => {
  const outbox = await outboxWith(newEntry(record, { id: "a", capturedBy: "jdoe" }));
  const post = fakePost(() => ({ ok: false, status: 503, data: { error: "database unavailable" } }));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(summary.retryable, 1);
  assert.equal(await outbox.count(), 1);
  assert.equal((await outbox.list())[0].state, QUEUED,
    "a 5xx is not something a record can fix by being different");
});

test("an expired session drops nothing and stops trying", async () => {
  const outbox = await outboxWith(
    newEntry(record, { id: "a", capturedBy: "jdoe" }),
    newEntry(record, { id: "b", capturedBy: "jdoe" }),
  );
  const post = fakePost(() => ({ ok: false, status: 401, data: { error: "unauthorized" } }));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  // A twelve-hour session against a three-day campaign makes this the normal
  // path, not the exception.
  assert.equal(summary.authRequired, true);
  assert.equal(await outbox.count(), 2);
  assert.equal(post.sent.length, 1, "no point sending the rest at a session that has gone");
  assert.match(describeSync(summary), /Sign in/);
});

test("a record the store refuses is kept and flagged, not retried", async () => {
  const outbox = await outboxWith(newEntry(record, { id: "bad", capturedBy: "jdoe" }));
  const post = fakePost(() => ({
    ok: true, status: 207,
    data: { results: [{ index: 0, status: "error", error: "rcs_ref is required" }] },
  }));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(summary.blocked, 1);
  const [entry] = await outbox.list();
  assert.equal(entry.state, BLOCKED);
  assert.equal(entry.error, "rcs_ref is required", "the server's own words, so a tester can fix it");

  // And a second sync does not send it again: the answer will not change.
  const second = fakePost(allCreated);
  const again = await syncOutbox({ outbox, post: second, subject: "jdoe" });
  assert.equal(second.sent.length, 0);
  assert.equal(again.heldBack, 1);
});

test("a batch refused whole flags every record in it", async () => {
  const outbox = await outboxWith(
    newEntry(record, { id: "a", capturedBy: "jdoe" }),
    newEntry(record, { id: "b", capturedBy: "jdoe" }),
  );
  const post = fakePost(() => ({
    ok: false, status: 403,
    data: { error: "record 0: source does not match the caller's identity" },
  }));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(summary.blocked, 2);
  assert.equal(await outbox.count(), 2);
  for (const entry of await outbox.list()) {
    assert.equal(entry.state, BLOCKED);
    assert.match(entry.error, /source does not match/);
  }
});

test("a mixed answer disposes of each record on its own terms", async () => {
  const outbox = await outboxWith(
    newEntry(record, { id: "good", capturedBy: "jdoe" }),
    newEntry(record, { id: "seen", capturedBy: "jdoe" }),
    newEntry(record, { id: "bad", capturedBy: "jdoe" }),
  );
  const post = fakePost(() => ({
    ok: true, status: 207,
    data: { results: [
      { index: 0, id: "x", status: "created" },
      { index: 1, id: "y", status: "duplicate" },
      { index: 2, status: "error", error: "branch is required" },
    ] },
  }));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.deepEqual(
    { filed: summary.filed, duplicate: summary.duplicate, blocked: summary.blocked },
    { filed: 1, duplicate: 1, blocked: 1 },
  );
  assert.deepEqual((await outbox.list()).map(e => e.id), ["bad"]);
});

// --- Whose records these are ---

test("somebody else's records are not sent under this name", async () => {
  const outbox = await outboxWith(
    newEntry(record, { id: "mine", capturedBy: "asmith" }),
    newEntry(record, { id: "theirs", capturedBy: "jdoe" }),
  );
  const post = fakePost(allCreated);

  const summary = await syncOutbox({ outbox, post, subject: "asmith" });

  assert.equal(summary.filed, 1);
  assert.equal(summary.heldBack, 1);
  assert.deepEqual(post.sent[0].payload.records.map(r => r.client_record_id), ["mine"]);
  assert.deepEqual((await outbox.list()).map(e => e.id), ["theirs"],
    "jdoe's record waits for jdoe rather than being filed under asmith's name");
});

// --- Chunking ---

test("a long queue banks what it managed to send before the link died", async () => {
  const entries = Array.from({ length: 120 }, (_, i) =>
    newEntry(record, { id: `r${String(i).padStart(3, "0")}`, capturedBy: "jdoe" }));
  const outbox = await outboxWith(...entries);

  // The first chunk lands, the second never arrives.
  const post = fakePost((payload, call) => (call === 1 ? allCreated(payload) : new Error("Failed to fetch")));

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(summary.filed, 50);
  assert.equal(summary.retryable, 50);
  assert.equal(post.sent.length, 2, "stop once the link has gone rather than running the rest into it");
  assert.equal(await outbox.count(), 70, "what went through is gone; the rest is still here");
});

test("an empty queue asks the server nothing", async () => {
  const outbox = createOutbox(memoryStore());
  const post = fakePost(allCreated);

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(post.sent.length, 0);
  assert.equal(describeSync(summary), "Nothing waiting to send.");
});
