// Records filed with nowhere to send them.
//
// A tester on a campaign presses Create and the record has to go somewhere
// that survives the drive home: a closed laptop, a browser restart, an expired
// session, a week. That rules out anything in the page, so the queue is
// IndexedDB, which is on disk.
//
// The logic here is kept behind a small storage interface — all/put/delete —
// so it can be exercised against a plain Map. IndexedDB is not available in
// node, and the decisions worth testing (what enters the queue, what leaves
// it, what may be sent and by whom) are not decisions about IndexedDB.

// What a queued record is waiting for.
//
// A record is QUEUED while the only thing standing between it and the store is
// a connection. It is BLOCKED once the server has refused it — a bad field, a
// source that is not the sender's — because that will not fix itself, and
// retrying it every time a signal appears would bury the one thing the tester
// has to look at.
import { digestsInRecord } from "./blobref.js";

export const QUEUED = "queued";
export const BLOCKED = "blocked";

export const DB_NAME = "evidence-outbox";
export const DB_VERSION = 2;
export const RECORD_STORE = "records";
// Photo bytes, keyed by their own digest — the same name the store will give
// them. Added in version 2; a browser holding a version 1 queue keeps its
// records and gains an empty blob store.
export const BLOB_STORE = "blobs";

// How long a photo with no record pointing at it is kept.
//
// The same reasoning the server's orphan sweep uses (DESIGN.md §4.4): between
// pasting an image into a log and pressing Create, a blob is legitimately
// unreferenced, and a sweep that ran in that window would delete the photo out
// of a log still being written. Matching the server's 24 hours keeps one number
// to explain rather than two.
export const ORPHAN_GRACE_MS = 24 * 60 * 60 * 1000;

// newEntry wraps a record for the queue.
//
// The client_record_id is minted here, at capture, and it is also the entry's
// own key. That is not a coincidence: the token means "this submission" to the
// server (docs/api-reference.md#sending-the-same-record-twice), and this queue
// holds exactly one row per submission, so they are the same identity. It also
// makes re-queueing an edited record a replace rather than a second copy.
//
// capturedBy is who was signed in when the record was written. It is kept so
// that a record cannot later be filed under somebody else's name — see
// sendableBy below.
export function newEntry(record, { capturedBy = null, id = crypto.randomUUID(), now = () => new Date() } = {}) {
  return {
    id,
    record: { ...record, client_record_id: id },
    capturedAt: now().toISOString(),
    capturedBy,
    state: QUEUED,
    error: null,
    attempts: 0,
  };
}

// sendableBy decides whether this session may file an entry.
//
// A record remembers who wrote it, and it is only ever sent by that person.
// The server would refuse it anyway for an ordinary human principal — source
// is bound to the caller's own subject — but a sender holding source:any would
// succeed, and file one tester's observations under another's name. Refusing
// here is what keeps `source` an attribution rather than a label.
//
// Two ways there is no claim to contradict, and both mean "send it":
//
//   - The entry was captured by nobody, on a store running open in development
//     or a page that never learned who it was.
//   - This session does not know who it is either — auth is off, or the login
//     has expired. Holding a record back then would be the page inventing a
//     mismatch out of its own ignorance, and telling a tester they are "signed
//     in as nobody" about records they wrote themselves. The server is the one
//     that decides this in the end: with no identity it accepts the source as
//     sent, and with one it enforces it, so sending and reading the answer is
//     both safer and more honest than guessing here.
export function sendableBy(entry, subject) {
  if (entry.state === BLOCKED) return false;
  if (!entry.capturedBy) return true;
  if (!subject) return true;
  return entry.capturedBy === subject;
}

// heldFrom explains why an entry is not going out with this sync, or null if
// it is. Separate from sendableBy because the UI has to say which of the two
// reasons applies, and they read very differently to a tester.
export function heldFrom(entry, subject) {
  if (entry.state === BLOCKED) return entry.error || "rejected by the store";
  if (entry.capturedBy && subject && entry.capturedBy !== subject) {
    return `written by ${entry.capturedBy}, so it waits for them rather than being filed as ${subject}`;
  }
  return null;
}

