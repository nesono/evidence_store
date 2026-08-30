import {
  API_BASE,
  apiFetch,
  apiFetchNoRedirect,
  esc,
  formatTime,
  getStoredAPIKey,
  goToLogin,
  logout,
  promptForAPIKey,
  setAuthMode,
} from "./common.js";
import { showAnalytics } from "./analytics.js";
import { mount as mountAccess, showAccess } from "./access.js";
import { parseUserDateTime } from "./datetime.js";
import { updateUtcPreview, wireUtcPreviews } from "./utcpreview.js";
import { renderMarkdown } from "./markdown.js";
import { attachImageUploads, hydrateImages } from "./images.js";
import { formatAccuracy, formatCoordinates, requestPosition } from "./location.js";
import { composeWeather, describeReading, fetchWeather, weatherPoint } from "./weather.js";
import {
  OFFLINE, announceUpdates, connectionState, followHashChanges, offerInstall,
  openTabFromHash, registerServiceWorker, startConnectionIndicator,
} from "./offline.js";
import { newEntry } from "./outbox.js";
import { setValue } from "./editing.js";
import { applyTemplate, mountTemplates } from "./templates.js";
import { refreshDatalists } from "./datalists.js";
import {
  applyURLState, doSearch, fetchEvidenceById, mountSearch, readStateFromURL, renderDetail,
} from "./search.js";
import {
  durabilityLevel, dropQueued, mountOutbox, openOutbox, queueRecord, runSync,
} from "./outboxview.js";

// --- Tabs ---

document.querySelectorAll(".nav-tab").forEach(tab => {
  tab.addEventListener("click", (e) => {
    e.preventDefault();
    const target = tab.dataset.tab;
    document.querySelectorAll(".nav-tab").forEach(t => t.classList.remove("active"));
    tab.classList.add("active");
    document.querySelectorAll(".tab-content").forEach(s => s.hidden = true);
    document.getElementById(`tab-${target}`).hidden = false;
    if (target === "analytics") showAnalytics();
    if (target === "access") showAccess();
  });
});

// --- Add Evidence ---

