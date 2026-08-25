// Unit tests for web/static/outbox.js, run with `node --test`.
//
// The queue holds evidence that exists nowhere else until it syncs, so the
// decisions worth testing are the ones that could lose a record or file it
// under the wrong name.

import test from "node:test";
import assert from "node:assert/strict";

import {
  BLOCKED, QUEUED,
  createOutbox, heldFrom, memoryStore, newEntry, sendableBy,
} from "../static/outbox.js";

const record = {
  repo: "org/firmware", branch: "main", rcs_ref: "abc123",
  procedure_ref: "manual/brake-test", evidence_type: "manual_test",
  source: "jdoe", result: "PASS", finished_at: "2026-08-25T10:00:00Z",
};

const at = iso => () => new Date(iso);

// --- What goes into the queue ---

test("the entry's key is the token the server dedupes on", () => {
  const entry = newEntry(record, { id: "9f1c8b3e-2d4a-4f6b-8c1d-0e2f3a4b5c6d" });

  assert.equal(entry.id, "9f1c8b3e-2d4a-4f6b-8c1d-0e2f3a4b5c6d");
  assert.equal(entry.record.client_record_id, entry.id,
    "the queue row and the submission are the same thing, so they share an identity");
});

test("a record keeps what it was given", () => {
  const entry = newEntry(record, { capturedBy: "jdoe", now: at("2026-08-25T11:00:00Z") });

  assert.equal(entry.record.finished_at, "2026-08-25T10:00:00Z",
    "when the test finished is not when it was queued");
  assert.equal(entry.capturedAt, "2026-08-25T11:00:00.000Z");
  assert.equal(entry.capturedBy, "jdoe");
  assert.equal(entry.state, QUEUED);
});

// --- Who may send it ---

test("a record is only sent by whoever wrote it", () => {
  const mine = newEntry(record, { capturedBy: "jdoe" });

  assert.equal(sendableBy(mine, "jdoe"), true);
  // The server would refuse this anyway for an ordinary human principal. A
  // sender holding source:any would not be refused, and would file one
  // tester's observations under another's name.
  assert.equal(sendableBy(mine, "asmith"), false);
  assert.match(heldFrom(mine, "asmith"), /written by jdoe/);
  assert.match(heldFrom(mine, "asmith"), /asmith/);
});

test("a session that does not know who it is holds nothing back", () => {
  const mine = newEntry(record, { capturedBy: "jdoe" });

  // Auth switched off, or a login that has expired. Refusing to send here
  // would be the page inventing a mismatch out of its own ignorance, and
  // telling a tester they are "signed in as nobody" about records they wrote.
  // The server decides this in the end: with no identity it takes the source
  // as sent, and with one it enforces it.
  assert.equal(sendableBy(mine, null), true);
  assert.equal(heldFrom(mine, null), null);
});

test("a record captured by nobody is sendable by anyone", () => {
  // A store running open in development has no identity to contradict.
  const anonymous = newEntry(record, { capturedBy: null });

  assert.equal(sendableBy(anonymous, "jdoe"), true);
  assert.equal(sendableBy(anonymous, null), true);
  assert.equal(heldFrom(anonymous, "jdoe"), null);
});

test("a blocked record waits for a person, not for a signal", () => {
  const blocked = { ...newEntry(record), state: BLOCKED, error: "rcs_ref is required" };

  assert.equal(sendableBy(blocked, "jdoe"), false);
  assert.equal(heldFrom(blocked, "jdoe"), "rcs_ref is required",
    "the server's own words: a tester fixing a record needs to know what was wrong");
});

// --- The queue ---

test("the queue drains oldest first", async () => {
  const outbox = createOutbox(memoryStore());
  await outbox.save(newEntry(record, { id: "b", now: at("2026-08-25T12:00:00Z") }));
  await outbox.save(newEntry(record, { id: "a", now: at("2026-08-25T09:00:00Z") }));
  await outbox.save(newEntry(record, { id: "c", now: at("2026-08-25T15:00:00Z") }));

  assert.deepEqual((await outbox.list()).map(e => e.id), ["a", "b", "c"],
    "the record a tester has carried longest is the one most worth getting in");
});

test("saving an edited record replaces it rather than filing a second copy", async () => {
  const outbox = createOutbox(memoryStore());
  const entry = newEntry(record, { id: "same" });
  await outbox.save(entry);

  await outbox.save({ ...entry, record: { ...entry.record, rcs_ref: "corrected" } });

  const all = await outbox.list();
  assert.equal(all.length, 1, "an edit is the same submission, not a new one");
  assert.equal(all[0].record.rcs_ref, "corrected");
});

test("settling removes a record; blocking keeps it", async () => {
  const outbox = createOutbox(memoryStore());
  await outbox.save(newEntry(record, { id: "filed" }));
  await outbox.save(newEntry(record, { id: "refused" }));

  await outbox.settle("filed");
  await outbox.block("refused", "branch is required");

  const all = await outbox.list();
  assert.equal(all.length, 1, "nothing leaves the queue until the store has said what became of it");
  assert.equal(all[0].state, BLOCKED);
  assert.equal(all[0].error, "branch is required");
  assert.equal(all[0].attempts, 1);
});

test("a failed attempt changes nothing about the record", async () => {
  const outbox = createOutbox(memoryStore());
  await outbox.save(newEntry(record, { id: "waiting" }));

  await outbox.recordAttempt("waiting");
  await outbox.recordAttempt("waiting");

  const [entry] = await outbox.list();
  assert.equal(entry.state, QUEUED, "the link failing says nothing about the record");
  assert.equal(entry.attempts, 2);
  assert.equal(entry.error, null);
});

test("a corrected record can go back in the queue", async () => {
  const outbox = createOutbox(memoryStore());
  await outbox.save(newEntry(record, { id: "fixable" }));
  await outbox.block("fixable", "evidence_type is invalid");

  const requeued = await outbox.unblock("fixable");

  assert.equal(requeued.state, QUEUED);
  assert.equal(requeued.error, null);
  assert.equal(sendableBy(requeued, null), true);
});

test("acting on a record that is no longer queued does not invent one", async () => {
  const outbox = createOutbox(memoryStore());

  assert.equal(await outbox.block("gone", "whatever"), null);
  assert.equal(await outbox.recordAttempt("gone"), null);
  assert.equal(await outbox.count(), 0);
});
