// Shared between the search/add view (app.js) and the analytics view
// (analytics.js). Extracted so the two can use the same API client and escaping
// without importing each other, which would make the module graph cyclic.

export const API_BASE = "/api/v1";

// --- Auth ---

const API_KEY_STORAGE = "evidence_api_key";

export function getStoredAPIKey() {
  return localStorage.getItem(API_KEY_STORAGE) || "";
}

export function setStoredAPIKey(key) {
  if (key) {
    localStorage.setItem(API_KEY_STORAGE, key);
  } else {
    localStorage.removeItem(API_KEY_STORAGE);
  }
  updateAuthUI();
}

export function updateAuthUI() {
  const logoutBtn = document.getElementById("auth-logout");
  const loginBtn = document.getElementById("auth-login");
  const signedIn = usingSession || !!getStoredAPIKey();

  if (logoutBtn) {
    logoutBtn.hidden = !signedIn;
    // "Logout" ends a session; clearing a pasted key is a different thing and
    // should not claim to be the same one.
    logoutBtn.textContent = usingSession ? "Log out" : "Clear API Key";
  }
  if (loginBtn) {
    loginBtn.hidden = signedIn;
    loginBtn.textContent = ssoEnabled ? "Log in" : "Set API Key";
  }
}

export function promptForAPIKey(msg) {
  const key = prompt(msg || "Enter your API key:");
  if (key !== null) {
    setStoredAPIKey(key.trim());
  }
  return getStoredAPIKey();
}

// --- Single sign-on ---

// Whether this deployment has a login flow to send people to. Learned from
// /me at startup: until then a 401 can only mean "ask for an API key", which
// is what this page did before there was anywhere to log in.
let ssoEnabled = false;

// Whether *this* page is authenticated by a session rather than a pasted key.
// It decides two things: that writes carry a CSRF token, and that the header
// offers logging out rather than clearing a key.
let usingSession = false;

export function setAuthMode({ sso, session }) {
  ssoEnabled = !!sso;
  usingSession = !!session;
  updateAuthUI();
}

export function isUsingSession() {
  return usingSession;
}

const CSRF_COOKIE = "evidence_csrf";

// The readable half of the double submit. The session cookie itself is
// HttpOnly and deliberately unreadable here; this one exists to be echoed
// back, which is what proves the request came from a page on this origin.
function csrfToken() {
  return document.cookie
    .split("; ")
    .find(c => c.startsWith(`${CSRF_COOKIE}=`))
    ?.slice(CSRF_COOKIE.length + 1) || "";
}

function isWrite(method) {
  return !!method && !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());
}

export function goToLogin() {
  // A full navigation, not fetch: the flow is a series of redirects through
  // the identity provider and has to happen in the address bar.
  window.location.href = "/auth/login";
}

// Wrapper around fetch that carries whichever credential this page has, and
// deals with a 401 in whichever way the deployment allows.
export async function apiFetch(url, options = {}) {
  const key = getStoredAPIKey();
  if (key) {
    options.headers = { ...options.headers, Authorization: `Bearer ${key}` };
  }
  if (isWrite(options.method)) {
    const token = csrfToken();
    // Sent whenever there is one to send. A bearer-authenticated request is
    // not checked for it, so an unnecessary header is harmless, and this
    // avoids the page having to know which credential won.
    if (token) options.headers = { ...options.headers, "X-CSRF-Token": token };
  }

  const resp = await fetch(url, options);
  if (resp.status !== 401) return resp;

  // Logged out, or the session expired while the tab was open. Sending
  // somebody to the identity provider is a better answer than asking a human
  // for an API key they probably do not have.
  if (ssoEnabled && !key) {
    goToLogin();
    return resp;
  }

  const newKey = promptForAPIKey("Authentication required. Enter your API key:");
  if (newKey) {
    options.headers = { ...options.headers, Authorization: `Bearer ${newKey}` };
    return fetch(url, options);
  }
  return resp;
}

// Ends the session server-side, which is what makes logging out mean
// something: the row goes, so the cookie stops resolving even if a copy of it
// survives somewhere.
export async function logout() {
  if (usingSession) {
    await fetch("/auth/logout", {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
    });
  }
  setStoredAPIKey("");
  window.location.href = "/";
}

// --- Formatting ---

export function esc(str) {
  const d = document.createElement("div");
  d.textContent = str;
  return d.innerHTML;
}

export function formatTime(iso) {
  const d = new Date(iso);
  const pad = n => String(n).padStart(2, "0");
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
}

export function resultBadge(result) {
  const cls = result ? result.toLowerCase() : "unknown";
  return `<span class="badge badge-${cls}">${result || "?"}</span>`;
}