async function submitEvidence(andAnother) {
  const form = document.getElementById("add-form");
  const feedback = document.getElementById("add-feedback");

  if (!form.checkValidity()) { form.reportValidity(); return; }

  let finishedAt;
  const rawFinished = form.finished_at.value.trim();
  if (rawFinished) {
    const d = parseUserDateTime(rawFinished);
    if (!d) {
      feedback.innerHTML = `<p class="feedback-error">Invalid date format. Use YYYY-MM-DD HH:MM (UTC)</p>`;
      return;
    }
    finishedAt = d.toISOString();
  } else {
    finishedAt = new Date().toISOString();
  }

  const metadata = {};
  const tags = form.tags.value.trim();
  if (tags) metadata.tags = tags.split(",").map(t => t.trim()).filter(Boolean);
  const notes = form.notes.value.trim();
  if (notes) metadata.notes = notes;
  // `observations` is the field DESIGN.md gives the manual evidence type for the
  // tester's own account of the run.
  const observations = form.observations.value.trim();
  if (observations) metadata.observations = observations;
  const location = form.location.value.trim();
  if (location) {
    metadata.location = location;
    // The margin belongs to the fix, not to the field: it is filed only while
    // the text is still the one the device produced. Typing over it — even to
    // correct it — makes it a place the tester named, and a metre count from a
    // reading that is no longer shown would be a claim about someone else's.
    const accuracy = Number(form.location.dataset.accuracyM);
    if (form.location.dataset.fromDevice === location && Number.isFinite(accuracy)) {
      metadata.location_accuracy_m = accuracy;
    }
  }
  const weather = form.weather_conditions.value.trim();
  if (weather) {
    metadata.weather_conditions = weather;
    // Which hour the reading was for is filed only while the line is still the
    // service's, the same rule the location's accuracy follows: an edited line
    // is the tester's account of the weather, and an hour attached to it would
    // dress that up as a reading nobody can go back and check.
    if (form.weather_conditions.dataset.fromService === weather &&
        form.weather_conditions.dataset.observedAt) {
      metadata.weather_observed_at = form.weather_conditions.dataset.observedAt;
    }
  }
  Object.assign(metadata, readCustomFields());

  const record = {
    repo: form.repo.value.trim(),
    branch: form.branch.value.trim(),
    rcs_ref: form.rcs_ref.value.trim(),
    procedure_ref: form.procedure_ref.value.trim(),
    evidence_type: form.evidence_type.value.trim(),
    source: form.source.value.trim(),
    result: form.querySelector('[name="result"]:checked').value,
    finished_at: finishedAt,
  };
  if (Object.keys(metadata).length > 0) record.metadata = metadata;

  feedback.innerHTML = "";
  const btn = form.querySelector('button[type="submit"]');
  btn.setAttribute("aria-busy", "true");

  // The submission gets its identity here, at capture, whether or not it is
  // about to reach anything. If it is queued and sent days later, it goes with
  // the same token, so a retry files one record rather than two.
  //
  // Editing a queued record reuses its token: the corrected version is the
  // same submission, not a second one.
  const entry = newEntry(record, { id: editingEntryID || undefined, capturedBy: currentSubject });

  try {
    if (connectionState() === OFFLINE) {
      // Do not spend a timeout finding out what the header already says.
      await queueEntry(entry, feedback, andAnother, form);
      return;
    }

    const resp = await apiFetch(`${API_BASE}/evidence`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(entry.record),
    });
    const data = await resp.json();
    if (!resp.ok) {
      const msg = data.errors ? data.errors.map(e => e.message || e).join(", ") : (data.error || JSON.stringify(data));
      feedback.innerHTML = `<p class="feedback-error">${esc(msg)}</p>`;
      return;
    }
    // Filed, so it is not waiting any more. This matters when the record came
    // back out of the outbox to be corrected: the queued copy goes now.
    if (editingEntryID) {
      await dropQueued(editingEntryID);
      editingEntryID = null;
    }
    refreshDatalists();
    if (andAnother) {
      feedback.innerHTML = `<p class="feedback-ok">Created <code>${data.id}</code></p>`;
      resetFormForNext(form);
    } else {
      // Submit ends the sitting, and a weather line does not keep: it is a
      // reading for one hour, and the next record filled in on this form could
      // be days later. Leaving it in the box is how yesterday's sky ends up
      // filed as today's.
      clearWeather();
      feedback.innerHTML = `<p class="feedback-ok">Created <code>${data.id}</code> &mdash; switching to search...</p>`;
      setTimeout(() => {
        document.querySelector('[data-tab="search"]').click();
        feedback.innerHTML = "";
      }, 1000);
    }
  } catch (err) {
    // The post did not reach the store. That is not a reason to lose a record
    // somebody was standing in a field to write, so it goes in the queue and
    // the tester is told where it went. The token it already carries is what
    // makes sending it again safe, even if this attempt did in fact land.
    await queueEntry(entry, feedback, andAnother, form, err);
  } finally {
    btn.removeAttribute("aria-busy");
  }
}

// queueEntry puts a record in the outbox and says so plainly. A tester has to
// be able to tell "filed" from "waiting" at a glance — they are different
// states of the evidence, and only one of them is safe to walk away from.
async function queueEntry(entry, feedback, andAnother, form, err) {
  await queueRecord(entry);
  editingEntryID = null;

  const why = err ? `Could not reach the store (${esc(err.message)}).` : "Offline.";
  feedback.innerHTML =
    `<p class="feedback-ok">${why} Saved here and it will be sent when there is a connection ` +
    `&mdash; <a href="#" class="outbox-open">see what is waiting</a>.</p>` +
    // Said at the moment the tester first relies on the queue, not buried in a
    // dialog they may never open. A private window is the usual reason, and
    // finding out afterwards means finding out by losing a day of evidence.
    (durabilityLevel() === "session"
      ? `<p class="feedback-error">This browser is not storing anything on disk &mdash; ` +
        `these records will be gone when you close this tab. Send them before you do.</p>`
      : "");
  feedback.querySelector(".outbox-open").addEventListener("click", event => {
    event.preventDefault();
    openOutbox();
  });

  if (andAnother) resetFormForNext(form);
}

// resetFormForNext clears what belongs to the run just filed and keeps what
// belongs to the sitting.
//
// Location and weather are deliberately kept: a tester filing several runs in a
// row is still standing where they were, under the same sky, and looking both
// up again for each one is how the fields stop being filled. The reading is an
// hour wide, which is longer than a burst of manual records takes.
function resetFormForNext(form) {
  const chosen = form.querySelector('[name="result"]:checked');
  if (chosen) chosen.checked = false;
  form.notes.value = "";
  form.observations.value = "";
  updateTestLogPreview();
  const currentTpl = document.getElementById("template-select").value;
  if (currentTpl) {
    applyTemplate(currentTpl);
  } else {
    document.getElementById("custom-fields-list").innerHTML = "";
  }
}

