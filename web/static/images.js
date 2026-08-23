// Images in a test log: getting them in (paste and drop) and getting them back
// out (hydrating what the renderer left behind).
//
// The renderer is a pure string function and never fetches anything, so it
// emits `<img data-blob="…">` with no `src`. It could not do otherwise: reading
// a blob needs the API key, and an `<img src>` cannot carry an Authorization
// header. So the bytes are fetched here, turned into an object URL, and put on
// the tag afterwards.

import { API_BASE, apiFetch } from "./common.js";

// Above this, an image is re-encoded before upload. Phone cameras produce a few
// megabytes of JPEG for something that will be read at screen size, and a log
// is more useful when its images load than when they are faithful to the last
// pixel.
const DOWNSCALE_ABOVE_BYTES = 1_500_000;
const MAX_DIMENSION = 2000;

// Object URLs by reference. A log shows the same image every time the dialog is
// opened, and the same image can appear in the preview and in a record, so the
// fetch is worth doing once.
const objectURLs = new Map();

let uploadCounter = 0;

// --- Showing images ---

// hydrateImages fills in the images inside a rendered log.
export async function hydrateImages(root) {
  const images = [...root.querySelectorAll("img[data-blob]:not([src])")];
  await Promise.all(images.map(loadImage));
}

async function loadImage(img) {
  const ref = img.getAttribute("data-blob");

  // A blob base pointing at another host does its own access control. Sending
  // this store's API key there would hand it to someone else.
  if (!ref.startsWith("/")) {
    img.src = ref;
    return;
  }

  const cached = objectURLs.get(ref);
  if (cached) {
    img.src = cached;
    return;
  }

  try {
    const resp = await apiFetch(ref);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const url = URL.createObjectURL(await resp.blob());
    objectURLs.set(ref, url);
    img.src = url;
  } catch {
    // A log whose image has been swept, or whose reader lacks the key, still
    // reads as a log. Saying so beats a broken-image icon.
    const missing = document.createElement("span");
    missing.className = "test-log-image-missing";
    missing.textContent = img.alt ? `[image unavailable: ${img.alt}]` : "[image unavailable]";
    img.replaceWith(missing);
  }
}

// releaseImages revokes object URLs nothing on the page is showing any more.
// Checking the document rather than assuming is what lets the record dialog and
// the Add form's preview share the cache without one closing out the other.
export function releaseImages() {
  const showing = new Set(
    [...document.querySelectorAll('img[src^="blob:"]')].map(img => img.src),
  );
  for (const [ref, url] of objectURLs) {
    if (showing.has(url)) continue;
    URL.revokeObjectURL(url);
    objectURLs.delete(ref);
  }
}

// --- Adding images ---

// attachImageUploads lets a tester paste or drop images into a test log field.
// Each one is uploaded and replaced by a markdown reference to it; onStatus
// reports what went wrong, since a silent failure would leave the tester
// believing the photo is filed.
export function attachImageUploads(field, onStatus) {
  field.addEventListener("paste", event => {
    const files = imageFiles(event.clipboardData);
    if (files.length === 0) return; // ordinary text paste
    event.preventDefault();
    embedAll(field, files, onStatus);
  });

  field.addEventListener("dragover", event => {
    if (!hasFiles(event.dataTransfer)) return;
    // Without this the browser navigates away to the dropped file, taking the
    // half-written log with it.
    event.preventDefault();
    field.classList.add("drop-active");
  });

  for (const name of ["dragleave", "dragend", "drop"]) {
    field.addEventListener(name, () => field.classList.remove("drop-active"));
  }

  field.addEventListener("drop", event => {
    const files = imageFiles(event.dataTransfer);
    if (files.length === 0) return;
    event.preventDefault();
    embedAll(field, files, onStatus);
  });
}

function hasFiles(transfer) {
  return !!transfer && [...(transfer.types || [])].includes("Files");
}

function imageFiles(transfer) {
  if (!transfer) return [];
  return [...(transfer.files || [])].filter(file => file.type.startsWith("image/"));
}

function embedAll(field, files, onStatus) {
  for (const file of files) embed(field, file, onStatus);
}

async function embed(field, file, onStatus) {
  // A placeholder goes in at the caret straight away, so the tester can keep
  // writing while the upload runs and can see where the image will land. It is
  // replaced by text search rather than by position, because by the time the
  // upload returns the caret has moved on.
  const token = `![uploading…](#pending-${++uploadCounter})`;
  insertAtCursor(field, token);

  try {
    const prepared = await prepareForUpload(file);
    const { ref } = await uploadBlob(prepared);
    replaceInField(field, token, `![${altFor(file)}](${ref})`);
  } catch (err) {
    replaceInField(field, token, "");
    onStatus(`Could not attach ${file.name || "image"}: ${err.message}`);
  }
}

// prepareForUpload shrinks an image that is larger than a log needs.
//
// Failures here are not reported: an image this cannot decode is passed through
// as it is, and the server decides whether it is something a log may carry.
async function prepareForUpload(file) {
  // Re-encoding an animation would quietly reduce it to its first frame.
  if (file.type === "image/gif") return file;
  if (file.size <= DOWNSCALE_ABOVE_BYTES) return file;

  try {
    const bitmap = await createImageBitmap(file);
    const scale = Math.min(1, MAX_DIMENSION / Math.max(bitmap.width, bitmap.height));
    const canvas = document.createElement("canvas");
    canvas.width = Math.round(bitmap.width * scale);
    canvas.height = Math.round(bitmap.height * scale);
    canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    bitmap.close?.();

    const shrunk = await new Promise(resolve => canvas.toBlob(resolve, "image/webp", 0.85));
    // Re-encoding can make a well-compressed image bigger. Keeping the original
    // in that case is both smaller and closer to what the tester saw.
    return shrunk && shrunk.size < file.size ? shrunk : file;
  } catch {
    return file;
  }
}

async function uploadBlob(file) {
  const resp = await apiFetch(`${API_BASE}/blobs`, {
    method: "POST",
    headers: { "Content-Type": file.type || "application/octet-stream" },
    body: file,
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.error || `upload failed (HTTP ${resp.status})`);
  return data;
}

// A pasted screenshot arrives as "image.png" from every browser, which makes a
// useless alt text. A dropped file's own name is worth keeping.
function altFor(file) {
  const name = (file.name || "").replace(/\.[a-z0-9]+$/i, "").trim();
  return !name || name === "image" ? "screenshot" : name;
}

function insertAtCursor(field, text) {
  const start = field.selectionStart ?? field.value.length;
  const end = field.selectionEnd ?? start;

  // On its own line, because an image in the middle of a sentence reads as a
  // gap in the sentence once it is rendered.
  const before = field.value.slice(0, start);
  const prefix = before === "" || before.endsWith("\n") ? "" : "\n";

  field.setRangeText(prefix + text + "\n", start, end, "end");
  field.dispatchEvent(new Event("input", { bubbles: true }));
  field.focus();
}

function replaceInField(field, token, replacement) {
  if (!field.value.includes(token)) return; // the tester deleted it meanwhile
  field.value = field.value.split(token).join(replacement);
  field.dispatchEvent(new Event("input", { bubbles: true }));
}
