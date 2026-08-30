// The outbox as it appears on the page: the counter in the header, the dialog
// listing what is waiting, and the sync those two report on.
//
// The queue itself is outbox.js and the sending is sync.js; this is the part
// that has a document to talk to. Moved out of app.js (issue #124) with the
// couplings turned into arguments: who is signed in and what happens when
// somebody presses Edit are both supplied at mount, so this module knows which
// record was chosen and nothing about the form it is going back into.

import { API_BASE, apiFetchNoRedirect, esc, formatTime, resultBadge } from "./common.js";
import { OFFLINE, connectionState, onConnectionChange } from "./offline.js";
import {
  BLOCKED, STALE_URGENT_DAYS, STALE_WARN_DAYS,
  ageInDays, assessDurability, createOutbox, heldFrom, openStore, roomIsTight, staleness,
} from "./outbox.js";
import { describeProgress, describeSync, formatBytes, progressFraction, syncOutbox } from "./sync.js";
import { digestsInRecord } from "./blobref.js";
import { parseCoordinates } from "./location.js";
import { fetchWeather } from "./weather.js";
import { useStash } from "./images.js";

// Records written with nowhere to send them. See docs/offline-support-plan.md.
let outbox = null;
// How this session reaches the rest of the page: who is signed in, and what to
// do when somebody presses Edit on a queued record. Supplied at mount so that
// the subject has one home rather than a copy here and another there.
let subjectOf = () => null;
let onEditRequested = () => {};
// One sync at a time. Reconnecting, loading and pressing the button can all
// arrive together, and three syncs racing would report three different things.
let syncing = false;

// What this browser has promised about the queue, learned once at startup.
let durability = { level: "durable", persisted: true };

export async function mountOutbox({ subject = () => null, onEdit = () => {} } = {}) {
  subjectOf = subject;
  onEditRequested = onEdit;
  const store = await openStore();
  outbox = createOutbox(store);
  // Asked for before anything is queued, so the promise is in place by the
  // time there is something to keep.
  durability = await assessDurability(store);
  // Photos attached from now on are named and kept here rather than uploaded,
  // so a log written with no connection is finished when the tester writes it.
  useStash(outbox);
  // Bytes belonging to logs that were never filed, from some earlier sitting.
  outbox.sweepBlobs().catch(() => {});
  await refreshOutboxCount();

  document.getElementById("outbox-status").addEventListener("click", event => {
    event.preventDefault();
    openOutbox();
  });
  document.getElementById("close-outbox").addEventListener("click", () => {
    document.getElementById("outbox-dialog").close();
  });
  document.getElementById("outbox-send").addEventListener("click", async () => {
    await runSync({ announce: true });
    await renderOutbox();
  });

  // The moment a signal returns is rarely a moment anyone is looking at the
  // page — a laptop lid opening in a hotel lobby is the whole opportunity.
  onConnectionChange(state => {
    if (state !== OFFLINE) runSync();
  });
}

export async function refreshOutboxCount() {
  const el = document.getElementById("outbox-status");
  if (!outbox || !el) return;
  const entries = await outbox.list();
  el.hidden = entries.length === 0;
  if (entries.length === 0) {
    // Cleared, not just hidden. Leaving "3 unsent for 45 days" behind an empty
    // queue means it reappears the moment one record is queued again, saying
    // something that stopped being true weeks ago.
    el.textContent = "";
    el.title = "";
    el.classList.remove("outbox-status-urgent");
    return;
  }

  const blocked = entries.filter(e => e.state === BLOCKED).length;
  const needs = n => `${n} need${n === 1 ? "s" : ""} attention`;
  if (blocked === 0) {
    el.textContent = `${entries.length} waiting to send`;
  } else if (blocked === entries.length) {
    // Saying "3 waiting, 3 need attention" reads as six records.
    el.textContent = needs(blocked);
  } else {
    el.textContent = `${entries.length} waiting, ${needs(blocked)}`;
  }

  // How long the oldest has been waiting, once that becomes the more useful
  // fact than how many there are. Nothing expires and nothing is refused; the
  // point is to say so while the evidence is still there to save.
  const stale = staleness(entries);
  el.classList.toggle("outbox-status-urgent", stale.level === "urgent");
  if (stale.level !== "none") {
    el.textContent = `${entries.length} unsent for ${stale.days} days`;
    el.title = stale.level === "urgent"
      ? `The oldest record here was written ${stale.days} days ago and has never reached the store.`
      : `Waiting ${stale.days} days. Send these while they are still here.`;
  } else {
    el.title = "Records written on this device that have not reached the store yet";
  }
}