// --- A newer build is waiting ---

function showUpdateNotice() {
  const notice = document.getElementById("update-notice");
  if (!notice || !notice.hidden) return;
  notice.hidden = false;
  document.getElementById("update-reload").addEventListener("click", event => {
    event.preventDefault();
    window.location.reload();
  });
}

// --- The record being corrected ---

// Who is signed in, remembered so a queued record can record who wrote it.
let currentSubject = null;
// The queued record currently loaded back into the form, if any. Its token is
// reused on submit, so correcting a record replaces it rather than filing a
// second copy of the same run.
let editingEntryID = null;

// --- Which build is answering ---

// Asked for directly rather than through apiFetch: /version is public, sits
// outside /api/v1, and sending a credential to it would be sending one where
// none is wanted.
//
// A failure is silent and leaves the footer reading just "Evidence Store".
// There is nothing for a tester to do about it, and an error line about a
// version number would be noise on a page that has just failed to reach its
// server for reasons they can already see in the header.
async function showServerVersion() {
  const el = document.getElementById("server-version");
  if (!el) return;
  try {
    const resp = await fetch("/version", { cache: "no-store" });
    if (!resp.ok) return;
    const { version, source } = await resp.json();
    if (!version) return;
    el.textContent = version;
    el.title = source === "build"
      ? "The build the server is running, by the minute it was built (UTC)"
      : source === "commit"
        ? "Built from a commit made at this time (UTC); the build itself was not stamped"
        : "This server was built from a working copy, so it matches no particular commit";
  } catch {
    // Offline, or the server is not answering. The header already says so.
  }
}
function fillFormFromRecord(record) {
  const form = document.getElementById("add-form");
  const metadata = record.metadata || {};

  for (const field of ["repo", "branch", "rcs_ref", "procedure_ref", "evidence_type", "source"]) {
    if (form[field]) form[field].value = record[field] || "";
  }
  const result = form.querySelector(`[name="result"][value="${record.result}"]`);
  if (result) result.checked = true;
  if (record.finished_at) {
    form.finished_at.value = formatTime(record.finished_at);
  }
  form.tags.value = (metadata.tags || []).join(", ");
  form.notes.value = metadata.notes || "";
  form.observations.value = metadata.observations || "";
  form.location.value = metadata.location || "";
  form.weather_conditions.value = metadata.weather_conditions || "";
  updateTestLogPreview();
}

// --- Custom metadata fields ---

document.getElementById("add-custom-field").addEventListener("click", () => {
  const list = document.getElementById("custom-fields-list");
  const row = document.createElement("div");
  row.className = "custom-field-row grid";
  row.innerHTML = `
    <input type="text" placeholder="key" class="cf-key">
    <input type="text" placeholder="value" class="cf-value">
    <button type="button" class="secondary outline cf-remove">&times;</button>
  `;
  row.querySelector(".cf-remove").addEventListener("click", () => row.remove());
  list.appendChild(row);
  row.querySelector(".cf-key").focus();
});

function readCustomFields() {
  const fields = {};
  document.querySelectorAll(".custom-field-row").forEach(row => {
    const key = row.querySelector(".cf-key").value.trim();
    const val = row.querySelector(".cf-value").value.trim();
    if (key) fields[key] = val;
  });
  return fields;
}

// --- Test log preview ---

// Markdown is only worth offering if the tester can see what it will look like
// before the record is filed; afterwards the log is immutable.
function updateTestLogPreview() {
  const preview = document.getElementById("test-log-preview");
  if (preview.hidden) return;
  const raw = document.querySelector('#add-form [name="observations"]').value;
  preview.innerHTML = raw.trim()
    ? renderMarkdown(raw)
    : `<p class="test-log-empty">Nothing written yet.</p>`;
  hydrateImages(preview);
}

document.getElementById("test-log-preview-toggle").addEventListener("click", (e) => {
  const preview = document.getElementById("test-log-preview");
  preview.hidden = !preview.hidden;
  e.target.setAttribute("aria-expanded", String(!preview.hidden));
  e.target.textContent = preview.hidden ? "Preview" : "Hide preview";
  updateTestLogPreview();
});

