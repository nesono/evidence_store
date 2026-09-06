import {
  API_BASE, esc, getStoredAPIKey, goToLogin, logout, mayDo,
  promptForAPIKey, setAuthMode, signedOutOnPurpose,
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
import { beginCorrection, mountAddForm, pinSourceToCaller } from "./addform.js";
import { mountOutbox, runSync } from "./outboxview.js";

// Who is signed in. Learned from /me at startup and handed to the two views
// that need it, so the answer has one home rather than a copy in each.
let currentSubject = null;

// --- Tabs ---

document.querySelectorAll(".nav-tab").forEach(tab => {
  tab.addEventListener("click", (e) => {
    e.preventDefault();
    // A tab shown but not granted. Inert rather than absent, so somebody can
    // see the store has the feature and that they are not permitted it.
    if (tab.classList.contains("unavailable")) return;
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
// --- What this caller may do ---

// Hide Add Result from somebody who cannot file one.
//
// Carol, a viewer, was shown the tab and could fill the whole form in before
// the store refused her — which is the worst moment to find out, and reads as a
// broken store rather than a permission she does not hold.
//
// Hidden only when we positively know she lacks the permission. An anonymous
// caller, a store with no authentication configured, and a tab that has gone
// offline all report no permissions for quite different reasons, and hiding the
// form in the last of those would take away offline capture — the one case
// where filing a result matters most and the server cannot be asked.
// Which tab needs what, and what to say when somebody has not got it.
//
// Dan — authenticated, in no mapped group, granted nothing — used to be shown
// every tab and met a raw error from each: one in the results table, another
// in Analytics the moment he pressed Apply. Being permitted nothing is a
// deliberate state in this store and not a fault, so reporting it as one both
// contradicts the design and sends a tester hunting a problem that is not
// there (#152).
const TAB_PERMISSIONS = [
  ["search-tab-item", "evidence:read", "Searching evidence needs the viewer role"],
  ["analytics-tab-item", "analytics:read", "Analytics needs the viewer role"],
  ["add-tab-item", "evidence:write", "Filing a result needs the contributor role"],
];

function markUnavailableTabs(me) {
  for (const [id, permission, why] of TAB_PERMISSIONS) {
    if (mayDo(me, permission)) continue;
    const item = document.getElementById(id);
    if (!item) continue;
    item.querySelector(".nav-tab")?.classList.add("unavailable");
    item.title = why;
  }
}

// --- Who is signed in ---

// Answered in the header, beside the logout button, because "am I still the
// person I think I am, and what does that let me do?" is a question a tester
// should not have to open a tab to answer — particularly where one store is
// reached by several people from the same bench.
function showIdentity(me) {
  const slot = document.getElementById("auth-identity");
  if (!slot || !me.authenticated || !me.subject) return;

  // The subject carries a "user:" prefix that means something to the store and
  // nothing to a reader.
  const name = me.subject.replace(/^user:/, "");
  const roles = (me.roles || []).length
    ? me.roles.join(", ")
    // Authenticated and granted nothing is a real state, not a failure, and
    // saying so plainly beats an empty space that reads like a bug.
    : "no roles";
  slot.innerHTML = `${esc(name)} <span class="auth-roles">(${esc(roles)})</span>`;
  slot.hidden = false;
}

// Reached by somebody the identity provider admitted and this store has
// granted nothing. Says so, rather than letting the failed search speak for it.
function showNoAccess() {
  const tbody = document.getElementById("results-body");
  if (!tbody) return;
  tbody.innerHTML =
    `<tr><td colspan="9" class="empty-state">Your account has no access to this store yet.` +
    ` Ask an administrator to grant you a role.</td></tr>`;
}

// --- Signed out ---

// Reached by logging out. The table is the page's main surface, so it is where
// the answer belongs: an empty one with no explanation reads as a store with
// nothing in it.
function showSignedOut() {
  const tbody = document.getElementById("results-body");
  if (!tbody) return;
  tbody.innerHTML =
    `<tr><td colspan="9" class="empty-state">Signed out. Log in to search the archive.</td></tr>`;
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
  markUnavailableTabs(me);
  showIdentity(me);
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
  mountSearch();
  mountAddForm({ subject: () => currentSubject });

  const { filters, detail } = readStateFromURL();
  applyURLState(filters);
  wireUtcPreviews();

  // Always search. The window is a view onto the whole result set, so an empty
  // filter set is a legitimate query — "everything" — not a prompt to fill the
  // form in. Only showing results once a filter is set used to leave any link
  // without one, including a shared deep link, rendering an empty table.
  //
  // Except straight after logging out, where the search would be a 401 and the
  // table would report it as an error. Somebody who just logged out has not hit
  // a fault; say what happened instead.
  if (signedOutOnPurpose() && !me.authenticated) {
    showSignedOut();
  } else if (!mayDo(me, "evidence:read")) {
    // Searching would be a 403, and the table would report it as an error. The
    // account is not broken; it simply has not been given anything yet, and
    // whoever is looking at it needs to know to go and ask.
    showNoAccess();
  } else {
    await doSearch(filters);
  }

  if (detail) {
    try {
      renderDetail(await fetchEvidenceById(detail));
    } catch { /* record may have been deleted; leave the window as it is */ }
  }
})();