export function createOutbox(store) {
  const outbox = {
    // Oldest first: a queue drains in the order it filled, and the record a
    // tester has been carrying longest is the one most worth getting in.
    async list() {
      const entries = await store.all();
      return entries.sort((a, b) => (a.capturedAt < b.capturedAt ? -1 : a.capturedAt > b.capturedAt ? 1 : 0));
    },

    async count() {
      return (await store.all()).length;
    },

    // Replaces any entry with the same id, which is what makes editing a
    // queued record safe: the edit lands on the same row rather than beside it.
    async save(entry) {
      await store.put(entry);
      return entry;
    },

    async get(id) {
      return (await store.all()).find(e => e.id === id) || null;
    },

    async remove(id) {
      await store.delete(id);
    },

    // Filed: the store has it, under this id. The queue's job for this record
    // is over.
    async settle(id) {
      await store.delete(id);
    },

    // Refused, and it will stay refused until something changes. The message is
    // the server's own, because a tester fixing a record needs to know what was
    // wrong with it rather than that something was.
    async block(id, message) {
      const entry = await outbox.get(id);
      if (!entry) return null;
      const blocked = { ...entry, state: BLOCKED, error: message, attempts: entry.attempts + 1 };
      await store.put(blocked);
      return blocked;
    },

    // Tried and could not be delivered — no connection, or the store having a
    // bad day. Nothing about the record is wrong, so it stays queued and only
    // its attempt count moves.
    async recordAttempt(id) {
      const entry = await outbox.get(id);
      if (!entry) return null;
      const attempted = { ...entry, attempts: entry.attempts + 1 };
      await store.put(attempted);
      return attempted;
    },

    // --- Photos ---
    //
    // Stashed by digest, which is what the store will call them, so an upload
    // is a straight replay of bytes that are already named.

    async stashBlob({ digest, ext, contentType, bytes, now = () => new Date() }) {
      // Already held: the same photo attached to two records is one stash
      // entry, for the same reason it is one object in the store.
      const existing = await store.getBlob(digest);
      if (existing) return existing;
      const blob = {
        digest, ext, contentType, bytes,
        size: bytes.byteLength ?? bytes.size ?? 0,
        stashedAt: now().toISOString(),
      };
      await store.putBlob(blob);
      return blob;
    },

    async getBlob(digest) {
      return store.getBlob(digest);
    },

    async blobs() {
      return store.allBlobs();
    },

    // markBlobUploaded records that the store now has these bytes, which is
    // what lets the sweep tell two unreachable photos apart: one that has been
    // delivered, and one that is still the only copy in the world.
    async markBlobUploaded(digest, now = () => new Date()) {
      const blob = await store.getBlob(digest);
      if (!blob) return null;
      const uploaded = { ...blob, uploadedAt: now().toISOString() };
      await store.putBlob(uploaded);
      return uploaded;
    },

    // sweepBlobs deletes photos no queued record points at any more, which is
    // what an upload leaves behind once its record has been filed.
    //
    // Reachability rather than a reference count, the same way the store
    // decides it: a count can drift, and what a queue actually holds is always
    // readable.
    //
    // The grace period is for bytes that have never been anywhere else. A log
    // still being written has no record to be reachable from yet, and deleting
    // its photographs would be deleting the only copy. Once the store has the
    // bytes that stops being true, so an uploaded photo whose record has been
    // filed goes at once rather than sitting on a phone for another day — on a
    // campaign that is the difference between a few megabytes and all of them.
    async sweepBlobs({ now = () => new Date(), grace = ORPHAN_GRACE_MS } = {}) {
      const entries = await outbox.list();
      const reachable = new Set();
      for (const entry of entries) {
        for (const digest of digestsInRecord(entry.record)) reachable.add(digest);
      }

      const cutoff = now().getTime() - grace;
      let deleted = 0;
      for (const blob of await store.allBlobs()) {
        if (reachable.has(blob.digest)) continue;
        if (!blob.uploadedAt && Date.parse(blob.stashedAt) > cutoff) continue;
        await store.deleteBlob(blob.digest);
        deleted++;
      }
      return deleted;
    },

    // How much of this device the queue is using, which a tester carrying a
    // week of photographs has a right to see.
    async bytesHeld() {
      return (await store.allBlobs()).reduce((total, blob) => total + (blob.size || 0), 0);
    },

    // Put a blocked record back in the queue, for a tester who has corrected
    // whatever the store objected to.
    async unblock(id) {
      const entry = await outbox.get(id);
      if (!entry) return null;
      const requeued = { ...entry, state: QUEUED, error: null };
      await store.put(requeued);
      return requeued;
    },
  };
  return outbox;
}

// --- Storage ---

// memoryStore is the seam the tests use, and the fallback for a browser that
// will not give us IndexedDB — a private window in some browsers. It is not a
// silent substitute: nothing in it survives the page, so the caller is told.
export function memoryStore() {
  const rows = new Map();
  const blobs = new Map();
  return {
    durable: false,
    async all() { return [...rows.values()].map(e => structuredClone(e)); },
    async put(entry) { rows.set(entry.id, structuredClone(entry)); },
    async delete(id) { rows.delete(id); },
    async allBlobs() { return [...blobs.values()]; },
    async getBlob(digest) { return blobs.get(digest) || null; },
    async putBlob(blob) { blobs.set(blob.digest, blob); },
    async deleteBlob(digest) { blobs.delete(digest); },
  };
}

export function indexedDBStore(indexedDB = globalThis.indexedDB) {
  let dbPromise;

  const open = () => {
    if (dbPromise) return dbPromise;
    dbPromise = new Promise((resolve, reject) => {
      const request = indexedDB.open(DB_NAME, DB_VERSION);
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains(RECORD_STORE)) {
          db.createObjectStore(RECORD_STORE, { keyPath: "id" });
        }
        if (!db.objectStoreNames.contains(BLOB_STORE)) {
          db.createObjectStore(BLOB_STORE, { keyPath: "digest" });
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    return dbPromise;
  };

  const run = async (mode, fn, storeName = RECORD_STORE) => {
    const db = await open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(storeName, mode);
      const request = fn(tx.objectStore(storeName));
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error);
      if (request) {
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      } else {
        tx.oncomplete = () => resolve();
      }
    });
  };

  return {
    durable: true,
    async all() { return (await run("readonly", s => s.getAll())) || []; },
    async put(entry) { await run("readwrite", s => s.put(entry)); },
    async delete(id) { await run("readwrite", s => s.delete(id)); },
    async allBlobs() { return (await run("readonly", s => s.getAll(), BLOB_STORE)) || []; },
    async getBlob(digest) { return (await run("readonly", s => s.get(digest), BLOB_STORE)) || null; },
    async putBlob(blob) { await run("readwrite", s => s.put(blob), BLOB_STORE); },
    async deleteBlob(digest) { await run("readwrite", s => s.delete(digest), BLOB_STORE); },
  };
}

// openStore returns the best storage this browser will give us, and says which
// one it gave. A private window that refuses IndexedDB gets a queue that does
// not survive the window closing, which a tester has to be told before they
// rely on it — the alternative is losing a day of evidence quietly.
export async function openStore(indexedDB = globalThis.indexedDB) {
  if (!indexedDB) return memoryStore();
  try {
    const store = indexedDBStore(indexedDB);
    await store.all();
    return store;
  } catch {
    return memoryStore();
  }
}