document.querySelector('#add-form [name="observations"]')
  .addEventListener("input", updateTestLogPreview);

// Pasting a screenshot is how a tester attaches one — the alternative is
// finding a file, naming it, and uploading it somewhere else first, which is
// how photos end up not being attached at all.
attachImageUploads(
  document.querySelector('#add-form [name="observations"]'),
  msg => {
    document.getElementById("add-feedback").innerHTML =
      `<p class="feedback-error">${esc(msg)}</p>`;
  },
);

document.getElementById("fill-now").addEventListener("click", () => {
  const input = document.querySelector('#add-form [name="finished_at"]');
  // Undoably: a button that fills a field in is one a tester may want to take
  // back, and taking it back is what Cmd-Z is for (issue #83).
  setValue(input, formatTime(new Date().toISOString()));
  updateUtcPreview(input);
});

// --- Location ---

// The button fills the field rather than replacing it: what it writes is text
// the tester can correct, add a bay number to, or throw away. The field works
// with the button never pressed, which is also what happens on a desktop with
// no receiver in it.
document.getElementById("fill-location").addEventListener("click", async (e) => {
  const input = document.querySelector('#add-form [name="location"]');
  const status = document.getElementById("location-status");
  const btn = e.currentTarget;

  status.classList.remove("location-status-error");
  status.textContent = "Locating…";
  btn.setAttribute("aria-busy", "true");
  btn.disabled = true;

  try {
    const { lat, lon, accuracy } = await requestPosition();
    setValue(input, formatCoordinates(lat, lon));
    input.dataset.fromDevice = input.value;
    input.dataset.accuracyM = accuracy;
    status.textContent = formatAccuracy(accuracy)
      ? `This device's position, ${formatAccuracy(accuracy)}`
      : "This device's position";
  } catch (err) {
    status.textContent = err.message;
    status.classList.add("location-status-error");
  } finally {
    btn.removeAttribute("aria-busy");
    btn.disabled = false;
  }
});

// An edited field is the tester's own account of the place again, so the
// device's margin stops applying to it and the note about where it came from
// stops being true.
document.querySelector('#add-form [name="location"]').addEventListener("input", (e) => {
  if (e.target.dataset.fromDevice === e.target.value) return;
  delete e.target.dataset.fromDevice;
  delete e.target.dataset.accuracyM;
  const status = document.getElementById("location-status");
  status.textContent = "";
  status.classList.remove("location-status-error");
});

// --- Weather ---

// The lookup is asked for the place and hour the record already names, so it
// wants a location and a finish time filled in first — but neither is required,
// and neither is worth refusing over: an empty location falls back to the
// device, and an empty finish time means the run is happening now, which is
// what the record itself would say.
document.getElementById("fill-weather").addEventListener("click", async (e) => {
  const input = document.querySelector('#add-form [name="weather_conditions"]');
  const status = document.getElementById("weather-status");
  const btn = e.currentTarget;

  status.classList.remove("location-status-error");

  if (connectionState() === OFFLINE) {
    // The lookup is a server call by design (DESIGN.md §4.5), so there is
    // nothing to wait for. Saying so beats a spinner that ends in a timeout.
    status.textContent = "No connection, so there is nobody to ask. Write down what you can see instead.";
    status.classList.add("location-status-error");
    document.getElementById("weather-compose").hidden = false;
    updateComposePreview();
    document.getElementById("wc-description").focus();
    return;
  }

  status.textContent = "Looking up the weather…";
  btn.setAttribute("aria-busy", "true");
  btn.disabled = true;

  try {
    const location = document.querySelector('#add-form [name="location"]').value.trim();
    const point = await weatherPoint(location, requestPosition);

    // The record's own finish time, not now: a tester filing yesterday
    // evening's run wants yesterday evening's weather, and today's would be a
    // plausible-looking untruth. An unparseable box is left to the field's own
    // preview to complain about; the lookup just asks about now.
    const rawFinished = document.querySelector('#add-form [name="finished_at"]').value.trim();
    const when = rawFinished ? parseUserDateTime(rawFinished) : null;

    const reading = await fetchWeather(point, when);
    setValue(input, reading.summary);
    input.dataset.fromService = reading.summary;
    if (reading.observed_at) input.dataset.observedAt = reading.observed_at;
    status.textContent = describeReading(reading, point.source);
  } catch (err) {
    status.textContent = err.message;
    status.classList.add("location-status-error");
  } finally {
    btn.removeAttribute("aria-busy");
    btn.disabled = false;
  }
});

