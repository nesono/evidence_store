// Shared between the search/add view (app.js) and the analytics view
// (analytics.js). Extracted so the two can use the same API client and escaping
// without importing each other, which would make the module graph cyclic.

export const API_BASE = "/api/v1";

// --- Auth ---

const API_KEY_STORAGE = "evidence_api_key";

// Where a logout lands when there is no identity provider to visit on the way,
// and the marker the landing page is recognised by.
export const SIGNED_OUT_PATH = "/?signed_out=1";

// signedOutOnPurpose reports whether this page was reached by logging out.
//
// It governs whether a 401 means "your session expired, go and log in" or "yes,
// that is exactly what logging out looks like", and lets a view say so rather
// than show an empty table it failed to fill.
//
// Read on demand rather than at load, and guarded: this module is imported by
// the unit tests under node, where there is no window to ask.
let signedOut = null;
export function signedOutOnPurpose() {
  if (signedOut === null) {
    try {
      signedOut = new URLSearchParams(window.location.search).has("signed_out");
    } catch {
      signedOut = false;
    }
  }
  return signedOut;
}

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

// Which login flows this deployment has. With one there is somewhere to go;
// with two the page has a choice to offer rather than a guess to make.
let loginMethods = [];

const LOGIN_PATHS = { oidc: "/auth/login", saml: "/auth/saml/login" };
const LOGIN_LABELS = { oidc: "Log in with SSO", saml: "Log in with SAML" };

// Whether *this* page is authenticated by a session rather than a pasted key.
// It decides two things: that writes carry a CSRF token, and that the header
// offers logging out rather than clearing a key.
let usingSession = false;

export function setAuthMode({ sso, session, methods }) {
  ssoEnabled = !!sso;
  usingSession = !!session;
  loginMethods = (methods || []).filter(m => m in LOGIN_PATHS);
  updateAuthUI();
}

export function availableLoginMethods() {
  return loginMethods.slice();
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

// goToLogin sends the browser to a login flow.
//
// A full navigation, not fetch: the flow is a series of redirects through the
// identity provider, and in SAML's case a form the provider posts back — none
// of which can happen inside an XHR.
//
// With two providers configured and no preference given, ask rather than
// guess: sending somebody to the wrong directory produces a login screen they
// have no account on, which reads as the store being broken.
export function goToLogin(method) {
  const chosen = method || (loginMethods.length === 1 ? loginMethods[0] : null);
  if (!chosen) {
    showLoginChoice();
    return;
  }
  window.location.href = LOGIN_PATHS[chosen] || LOGIN_PATHS.oidc;
}

function showLoginChoice() {
  const dialog = document.getElementById("login-choice-dialog");
  if (!dialog) {
    // No dialog on the page: better to reach the first provider than nothing.
    window.location.href = LOGIN_PATHS[loginMethods[0]] || LOGIN_PATHS.oidc;
    return;
  }
  const list = document.getElementById("login-choice-list");
  list.innerHTML = "";
  for (const method of loginMethods) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = LOGIN_LABELS[method] || method;
    button.addEventListener("click", () => goToLogin(method));
    list.appendChild(button);
  }
  dialog.showModal();
}

// Wrapper around fetch that carries whichever credential this page has, and
// deals with a 401 in whichever way the deployment allows.
// A background sync must not throw a tester out of a half-written record and
// off to an identity provider. It asks for the 401 instead and puts a "sign in
// to send these" line in the outbox, where somebody can act on it when they
// are ready. See sync.js.
export async function apiFetchNoRedirect(url, options = {}) {
  return apiFetch(url, { ...options, redirectOnAuth: false });
}

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

  const { redirectOnAuth = true, ...fetchOptions } = options;
  const resp = await fetch(url, fetchOptions);
  if (resp.status !== 401) return resp;
  if (!redirectOnAuth) return resp;

  // Logged out, or the session expired while the tab was open. Sending
  // somebody to the identity provider is a better answer than asking a human
  // for an API key they probably do not have.
  //
  // Unless they have just logged out on purpose. Every view here opens with a
  // request, and with authentication configured an anonymous one is a 401, so
  // without this the page bounces back to the provider — which still has its
  // own session and signs them straight back in. Logging out would be
  // impossible: the button would appear to do nothing at all.
  if (ssoEnabled && !key && !signedOutOnPurpose()) {
    goToLogin();
    return resp;
  }

  const newKey = promptForAPIKey("Authentication required. Enter your API key:");
  if (newKey) {
    return fetch(url, { ...fetchOptions, headers: { ...options.headers, Authorization: `Bearer ${newKey}` } });
  }
  return resp;
}

// Ends the session server-side, which is what makes logging out mean
// something: the row goes, so the cookie stops resolving even if a copy of it
// survives somewhere.
export async function logout() {
  let next = SIGNED_OUT_PATH;
  if (usingSession) {
    const resp = await fetch("/auth/logout", {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
    });
    // Where to go next is the server's to say: it knows whether the identity
    // provider has a session of its own to end. Ending only ours would leave
    // the provider still signed in, so the next login is answered without a
    // password and the person who just logged out is silently logged back in.
    if (resp.ok) {
      try {
        const body = await resp.json();
        if (body && body.logout_url) next = body.logout_url;
      } catch {
        // An older server answered 204 with no body. Landing signed out here
        // is still better than landing on a page that logs us back in.
      }
    }
  }
  setStoredAPIKey("");
  window.location.href = next;
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
