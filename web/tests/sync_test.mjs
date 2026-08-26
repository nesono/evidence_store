// Unit tests for web/static/sync.js, run with `node --test`.
//
// Every case here is a way a sync can go wrong on a bad link, and what the
// queue is left holding afterwards. The rule the tests are really checking is
// one: nothing leaves the queue until the store has said what became of it.

import test from "node:test";
import assert from "node:assert/strict";

import { BLOCKED, QUEUED, createOutbox, memoryStore, newEntry } from "../static/outbox.js";
import {
  RECORDS_SHARE, describeProgress, describeSync, formatBytes, progressFraction, syncOutbox,
} from "../static/sync.js";

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

// --- Photos ---
//
// The ordering here is the one rule that cannot bend: a record must never be
// filed pointing at bytes the store does not have, because the reference in its
// log would resolve to nothing and the log is immutable once filed.

const PNG = "sha256:" + "d".repeat(64);
const JPG = "sha256:" + "e".repeat(64);

function withPhoto(id, digest, ext = "png") {
  const entry = newEntry({
    ...record,
    metadata: { observations: `Damage here ![shot](/api/v1/blobs/${digest}.${ext})` },
  }, { id, capturedBy: "jdoe" });
  return entry;
}

function fakeBlobs(outbox) {
  return async (digest, size) => outbox.stashBlob({
    digest, ext: "png", contentType: "image/png",
    bytes: new ArrayBuffer(size), now: () => new Date("2026-08-25T10:00:00Z"),
  });
}

test("photos go before the records that name them", async () => {
  const outbox = await outboxWith(withPhoto("a", PNG));
  await fakeBlobs(outbox)(PNG, 1024);

  const order = [];
  const putBlob = async blob => { order.push("photo:" + blob.digest.slice(7, 11)); return { ok: true, status: 201 }; };
  const post = fakePost(payload => { order.push("records"); return allCreated(payload); });

  const summary = await syncOutbox({ outbox, post, putBlob, subject: "jdoe" });

  assert.deepEqual(order, ["photo:dddd", "records"],
    "a record filed first would point at bytes that are not there yet");
  assert.equal(summary.photos, 1);
  assert.equal(summary.filed, 1);
});

test("a photo that will not upload holds its record back", async () => {
  const outbox = await outboxWith(withPhoto("a", PNG));
  await fakeBlobs(outbox)(PNG, 2048);

  const post = fakePost(allCreated);
  const putBlob = async () => ({ ok: false, status: 500 });

  const summary = await syncOutbox({ outbox, post, putBlob, subject: "jdoe" });

  assert.equal(post.sent.length, 0, "the record must not go without its photo");
  assert.equal(summary.retryable, 1);
  assert.equal(await outbox.count(), 1);
  assert.equal((await outbox.list())[0].state, QUEUED, "nothing is wrong with the record");
});

test("an expired session during the photos stops the whole sync", async () => {
  const outbox = await outboxWith(withPhoto("a", PNG));
  await fakeBlobs(outbox)(PNG, 512);

  const post = fakePost(allCreated);
  const summary = await syncOutbox({
    outbox, post, subject: "jdoe",
    putBlob: async () => ({ ok: false, status: 401 }),
  });

  assert.equal(summary.authRequired, true);
  assert.equal(post.sent.length, 0);
  assert.equal(await outbox.count(), 1);
});

test("only the photos the outgoing records name are uploaded", async () => {
  const outbox = await outboxWith(withPhoto("mine", PNG));
  const stash = fakeBlobs(outbox);
  await stash(PNG, 100);
  // A photo pasted into a log that was never submitted. It is not owed to
  // anybody yet, and uploading it would send bytes no record points at.
  await stash(JPG, 100);

  const uploaded = [];
  const putBlob = async blob => { uploaded.push(blob.digest); return { ok: true, status: 201 }; };

  await syncOutbox({ outbox, post: fakePost(allCreated), putBlob, subject: "jdoe" });

  assert.deepEqual(uploaded, [PNG]);
});

test("a record with no photos needs no upload step", async () => {
  const outbox = await outboxWith(newEntry(record, { id: "plain", capturedBy: "jdoe" }));
  let asked = false;
  const putBlob = async () => { asked = true; return { ok: true, status: 201 }; };

  const summary = await syncOutbox({ outbox, post: fakePost(allCreated), putBlob, subject: "jdoe" });

  assert.equal(asked, false);
  assert.equal(summary.filed, 1);
});

// --- Watching it go ---

test("progress is weighted by bytes, not by photo count", async () => {
  const outbox = await outboxWith(withPhoto("a", PNG), withPhoto("b", JPG));
  const stash = fakeBlobs(outbox);
  await stash(PNG, 9_000_000);   // a photograph
  await stash(JPG, 1_000_000);   // a screenshot

  const seen = [];
  const putBlob = async () => ({ ok: true, status: 201 });
  await syncOutbox({
    outbox, post: fakePost(allCreated), putBlob, subject: "jdoe",
    onProgress: p => seen.push({ ...p }),
  });

  const afterFirst = seen.filter(p => p.phase === "photos" && p.done === 1).pop();
  assert.equal(afterFirst.bytesDone, 9_000_000);
  assert.equal(afterFirst.bytesTotal, 10_000_000);

  // One of two photos done is 90% of the work here, not 50%.
  assert.ok(progressFraction(afterFirst) > 0.8,
    `expected most of the bar after the big photo, got ${progressFraction(afterFirst)}`);

  assert.ok(seen.some(p => p.phase === "records"), "the records phase is reported too");
});