export function openOutbox() {
  document.getElementById("outbox-dialog").showModal();
  renderOutbox();
}

async function renderOutbox() {
  const list = document.getElementById("outbox-list");
  const explainer = document.getElementById("outbox-explainer");
  const entries = await outbox.list();

  if (entries.length === 0) {
    explainer.textContent = "";
    list.innerHTML = `<p class="test-log-empty">Nothing waiting. Everything filed here has reached the store.</p>`;
    return;
  }

  const held = await outbox.bytesHeld();
  explainer.className = "outbox-explainer";
  explainer.textContent = describeDurability(durability, held);
  if (durability.level === "session") explainer.classList.add("outbox-explainer-alarm");
  else if (durability.level === "evictable" || roomIsTight(durability)) {
    explainer.classList.add("outbox-explainer-warn");
  }

  list.innerHTML = entries.map(entry => {
    const r = entry.record;
    const digests = digestsInRecord(r);
    const held = heldFrom(entry, subjectOf());
    const heldClass = entry.state === BLOCKED ? "outbox-entry-error" : "outbox-entry-held";
    return `
      <div class="outbox-entry" data-id="${esc(entry.id)}">
        <div class="outbox-entry-head">
          ${resultBadge(r.result)}
          <span class="outbox-entry-procedure">${esc(r.procedure_ref || "(no procedure)")}</span>
        </div>
        <div class="outbox-entry-meta">
          ${esc(r.repo || "")} ${esc(r.branch || "")} ${esc((r.rcs_ref || "").slice(0, 12))}
          &middot; written ${formatTime(entry.capturedAt)}${describeAge(entry)}
        </div>
        ${digests.length ? `<div class="outbox-entry-photos" data-digests="${esc(digests.join(","))}"></div>` : ""}
        ${held ? `<div class="${heldClass}">${esc(held)}</div>` : ""}
        <div class="outbox-entry-actions">
          ${canLookUpWeather(r) ? `<button class="secondary outline outbox-weather">Look up weather</button>` : ""}
          <button class="secondary outline outbox-edit">Edit</button>
          <button class="secondary outline outbox-delete">Delete</button>
        </div>
      </div>`;
  }).join("");

  // The photographs, from the device's own copy. Seeing them is how a tester
  // knows the pictures are safe and not just the words about them.
  for (const holder of list.querySelectorAll(".outbox-entry-photos")) {
    for (const digest of holder.dataset.digests.split(",")) {
      const blob = await outbox.getBlob(digest);
      if (!blob) continue;
      const img = document.createElement("img");
      img.src = URL.createObjectURL(new Blob([blob.bytes], { type: blob.contentType }));
      img.alt = "attached photo";
      holder.appendChild(img);
    }
  }

  list.querySelectorAll(".outbox-weather").forEach(btn => {
    btn.addEventListener("click", () => lookUpQueuedWeather(btn.closest(".outbox-entry").dataset.id, btn));
  });
  list.querySelectorAll(".outbox-edit").forEach(btn => {
    btn.addEventListener("click", () => editQueued(btn.closest(".outbox-entry").dataset.id));
  });
  list.querySelectorAll(".outbox-delete").forEach(btn => {
    btn.addEventListener("click", () => deleteQueued(btn.closest(".outbox-entry").dataset.id));
  });
}

