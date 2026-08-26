// Naming a blob without asking the server what it is called.
//
// A blob is named by the SHA-256 of its bytes and by nothing else
// (DESIGN.md §4.4), which is what makes this possible: the browser can work
// out the reference an upload *would* return, write it into the log, and send
// the bytes later. The log is finished the moment the tester writes it, and
// nothing has to be rewritten at sync.
//
// Everything here mirrors a decision made in Go, and the two must not drift:
//
//   PATH_PREFIX  <- blob.PathPrefix
//   ALGORITHM    <- blob.algorithm
//   MEDIA        <- blob.mediaExt, and the magic bytes http.DetectContentType
//                   uses to choose between them
//   refsIn       <- blob.Refs
//
// The consequence of drifting is not a broken build: it is a log that points at
// a reference the store does not serve, discovered by a reader months later.

export const PATH_PREFIX = "/api/v1/blobs/";
export const ALGORITHM = "sha256";

// What a test log may embed. The server decides this from the bytes and
// rejects anything else, so a browser that guessed differently would have its
// upload refused after the reference was already written into the log.
//
// The signatures are the ones Go's http.DetectContentType matches on. WebP is
// the awkward one: "RIFF", four bytes of length, then "WEBPVP".
const MEDIA = [
  { type: "image/png", ext: "png", match: b => starts(b, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]) },
  { type: "image/jpeg", ext: "jpg", match: b => starts(b, [0xff, 0xd8, 0xff]) },
  { type: "image/gif", ext: "gif", match: b => ascii(b, 0, "GIF87a") || ascii(b, 0, "GIF89a") },
  { type: "image/webp", ext: "webp", match: b => ascii(b, 0, "RIFF") && ascii(b, 8, "WEBPVP") },
];

function starts(bytes, signature) {
  if (bytes.length < signature.length) return false;
  return signature.every((byte, i) => bytes[i] === byte);
}

function ascii(bytes, offset, text) {
  if (bytes.length < offset + text.length) return false;
  for (let i = 0; i < text.length; i++) {
    if (bytes[offset + i] !== text.charCodeAt(i)) return false;
  }
  return true;
}

// sniffMedia identifies an image from its leading bytes, or returns null for
// anything a log may not carry.
//
// The file's own type is not consulted, for the same reason the server does not
// consult the uploader's Content-Type: it is a claim, and the extension written
// into a log has to be a property of the bytes.
export function sniffMedia(bytes) {
  const head = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  return MEDIA.find(m => m.match(head)) || null;
}

// digestOf returns the content address of some bytes, spelled as the store
// spells it.
export async function digestOf(bytes, subtle = globalThis.crypto?.subtle) {
  if (!subtle) throw new Error("SHA-256 is unavailable; this page needs HTTPS");
  const hash = await subtle.digest("SHA-256", bytes);
  const hex = [...new Uint8Array(hash)].map(b => b.toString(16).padStart(2, "0")).join("");
  return `${ALGORITHM}:${hex}`;
}

// refPath is the reference as it is written into a log.
export function refPath(digest, ext) {
  return ext ? `${PATH_PREFIX}${digest}.${ext}` : `${PATH_PREFIX}${digest}`;
}

// describe works out everything the log and the queue need to know about an
// image, without a server: what it is, what it will be called, and what to
// write. Returns null if a log may not carry it.
export async function describe(bytes, subtle) {
  const media = sniffMedia(bytes);
  if (!media) return null;
  const digest = await digestOf(bytes, subtle);
  return { digest, ext: media.ext, contentType: media.type, ref: refPath(digest, media.ext) };
}

// refsIn finds the blobs a text refers to, deduplicated by digest, in the order
// they first appear — the same rule blob.Refs applies server-side.
//
// Anchored on the serving path rather than on markdown, because the same
// reference has to be found in `![shot](…)`, in a bare link somebody typed, and
// in photo_uris. The leading delimiter is what stops
// `https://elsewhere.example.com/api/v1/blobs/…` reading as a reference into
// this store: a reference is relative, so the path may only follow a delimiter,
// never the tail of a host.
const REF_PATTERN = new RegExp(
  `(?:^|[^\\w.:/@-])${PATH_PREFIX.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}` +
  `(${ALGORITHM}:[0-9a-f]{64})(?:\\.([a-z0-9]{1,5}))?`,
  "g",
);

export function refsIn(text) {
  if (!text) return [];
  const seen = new Set();
  const refs = [];
  for (const match of String(text).matchAll(REF_PATTERN)) {
    const digest = match[1];
    if (seen.has(digest)) continue;
    seen.add(digest);
    refs.push({ digest, ext: match[2] || "" });
  }
  return refs;
}

// digestsInRecord lists every blob a queued record depends on: the ones written
// into its log, and the ones its photo_uris names. Both, because a client may
// have filled in either.
export function digestsInRecord(record) {
  const metadata = (record && record.metadata) || {};
  const fromLog = refsIn(metadata.observations);
  const fromPhotos = refsIn([].concat(metadata.photo_uris || []).join("\n"));
  const digests = new Set([...fromLog, ...fromPhotos].map(r => r.digest));
  return [...digests];
}
