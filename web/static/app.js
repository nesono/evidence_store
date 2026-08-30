import {
  API_BASE, getStoredAPIKey, goToLogin, logout, promptForAPIKey, setAuthMode,
} from "./common.js";
import { showAnalytics } from "./analytics.js";
import { mount as mountAccess, showAccess } from "./access.js";
import { wireUtcPreviews } from "./utcpreview.js";
import {
  announceUpdates, followHashChanges, offerInstall, openTabFromHash,
  registerServiceWorker, startConnectionIndicator,
} from "./offline.js";
import {
  applyURLState, doSearch, fetchEvidenceById, mountSearch, readStateFromURL, renderDetail,
} from "./search.js";
import { mountTemplates } from "./templates.js";
import { beginCorrection, mountAddForm, pinSourceToCaller } from "./addform.js";
import { mountOutbox, runSync } from "./outboxview.js";

// Who is signed in. Learned from /me at startup and handed to the two views
// that need it, so the answer has one home rather than a copy in each.
let currentSubject = null;

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
    onEdit: beginCorrection,
  });
  runSync();
  mountTemplates();
  mountSearch();
  mountAddForm({ subject: () => currentSubject });

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