// describeDurability says plainly what this browser has promised about the
// queue, because a tester deciding whether to keep working in the field is
// entitled to know before they rely on it rather than after.
function describeDurability(assessment, bytesHeld) {
  const photos = bytesHeld ? ` Photos waiting here take up ${formatBytes(bytesHeld)}.` : "";

  if (assessment.level === "session") {
    return "This browser will not store anything on disk, so these records last only " +
      "as long as this tab. Send them before you close it, or file them somewhere else." + photos;
  }

  const base = "These are on this device only, until they are sent. They survive closing the " +
    "browser, and they go automatically when there is a connection." + photos;

  if (assessment.level === "evictable") {
    return base + " This browser has not promised to keep them, so it may reclaim the space " +
      "if the device runs low. Send them when you can.";
  }
  if (roomIsTight(assessment)) {
    return base + " This device is running low on space for them.";
  }
  return base;
}

// canLookUpWeather reports whether the reading is still worth offering.
//
// A record that already carries a weather line is left alone, whoever wrote it:
// overwriting a tester's own account of the sky with a model's would be exactly
// the wrong way round. What is needed is a point to ask about, which means the
// location has to be coordinates rather than "Lab 2, bay 4".
function canLookUpWeather(record) {
  const metadata = record.metadata || {};
  if (metadata.weather_conditions) return false;
  return !!parseCoordinates(metadata.location || "");
}

// lookUpQueuedWeather fills in the weather for a record written earlier.
//
// The lookup wants a point and an hour, and a queued record already carries
// both, so a reading fetched now is still a reading for the hour the test ran
// in. That is what lets weather_observed_at go on it: the record synced on
// Friday for a test run on Tuesday is not passed off as having been checked
// live, and a reader can still go back and check it.
//
// The bound worth knowing is that the service keeps a recent window. Outside
// it the answer is a 404 carrying the service's own account of why, which is
// shown as it stands — it tells a tester what to do next in a way "unavailable"
// cannot, and what to do next is write the weather down.
async function lookUpQueuedWeather(id, btn) {
  const entry = await outbox.get(id);
  if (!entry) return;

  const explainer = document.getElementById("outbox-explainer");
  btn.setAttribute("aria-busy", "true");
  btn.disabled = true;
  try {
    const point = parseCoordinates(entry.record.metadata.location);
    const when = entry.record.finished_at ? new Date(entry.record.finished_at) : null;
    const reading = await fetchWeather(point, when);

    const metadata = { ...entry.record.metadata, weather_conditions: reading.summary };
    if (reading.observed_at) metadata.weather_observed_at = reading.observed_at;
    await outbox.save({ ...entry, record: { ...entry.record, metadata } });

    explainer.textContent = `Weather filled in: ${reading.summary}`;
    await renderOutbox();
  } catch (err) {
    explainer.textContent = `${err.message} You can write the weather in by editing the record.`;
  } finally {
    btn.removeAttribute("aria-busy");
    btn.disabled = false;
  }
}

// editQueued loads a waiting record back into the Add Result form.
//
// The queued copy stays where it is until the tester submits. Taking it out of
// the queue on the way into the form would mean a closed tab loses the record,
// which is the one thing this whole feature exists to prevent.
async function editQueued(id) {
  const entry = await outbox.get(id);
  if (!entry) return;

  document.getElementById("outbox-dialog").close();
  // What happens to the form is the form's business. This module knows which
  // record was chosen and nothing about where it is going.
  onEditRequested(entry);
}

async function deleteQueued(id) {
  const entry = await outbox.get(id);
  if (!entry) return;
  // No undo, and the record exists nowhere else. Worth a question.
  const what = entry.record.procedure_ref || "this record";
  if (!confirm(`Delete ${what}? It has not been sent, and this is the only copy.`)) return;

  await outbox.remove(id);
  await refreshOutboxCount();
  await renderOutbox();
}

