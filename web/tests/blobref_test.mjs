// Unit tests for web/static/blobref.js, run with `node --test`.
//
// This module works out what the store would call an image, without asking it.
// Everything here mirrors a decision made in Go, and the whole feature rests on
// the two agreeing: a reference computed in a field and written into a log is
// the reference the upload will produce days later, or the log points at
// something the store does not serve and a reader finds out months after that.
//
// So the expectations below are not hand-written. Each fixture was posted to a
// running server and the reference it answered with is what is asserted here.
// If Go's sniffer, the extension table or the path prefix ever move, these fail
// without needing a server to notice.

import test from "node:test";
import assert from "node:assert/strict";

import { describe, digestsInRecord, refPath, refsIn, sniffMedia } from "../static/blobref.js";

// One 8x8 image of each type a test log may embed, and the reference
// POST /api/v1/blobs answered with for those exact bytes.
const FIXTURES = {
  png: {
    bytes: "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAIAQMAAAD+wSzIAAAAIGNIUk0AAHomAACAhAAA+gAAAIDoAAB1MAAA6mAAADqYAAAXcJy6UTwAAAAGUExURQFyrf///1KWPg8AAAABYktHRAH/Ai3eAAAAB3RJTUUH6ggZEzUo74629QAAACV0RVh0ZGF0ZTpjcmVhdGUAMjAyNi0wOC0yNVQxOTo1Mzo0MCswMDowMN0ZngMAAAAldEVYdGRhdGU6bW9kaWZ5ADIwMjYtMDgtMjVUMTk6NTM6NDArMDA6MDCsRCa/AAAAKHRFWHRkYXRlOnRpbWVzdGFtcAAyMDI2LTA4LTI1VDE5OjUzOjQwKzAwOjAw+1EHYAAAAAtJREFUCNdjYEAFAAAQAAGhxSHBAAAAAElFTkSuQmCC",
    ref: "/api/v1/blobs/sha256:d5162ff51a59e018804ad2e4dfd40d7edd6a91d58e12c412364eb63f7c3e32d6.png",
    contentType: "image/png",
  },
  jpeg: {
    bytes: "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAA0JCgsKCA0LCgsODg0PEyAVExISEyccHhcgLikxMC4pLSwzOko+MzZGNywtQFdBRkxOUlNSMj5aYVpQYEpRUk//2wBDAQ4ODhMREyYVFSZPNS01T09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT0//wAARCAAIAAgDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAX/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAABQb/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCWAPV7/9k=",
    ref: "/api/v1/blobs/sha256:7a30e4412894deaa628297d911c9529e950a94b49a3ede9e8197f0a3aeba1741.jpg",
    contentType: "image/jpeg",
  },
  gif: {
    bytes: "R0lGODlhCAAIAPAAAC2eLQAAACH5BAAAAAAALAAAAAAIAAgAAAIHhI+py+1dAAA7",
    ref: "/api/v1/blobs/sha256:40e4710d64b7ca69237649a0d4406d0d51d959ee0460d626b614791a5d361114.gif",
    contentType: "image/gif",
  },
  webp: {
    bytes: "UklGRjoAAABXRUJQVlA4IC4AAADQAQCdASoIAAgAAgA0JaACdLoB+AADsAD+4ka/9GH8aR40j4uH/npmvMH+FgAA",
    ref: "/api/v1/blobs/sha256:7a003f568370539f29b204f7f1273b0c93183b4af7e64259b6d3e7a2e7ce63e4.webp",
    contentType: "image/webp",
  },
};

const bytesOf = name => new Uint8Array(Buffer.from(FIXTURES[name].bytes, "base64"));

// --- The reference the server would give ---

for (const [name, fixture] of Object.entries(FIXTURES)) {
  test(`a ${name} is named exactly as the store names it`, async () => {
    const described = await describe(bytesOf(name));

    assert.equal(described.ref, fixture.ref,
      "this reference goes into a log offline and must be the one the upload returns");
    assert.equal(described.contentType, fixture.contentType);
  });
}

test("the type comes from the bytes, not from what the file claims", () => {
  // The server sniffs and would refuse this after the reference was already
  // written into the log, so the browser has to reach the same answer.
  const pdf = new Uint8Array([0x25, 0x50, 0x44, 0x46, 0x2d, 0x31, 0x2e, 0x37]);
  assert.equal(sniffMedia(pdf), null);

  const empty = new Uint8Array([]);
  assert.equal(sniffMedia(empty), null);

  // Truncated to shorter than its own signature.
  assert.equal(sniffMedia(bytesOf("png").slice(0, 4)), null);
});

test("a log may not carry it, so nothing is written for it", async () => {
  const pdf = new Uint8Array([0x25, 0x50, 0x44, 0x46]);
  assert.equal(await describe(pdf), null);
});

test("a reference with no extension is still a reference", () => {
  assert.equal(refPath("sha256:abc", ""), "/api/v1/blobs/sha256:abc");
  assert.equal(refPath("sha256:abc", "png"), "/api/v1/blobs/sha256:abc.png");
});

// --- Finding references again ---

const A = "sha256:" + "a".repeat(64);
const B = "sha256:" + "b".repeat(64);

test("references are found wherever a tester put them", () => {
  const log = [
    `Wet surface, see ![shot](/api/v1/blobs/${A}.jpg)`,
    `and the bare link /api/v1/blobs/${B}.png at the end.`,
  ].join("\n");

  assert.deepEqual(refsIn(log).map(r => r.digest), [A, B]);
});

test("the same image twice is one blob", () => {
  // Deduplicated by digest and not by digest-plus-extension, the rule the
  // server applies: one image written once with an extension and once without
  // is one object.
  const log = `![a](/api/v1/blobs/${A}.jpg) and again ![b](/api/v1/blobs/${A})`;
  assert.deepEqual(refsIn(log).map(r => r.digest), [A]);
});

test("another store's blob is not read as one of ours", () => {
  // A reference into this store is relative. An absolute URL at somebody
  // else's host that happens to share the path is theirs, and uploading our
  // bytes under it would be filing a photo nobody took.
  const log = `![theirs](https://elsewhere.example.com/api/v1/blobs/${A}.jpg)`;
  assert.deepEqual(refsIn(log), []);
});

test("a record's blobs come from its log and its photo_uris", () => {
  const record = {
    metadata: {
      observations: `![shot](/api/v1/blobs/${A}.jpg)`,
      photo_uris: [`/api/v1/blobs/${B}.png`, "https://photos.example.com/elsewhere.jpg"],
    },
  };

  assert.deepEqual(digestsInRecord(record).sort(), [A, B].sort());
});

test("a record with no images depends on no blobs", () => {
  assert.deepEqual(digestsInRecord({ metadata: { observations: "nothing attached" } }), []);
  assert.deepEqual(digestsInRecord({}), []);
});
