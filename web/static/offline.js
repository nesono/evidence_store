// Whether this page can reach its server, and keeping it loadable when it
// cannot.
//
// Two jobs that belong together because they answer the same question. The
// indicator in the header is what a tester reads before deciding whether to
// keep working; the service worker is what makes the page there to read.

// How often to ask the server how it is, while the browser believes there is a
// network. Failures back off: a phone in a valley should not spend its battery
// asking a question it has already been told the answer to nine times.
const POLL_MS = 5000;
const MAX_POLL_MS = 60000;

// --- What a state looks like ---

// The three answers, and they are genuinely different. "Offline" is about this
// device, and a tester can carry on writing. "Unhealthy" is the server saying
// it cannot serve — the network is fine and the store is not, which is somebody
// else's problem and not something waiting will fix.
export const ONLINE = "online";
export const OFFLINE = "offline";
export const UNHEALTHY = "unhealthy";

export function connectionLabel(state) {
  switch (state) {
    case ONLINE:
      return { text: "Connected", className: "health-ok" };
    case UNHEALTHY:
      return { text: "Unhealthy", className: "health-fail" };
    default:
      return { text: "Offline", className: "health-fail" };
  }
}

// nextDelay spaces out the checks after repeated failures, and goes straight
// back to the short interval once something answers.
//
// It never returns Infinity, however long the failure has lasted: the whole
// point of the indicator is to notice the moment a signal comes back, and a
// worker that has given up would leave a tester in a hotel lobby believing they
// are still cut off.
export function nextDelay(state, failures) {
  if (state === ONLINE) return POLL_MS;
  return Math.min(POLL_MS * 2 ** Math.max(0, failures - 1), MAX_POLL_MS);
}

// --- Asking ---

// checkHealth reports what this device can currently reach.
//
// navigator.onLine is consulted first and trusted only when it says no: a
// browser that reports a connection may still be on a captive portal or a
// tunnel that goes nowhere, but one that reports none is right, and asking
// anyway costs a failed request and a line of console noise every few seconds.
export async function checkHealth(fetchImpl = fetch, onLine = () => navigator.onLine) {
  if (onLine() === false) return OFFLINE;
  try {
    const resp = await fetchImpl("/healthz", { cache: "no-store" });
    return resp.ok ? ONLINE : UNHEALTHY;
  } catch {
    return OFFLINE;
  }
}

// --- The indicator ---

const listeners = new Set();
let current = ONLINE;

export function connectionState() {
  return current;
}

// onConnectionChange registers a callback for transitions, and returns a
// function that removes it. Phase 3's outbox uses this to drain itself the
// moment a signal returns.
export function onConnectionChange(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function setState(state) {
  if (state === current) return;
  current = state;
  for (const fn of listeners) {
    try {
      fn(state);
    } catch (err) {
      // One bad listener must not stop the others, or stop the polling loop
      // that is calling this.
      console.error("connection listener failed", err);
    }
  }
}

export function startConnectionIndicator(el = document.getElementById("health-status")) {
  let failures = 0;
  let timer;

  const render = state => {
    if (!el) return;
    const { text, className } = connectionLabel(state);
    el.innerHTML = `<span class="health-dot ${className}"></span> ${text}`;
  };

  const tick = async () => {
    const state = await checkHealth();
    failures = state === ONLINE ? 0 : failures + 1;
    render(state);
    setState(state);
    clearTimeout(timer);
    timer = setTimeout(tick, nextDelay(state, failures));
  };

  // The browser's own events are the fastest signal there is — faster than any
  // interval — so they drive a check rather than waiting for the next one.
  window.addEventListener("online", () => { failures = 0; tick(); });
  window.addEventListener("offline", () => { failures = 1; render(OFFLINE); setState(OFFLINE); });
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") tick();
  });

  tick();
}

// --- Installing, and being told about a new version ---

// TABS is the set of tab names a URL may name. The manifest's Add Result
// shortcut opens "/#add", and a shortcut that lands on the wrong tab is worse
// than no shortcut.
const TABS = ["search", "analytics", "add", "access"];