// --- Writing the weather down ---

// The lookup needs a server; the sky does not. Offline the button says so and
// the tester writes what they can see, which on a proving ground is better
// information anyway: they are standing in it.
const composeFields = {
  description: "wc-description",
  temperatureC: "wc-temperature",
  windKph: "wc-wind",
  humidity: "wc-humidity",
  precipitationMm: "wc-precipitation",
};

function readComposeFields() {
  const values = {};
  for (const [key, elementID] of Object.entries(composeFields)) {
    values[key] = document.getElementById(elementID).value;
  }
  return values;
}

function updateComposePreview() {
  const line = composeWeather(readComposeFields());
  document.getElementById("wc-preview").textContent = line
    ? `Will be filed as: ${line}`
    : "Fill in whatever you can see. Anything you leave empty is left out.";

  // Live, into the field itself. There is no Apply button because there is
  // nothing to apply: the boxes are a way of typing the line, and the line is
  // what is filed.
  const input = document.querySelector('#add-form [name="weather_conditions"]');
  // A plain assignment, unlike the buttons above, and deliberately. This runs on
  // every keystroke in the composer boxes, and an undoable write has to focus
  // the field it writes to — which would pull the caret out of the box being
  // typed in and back again, dozens of times, breaking IME along the way. The
  // composer's own boxes keep their undo; the line they compose is not
  // somewhere anybody is typing.
  input.value = line;
  // A written line is the tester's own account and never a reading, so the hour
  // an actual reading would have carried stays off it. That difference is what
  // lets a reader tell the two apart months later.
  delete input.dataset.fromService;
  delete input.dataset.observedAt;
}

document.getElementById("compose-weather").addEventListener("click", () => {
  const panel = document.getElementById("weather-compose");
  panel.hidden = !panel.hidden;
  if (panel.hidden) return;

  const status = document.getElementById("weather-status");
  status.classList.remove("location-status-error");
  status.textContent = "";
  updateComposePreview();
  document.getElementById("wc-description").focus();
});

for (const elementID of Object.values(composeFields)) {
  document.getElementById(elementID).addEventListener("input", updateComposePreview);
}

// Editing the line makes it the tester's account of the weather again — which
// is the point of leaving it editable, since the person who was standing there
// outranks a model that put the hailstorm two valleys away. The hour the
// service read stops applying to it at the same moment.
document.querySelector('#add-form [name="weather_conditions"]').addEventListener("input", (e) => {
  if (e.target.dataset.fromService === e.target.value) return;
  delete e.target.dataset.fromService;
  delete e.target.dataset.observedAt;
  const status = document.getElementById("weather-status");
  status.textContent = "";
  status.classList.remove("location-status-error");
});

// clearWeather empties the field and everything that was true about it.
function clearWeather() {
  const input = document.querySelector('#add-form [name="weather_conditions"]');
  input.value = "";
  delete input.dataset.fromService;
  delete input.dataset.observedAt;
  const status = document.getElementById("weather-status");
  status.textContent = "";
  status.classList.remove("location-status-error");

  const panel = document.getElementById("weather-compose");
  panel.hidden = true;
  for (const elementID of Object.values(composeFields)) {
    document.getElementById(elementID).value = "";
  }
}

document.getElementById("add-form").addEventListener("submit", (e) => {
  e.preventDefault();
  submitEvidence(false);
});

document.getElementById("add-another").addEventListener("click", () => {
  submitEvidence(true);
});

// --- Auth UI ---

document.getElementById("auth-logout")?.addEventListener("click", async (e) => {
  e.preventDefault();
  await logout();
});

document.getElementById("close-login-choice")?.addEventListener("click", () => {
  document.getElementById("login-choice-dialog").close();
});

document.getElementById("auth-login")?.addEventListener("click", (e) => {
  e.preventDefault();
  // Where there is an identity provider, that is what "log in" means. The API
  // key path stays for CI, for scripts, and for anyone who reaches this page
  // holding a key rather than an account.
  if (ssoAvailable) {
    goToLogin();
    return;
  }
  promptForAPIKey("Enter your API key:");
});