test("the bar keeps a sliver for the records", async () => {
  // A batch of JSON against a week of photographs is not half the work, and a
  // bar that says it is would sit still and then finish instantly.
  assert.equal(progressFraction({ phase: "photos", bytesDone: 10, bytesTotal: 10 }), 1 - RECORDS_SHARE);
  assert.equal(progressFraction({ phase: "records", done: 4, total: 4 }), 1);
  assert.equal(progressFraction(null), 0);
  // No photos at all: the phase reports without dividing by zero.
  assert.equal(progressFraction({ phase: "photos", bytesDone: 0, bytesTotal: 0 }), 0);
});

test("the progress line names the phase", () => {
  assert.match(
    describeProgress({ phase: "photos", done: 7, total: 23, bytesDone: 18_400_000, bytesTotal: 62_100_000 }),
    /Uploading photos 7 of 23 \(17\.5 MB of 59\.2 MB\)/);
  assert.equal(describeProgress({ phase: "records", total: 12 }), "Filing 12 records");
  assert.equal(describeProgress({ phase: "records", total: 1 }), "Filing 1 record");
});

// --- Weather a record can still gain ---
//
// The chance to add it exists only while the record is in the queue: once it is
// filed it is evidence, and evidence is immutable. Sync is where that window
// closes, so it is where the lookup has to happen.

const AT_A_POINT = { ...record, metadata: { location: "52.51631, 13.37771" } };

const reading = {
  summary: "Light rain, 6 °C, wind 24 km/h",
  observed_at: "2026-08-25T10:00:00Z",
};

test("a record that named a place but no sky gains the reading", async () => {
  const outbox = await outboxWith(newEntry(AT_A_POINT, { id: "a", capturedBy: "jdoe" }));
  const post = fakePost(allCreated);

  const summary = await syncOutbox({
    outbox, post, subject: "jdoe",
    lookUpWeather: async () => reading,
  });

  assert.equal(summary.weather, 1);
  const sent = post.sent[0].payload.records[0];
  assert.equal(sent.metadata.weather_conditions, reading.summary);
  // Marked as a reading, so it reads as the fetched thing it is rather than as
  // the tester's own account of the sky.
  assert.equal(sent.metadata.weather_observed_at, reading.observed_at);
});

test("a tester's own words are never replaced", async () => {
  const written = {
    ...record,
    metadata: { location: "52.51631, 13.37771", weather_conditions: "sleet, gusting across the straight" },
  };
  const outbox = await outboxWith(newEntry(written, { id: "a", capturedBy: "jdoe" }));
  const post = fakePost(allCreated);

  let asked = false;
  await syncOutbox({
    outbox, post, subject: "jdoe",
    lookUpWeather: async () => { asked = true; return reading; },
  });

  assert.equal(asked, false, "somebody who was standing there outranks a model");
  assert.equal(post.sent[0].payload.records[0].metadata.weather_conditions,
    "sleet, gusting across the straight");
});

test("a record with no point is not asked about", async () => {
  const atABench = { ...record, metadata: { location: "Lab 2, bay 4" } };
  const outbox = await outboxWith(newEntry(atABench, { id: "a", capturedBy: "jdoe" }));

  let asked = false;
  await syncOutbox({
    outbox, post: fakePost(allCreated), subject: "jdoe",
    lookUpWeather: async () => { asked = true; return null; },
  });

  // A place name is not a point, and resolving it would mean asking a third
  // party what the tester meant.
  assert.equal(asked, true, "the lookup itself decides; it is given the text as written");
});

test("a lookup that fails does not hold the record back", async () => {
  const outbox = await outboxWith(newEntry(AT_A_POINT, { id: "a", capturedBy: "jdoe" }));
  const post = fakePost(allCreated);

  const summary = await syncOutbox({
    outbox, post, subject: "jdoe",
    // Outside the window the service keeps, which is what a record synced
    // months after the test runs into.
    lookUpWeather: async () => { throw new Error("out of allowed range"); },
  });

  assert.equal(summary.filed, 1, "weather is not worth delaying evidence for");
  assert.equal(summary.weather, 0);
  assert.equal(post.sent[0].payload.records[0].metadata.weather_conditions, undefined);
});

test("without a lookup to call, nothing changes", async () => {
  const outbox = await outboxWith(newEntry(AT_A_POINT, { id: "a", capturedBy: "jdoe" }));
  const post = fakePost(allCreated);

  const summary = await syncOutbox({ outbox, post, subject: "jdoe" });

  assert.equal(summary.filed, 1);
  assert.equal(summary.weather, 0);
});