// tabFromHash reads which tab a URL asks for, or null for anything else.
// Deliberately strict: an unknown fragment selects nothing rather than
// guessing, and the page opens where it always did.
export function tabFromHash(hash) {
  const name = String(hash || "").replace(/^#/, "").trim().toLowerCase();
  return TABS.includes(name) ? name : null;
}

// openTabFromHash follows a fragment on load, which is what makes the installed
// app's shortcut work.
export function openTabFromHash(hash = window.location.hash) {
  const tab = tabFromHash(hash);
  if (!tab) return null;
  const link = document.querySelector(`.nav-tab[data-tab="${tab}"]`);
  // Access is only mounted for an administrator; a shortcut to a tab this
  // caller does not have is silently the ordinary page.
  if (!link || link.closest("[hidden]")) return null;
  link.click();
  return tab;
}

// followHashChanges keeps the shortcut working when the app is already open.
//
// Activating a shortcut while the app is running is a same-document navigation
// — the fragment changes and nothing reloads — so the load-time call never
// happens and the tab never moves. Cold launches were fine, which is exactly
// how this would have shipped unnoticed.
export function followHashChanges() {
  window.addEventListener("hashchange", () => openTabFromHash());
}

// offerInstall shows the button that installs the app, but only where the
// browser has said it would accept one.
//
// Installing matters more here than the word suggests. On iOS it is what
// exempts the queue from Safari discarding storage for a site not visited in
// seven days, which is shorter than a campaign — so the button is not a
// nicety, it is how a tester keeps their evidence. Browsers that fire no
// beforeinstallprompt (Safari among them) get nothing: an inert button would
// be worse than the Share-menu instruction in the README.
export function offerInstall(button = document.getElementById("install-app")) {
  if (!button) return;
  let deferred = null;

  window.addEventListener("beforeinstallprompt", event => {
    // Keep the browser's own banner from appearing as well as this button.
    event.preventDefault();
    deferred = event;
    button.hidden = false;
  });

  button.addEventListener("click", async () => {
    if (!deferred) return;
    button.hidden = true;
    const prompt = deferred;
    // The event is good for one use, whatever the person chooses.
    deferred = null;
    await prompt.prompt();
  });

  window.addEventListener("appinstalled", () => {
    button.hidden = true;
    deferred = null;
  });
}

// announceUpdates says when a newer build has been fetched.
//
// It says, and does not act. The worker takes over as soon as it installs, so
// the *next* load is already the new version — but this page is still running
// the JavaScript it started with, and reloading it out from under somebody
// would throw away a half-written test log. Whoever is typing decides when.
export function announceUpdates(registration, notify) {
  if (!registration) return;
  registration.addEventListener("updatefound", () => {
    const incoming = registration.installing;
    if (!incoming) return;
    incoming.addEventListener("statechange", () => {
      // A worker reaching `installed` with something already in control means a
      // replacement, not a first visit.
      if (incoming.state === "installed" && navigator.serviceWorker.controller) {
        notify();
      }
    });
  });
}

// --- Keeping the page loadable ---

// registerServiceWorker installs the worker that serves the page with no
// server to ask.
//
// Service workers need a secure context, so this does nothing over plain HTTP
// except on localhost. That is not a limitation this code can route around —
// it says so out loud, because a deployment on an internal address over HTTP is
// exactly where somebody will wonder why offline does not work.
export function registerServiceWorker(nav = navigator, secure = window.isSecureContext) {
  if (!("serviceWorker" in nav)) return Promise.resolve(null);
  if (!secure) {
    console.warn(
      "Evidence Store: offline support needs HTTPS (or localhost). " +
      "This page is not in a secure context, so the app will not load without a connection.",
    );
    return Promise.resolve(null);
  }
  return nav.serviceWorker.register("/sw.js").catch(err => {
    // A failed registration costs offline support and nothing else, so it is a
    // warning rather than something the tester is shown.
    console.warn("Evidence Store: could not register the service worker", err);
    return null;
  });
}