// describeAge adds the waiting time to a row, once it is long enough to be the
// point. A record written this morning does not need telling.
function describeAge(entry) {
  const days = ageInDays(entry);
  if (days < STALE_WARN_DAYS) return "";
  const emphasis = days >= STALE_URGENT_DAYS ? "outbox-entry-error" : "outbox-entry-held";
  return ` &middot; <span class="${emphasis}">waiting ${days} days</span>`;
}

// runSync drains the queue and says what happened.
//
// `announce` is on when a person pressed the button and is waiting for an
// answer. An automatic sync stays quiet unless something needs a person: the
// count in the header falling is the report, and a dialog that appears because
// a train came out of a tunnel is not.
export async function runSync({ announce = false } = {}) {
  if (!outbox || syncing) return null;
  if (await outbox.count() === 0) return null;

  syncing = true;
  const button = document.getElementById("outbox-send");
  button.setAttribute("aria-busy", "true");
  try {
    const summary = await syncOutbox({
      outbox,
      subject: subjectOf(),
      onProgress: showProgress,
      // A record that named a place but no sky can still gain the reading,
      // right up until it is filed and becomes something nobody can add to.
      lookUpWeather: async (location, finishedAt) => {
        const point = parseCoordinates(location);
        if (!point) return null;
        return fetchWeather(point, finishedAt ? new Date(finishedAt) : null, apiFetchNoRedirect);
      },
      // Uploading is a straight replay of bytes that are already named: the
      // store answers with the reference the browser worked out when the photo
      // was attached, and storing the same bytes twice is one object.
      putBlob: async blob => {
        const resp = await apiFetchNoRedirect(`${API_BASE}/blobs`, {
          method: "POST",
          headers: { "Content-Type": blob.contentType || "application/octet-stream" },
          body: blob.bytes,
        });
        return { ok: resp.ok, status: resp.status };
      },
      post: async (path, payload) => {
        const resp = await apiFetchNoRedirect(`${API_BASE}${path}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        const data = await resp.json().catch(() => null);
        return { ok: resp.ok, status: resp.status, data };
      },
    });

    // Photos whose records have now been filed are owed to nobody.
    await outbox.sweepBlobs();
    await refreshOutboxCount();
    clearProgress();
    const explainer = document.getElementById("outbox-explainer");
    if (announce || summary.authRequired || summary.blocked) {
      explainer.textContent = describeSync(summary);
    }
    return summary;
  } finally {
    syncing = false;
    clearProgress();
    button.removeAttribute("aria-busy");
  }
}

// showProgress reports a sync as it runs.
//
// A week of manual results is a few kilobytes of JSON behind a few hundred
// megabytes of photographs, over whatever link a hotel lobby provides. A tester
// who cannot see it moving has no way to tell an upload in progress from one
// that has quietly wedged — which is exactly when they close the laptop.
function showProgress(progress) {
  const el = document.getElementById("outbox-progress");
  if (!el) return;
  el.hidden = false;
  el.querySelector("progress").value = progressFraction(progress);
  el.querySelector(".outbox-progress-label").textContent = describeProgress(progress);
}

function clearProgress() {
  const el = document.getElementById("outbox-progress");
  if (el) el.hidden = true;
}

// --- What the Add Result form needs from here ---

// queueRecord files a record on this device instead of at the store.
export async function queueRecord(entry) {
  await outbox.save(entry);
  await refreshOutboxCount();
}

// dropQueued forgets a queued record, for when the store has just accepted it.
export async function dropQueued(id) {
  await outbox.remove(id);
  await refreshOutboxCount();
}

// durabilityLevel reports what this browser has promised about the queue, so
// that a record filed into storage that will not survive the tab says so at the
// moment it is filed rather than in a dialog nobody opens.
export function durabilityLevel() {
  return durability.level;
}
