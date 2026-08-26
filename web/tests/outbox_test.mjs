// Unit tests for web/static/outbox.js, run with `node --test`.
//
// The queue holds evidence that exists nowhere else until it syncs, so the
// decisions worth testing are the ones that could lose a record or file it
// under the wrong name.

import test from "node:test";
import assert from "node:assert/strict";

import {
  BLOCKED, QUEUED, STALE_WARN_DAYS,
  ageInDays, assessDurability, createOutbox, heldFrom, memoryStore, newEntry,
  roomIsTight, sendableBy, staleness,
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

// --- Photos ---
//
// A queued photo is the only copy of itself. The sweep decides when that stops
// being true, and getting it wrong either fills a phone or loses a photograph.

const DIGEST_A = "sha256:" + "a".repeat(64);
const DIGEST_B = "sha256:" + "b".repeat(64);

const withPhoto = digest => ({
  ...record,
  metadata: { observations: `see ![shot](/api/v1/blobs/${digest}.png)` },
});

async function stash(outbox, digest, when) {
  return outbox.stashBlob({
    digest, ext: "png", contentType: "image/png",
    bytes: new ArrayBuffer(64), now: at(when),
  });
}

test("the same photo attached twice is stashed once", async () => {
  const outbox = createOutbox(memoryStore());
  await stash(outbox, DIGEST_A, "2026-08-25T10:00:00Z");
  await stash(outbox, DIGEST_A, "2026-08-25T11:00:00Z");

  const blobs = await outbox.blobs();
  assert.equal(blobs.length, 1, "content addressing means one image is one object here too");
  assert.equal(blobs[0].stashedAt, "2026-08-25T10:00:00.000Z", "the first stash is not overwritten");
});

test("a photo a queued record needs is never swept", async () => {
  const outbox = createOutbox(memoryStore());
  await outbox.save(newEntry(withPhoto(DIGEST_A), { id: "waiting" }));
  await stash(outbox, DIGEST_A, "2026-01-01T00:00:00Z");   // long past any grace

  const deleted = await outbox.sweepBlobs({ now: at("2026-08-25T10:00:00Z") });

  assert.equal(deleted, 0);
  assert.equal((await outbox.blobs()).length, 1, "its record has not been filed yet");
});

test("a photo pasted into a log still being written is spared", async () => {
  const outbox = createOutbox(memoryStore());
  // Nothing references it: the tester has not pressed Create yet.
  await stash(outbox, DIGEST_A, "2026-08-25T09:59:00Z");

  const deleted = await outbox.sweepBlobs({ now: at("2026-08-25T10:00:00Z") });

  assert.equal(deleted, 0, "deleting this would delete the only copy of a photograph");
});

test("a photo the store has is swept as soon as its record is filed", async () => {
  const outbox = createOutbox(memoryStore());
  await stash(outbox, DIGEST_A, "2026-08-25T09:59:00Z");
  await outbox.markBlobUploaded(DIGEST_A, at("2026-08-25T10:00:00Z"));

  // A minute old, so the grace period would otherwise keep it for a day. On a
  // campaign that is the difference between a few megabytes and all of them.
  const deleted = await outbox.sweepBlobs({ now: at("2026-08-25T10:00:30Z") });

  assert.equal(deleted, 1);
  assert.equal((await outbox.blobs()).length, 0);
});

test("an abandoned photo goes once the grace period is up", async () => {
  const outbox = createOutbox(memoryStore());
  await stash(outbox, DIGEST_A, "2026-08-24T09:00:00Z");

  const deleted = await outbox.sweepBlobs({ now: at("2026-08-25T10:00:00Z") });

  assert.equal(deleted, 1, "a log that was never filed does not keep its photographs forever");
});

test("the queue reports how much of the device it is using", async () => {
  const outbox = createOutbox(memoryStore());
  await outbox.stashBlob({ digest: DIGEST_A, ext: "png", contentType: "image/png", bytes: new ArrayBuffer(1500) });
  await outbox.stashBlob({ digest: DIGEST_B, ext: "png", contentType: "image/png", bytes: new ArrayBuffer(2500) });

  assert.equal(await outbox.bytesHeld(), 4000);
});

// --- How long something has been waiting ---
//
// A record that sits unsent is the failure this whole feature can produce, so
// the queue has to say so while the evidence is still there to save. Nothing
// here expires or refuses a record: one queued in March is still a true account
// of a test that happened in March.

const NOW = new Date("2026-08-25T12:00:00Z");
const written = iso => ({ ...newEntry(record), capturedAt: iso });

test("a record written today is not nagged about", () => {
  const entries = [written("2026-08-25T09:00:00Z"), written("2026-08-23T09:00:00Z")];
  assert.equal(staleness(entries, NOW).level, "none");
});

test("a week waiting is worth saying", () => {
  // Seven days is Safari's eviction horizon for a site nobody has visited, so
  // the warning arrives while the records still exist.
  const result = staleness([written("2026-08-18T11:00:00Z")], NOW);
  assert.equal(result.level, "warn");
  assert.equal(result.days, STALE_WARN_DAYS);
});

test("a month waiting is worth insisting on", () => {
  const result = staleness([written("2026-07-01T12:00:00Z")], NOW);
  assert.equal(result.level, "urgent");
  assert.equal(result.days, 55);
});

test("the oldest record is the one that speaks", () => {
  const entries = [written("2026-08-25T11:00:00Z"), written("2026-07-01T12:00:00Z")];
  assert.equal(staleness(entries, NOW).level, "urgent");
});

test("an empty queue says nothing", () => {
  assert.deepEqual(staleness([], NOW), { level: "none", days: 0 });
});

test("a record with an unreadable date is not called old", () => {
  // Better to under-warn than to tell somebody their evidence is rotting
  // because of a parse failure.
  assert.equal(ageInDays({ capturedAt: "not a date" }, NOW), 0);
});

// --- What the browser has promised ---

test("no disk storage at all is the strongest answer", async () => {
  const assessment = await assessDurability(memoryStore(), null);
  assert.equal(assessment.level, "session",
    "a queue that lasts as long as the tab is not somewhere to leave evidence");
});

test("a browser that promises to keep it says so", async () => {
  const storage = { persisted: async () => true, estimate: async () => ({ quota: 100, usage: 1 }) };
  const assessment = await assessDurability({ durable: true }, storage);

  assert.equal(assessment.level, "durable");
  assert.equal(assessment.persisted, true);
});

test("permission is asked for, not just checked", async () => {
  let asked = false;
  const storage = {
    persisted: async () => false,
    persist: async () => { asked = true; return true; },
    estimate: async () => ({}),
  };

  const assessment = await assessDurability({ durable: true }, storage);

  assert.equal(asked, true, "asking is free and usually granted for a site in use");
  assert.equal(assessment.level, "durable");
});

test("a refusal is reported rather than assumed away", async () => {
  // A private window refuses. So does a browser about to reclaim space. Both
  // are worth a tester knowing, and neither needs fingerprinting to detect —
  // the Storage API answers the question directly.
  const storage = { persisted: async () => false, persist: async () => false, estimate: async () => ({}) };
  const assessment = await assessDurability({ durable: true }, storage);

  assert.equal(assessment.level, "evictable");
});

test("a browser that will not discuss it has promised nothing", async () => {
  const storage = { persisted: async () => { throw new Error("denied"); } };
  const assessment = await assessDurability({ durable: true }, storage);

  assert.equal(assessment.level, "evictable");
});

test("a device running out of room is worth mentioning", () => {
  assert.equal(roomIsTight({ quota: 1000, usage: 850 }), true);
  assert.equal(roomIsTight({ quota: 1000, usage: 200 }), false);
  // Nothing to say when the browser did not tell us.
  assert.equal(roomIsTight({}), false);
  assert.equal(roomIsTight(), false);
});