// --- Init ---

// Asking the server who we are is what lets the page offer only what this
// caller can actually do. A store with nothing configured answers
// "not authenticated", which means open rather than locked out.
// ssoAvailable is what the "log in" button branches on.
let ssoAvailable = false;

// /auth/config answers whether there is anywhere to log in. It has to be a
// separate request from /me because /me refuses an anonymous caller — which is
// precisely the caller asking the question.
async function loadAuthConfig() {
  try {
    const resp = await fetch("/auth/config");
    if (resp.ok) return await resp.json();
  } catch { /* offline or mid-restart; fall through */ }
  return { sso_enabled: false };
}

// The Source box used to be a free-text field asking a tester to type their
// own name, which the server has refused to take on trust since the source
// binding landed: anyone without source:any may only file under their own
// subject. Now that the page knows who it is, it fills the box in and locks it
// rather than letting somebody type a name that will come back a 403.
//
// A caller holding source:any — a build robot, or an admin who also holds ci —
// is left alone: writing a source that is not its own name is exactly what
// that permission is for.
function pinSourceToCaller(me) {
  const input = document.querySelector('#add-form [name="source"]');
  if (!input || !me.authenticated) return;
  if ((me.permissions || []).includes("source:any")) return;

  input.value = me.subject;
  input.readOnly = true;
  input.title = `Filed as ${me.subject}, the account you are signed in as`;

  const label = input.closest("label");
  const hint = label?.querySelector("small");
  if (hint) hint.textContent = "(you)";
}

async function loadIdentity() {
  try {
    // Plain fetch, not apiFetch: a 401 here is the ordinary state of a page
    // nobody has logged into yet, and bouncing it straight to the identity
    // provider would make the store impossible to look at anonymously — or to
    // reach with an API key.
    const key = getStoredAPIKey();
    const resp = await fetch(`${API_BASE}/me`, {
      headers: key ? { Authorization: `Bearer ${key}` } : {},
    });
    if (resp.ok) return await resp.json();
  } catch { /* offline or mid-restart; fall through */ }
  return { authenticated: false, permissions: [] };
}

(async function init() {
  startConnectionIndicator();
  showServerVersion();
  offerInstall();
  // Not awaited: the page has nothing to wait for. The worker takes over on
  // the next load, and a tester who installs this today is covered tomorrow.
  registerServiceWorker().then(registration => announceUpdates(registration, showUpdateNotice));

  const [authConfig, me] = await Promise.all([loadAuthConfig(), loadIdentity()]);
  ssoAvailable = !!authConfig.sso_enabled;
  setAuthMode({
    sso: ssoAvailable,
    session: me.via_session,
    methods: authConfig.login_methods,
  });
  mountAccess(me);
  pinSourceToCaller(me);
  // The installed app's Add Result shortcut opens "/#add". Done after Access is
  // mounted, so a fragment naming a tab this caller does not have selects
  // nothing rather than a tab that is not there.
  openTabFromHash();
  followHashChanges();
  currentSubject = me.authenticated ? me.subject : null;

  // After the identity is known, so a queued record is attributed to whoever
  // is actually signed in, and so a sync does not send somebody else's records
  // under this name.
  await mountOutbox({
    subject: () => currentSubject,
    onEdit: entry => {
      editingEntryID = entry.id;
      fillFormFromRecord(entry.record);
      document.querySelector('[data-tab="add"]').click();
      document.getElementById("add-feedback").innerHTML =
        `<p class="feedback-ok">Correcting a record that is waiting to send. ` +
        `Submitting replaces it rather than filing a second copy.</p>`;
    },
  });
  runSync();
  mountTemplates();
  mountSearch();
  refreshDatalists();
  document.querySelector('#add-form [name="finished_at"]').value = formatTime(new Date().toISOString());

  const { filters, detail } = readStateFromURL();
  applyURLState(filters);
  wireUtcPreviews();

  // Always search. The window is a view onto the whole result set, so an empty
  // filter set is a legitimate query — "everything" — not a prompt to fill the
  // form in. Only showing results once a filter is set used to leave any link
  // without one, including a shared deep link, rendering an empty table.
  await doSearch(filters);

  if (detail) {
    try {
      renderDetail(await fetchEvidenceById(detail));
    } catch { /* record may have been deleted; leave the window as it is */ }
  }
})();
