// What the page is made of, kept so that it opens with no server to ask.
//
// This caches the application and nothing else. No evidence, no search result,
// no analytics window is ever stored here: a record answered from a cache is a
// claim about the archive that may have been true last Tuesday, and a tester
// cannot tell it from a live one. Offline, a question about the archive gets an
// error, which is the truthful answer. What offline support means in this store
// is that the page loads and a record can be *written* (docs/offline-support-plan.md),
// not that the archive is available in the field.
//
// A classic worker rather than a module one. This is the layer whose whole job
// is to work when things are degraded, so it is written to the oldest service
// worker API rather than the newest, and the shell list lives inline instead of
// being imported. web/tests/offline_shell_test.mjs reads the list back out of
// this file to check it against what the page actually loads.

const VERSION = "v1";
const SHELL_CACHE = `evidence-shell-${VERSION}`;

// Everything the page needs before it can render anything at all. Adding a
// module to index.html and forgetting to add it here would produce a page that
// works in the office and is blank on a proving ground, which is the failure
// this file exists to prevent — so a test fails instead.
const SHELL = [
  "/",
  "/index.html",
  "/pico.min.css",
  "/style.css",
  "/app.js",
  "/blobref.js",
  "/access.js",
  "/addform.js",
  "/analytics.js",
  "/common.js",
  "/datalists.js",
  "/datepicker.js",
  "/datetime.js",
  "/evidencetype.js",
  "/editing.js",
  "/images.js",
  "/location.js",
  "/markdown.js",
  "/offline.js",
  "/outbox.js",
  "/outboxview.js",
  "/search.js",
  "/sync.js",
  "/utcpreview.js",
  "/weather.js",
  "/manifest.webmanifest",
  "/icon.svg",
  "/icon-192.png",
  "/icon-512.png",
  "/apple-touch-icon.png",
];

// Paths that must always reach the server, whatever happens. The API is the
// obvious one; the login flows matter just as much, because a cached redirect
// is a login that can never complete.
// /version is here for a different reason from the rest: it would cache
// perfectly well, and a cached one is exactly the problem. The version is what
// somebody reads out when they are already confused, and a remembered answer
// would name a build that may have been replaced since.
const NEVER_CACHED = ["/api/", "/auth/", "/healthz", "/version"];

self.addEventListener("install", event => {
  event.waitUntil(
    caches.open(SHELL_CACHE)
      .then(cache => cache.addAll(SHELL))
      // Take over straight away. The alternative — waiting for every tab to
      // close — means a tester who reloads before leaving still carries the
      // previous version into the field.
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", event => {
  event.waitUntil(
    caches.keys()
      .then(names => Promise.all(
        names.filter(name => name.startsWith("evidence-shell-") && name !== SHELL_CACHE)
          .map(name => caches.delete(name)),
      ))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", event => {
  const request = event.request;

  // A GET is the only thing that can be answered from a cache. A POST that
  // cannot reach the server has to fail here so the page can queue it, which
  // is what phase 3 of the plan does with the answer.
  if (request.method !== "GET") return;

  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  if (NEVER_CACHED.some(prefix => url.pathname.startsWith(prefix))) return;

  // A navigation gets the network first, so a deploy reaches an online tester
  // on their next load rather than whenever the worker happens to update. With
  // no network it gets the shell, which is the whole point.
  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request).catch(() => caches.match("/index.html", { cacheName: SHELL_CACHE })),
    );
    return;
  }

  event.respondWith(staleWhileRevalidate(request));
});

// Answer from the cache at once, and replace it with whatever the network says
// for next time.
//
// Cache-first would strand a tester on an old app until the worker itself
// changed; network-first would make every asset wait out a timeout on a link
// that is technically connected and effectively not, which is the normal state
// of a phone at a proving ground.
async function staleWhileRevalidate(request) {
  const cache = await caches.open(SHELL_CACHE);
  const cached = await cache.match(request);

  const network = fetch(request)
    .then(response => {
      // Only a real, complete answer is worth keeping. An opaque response has
      // no status to read, and caching a 404 would make one deploy's mistake
      // permanent.
      if (response.ok && response.type === "basic") {
        cache.put(request, response.clone());
      }
      return response;
    })
    .catch(() => undefined);

  if (cached) return cached;

  const response = await network;
  if (response) return response;

  // Nothing cached and nothing reachable: say so in a way a fetch caller can
  // read, rather than throwing something that surfaces as "Failed to fetch".
  return new Response("offline and not in the cache", {
    status: 503,
    statusText: "Offline",
    headers: { "Content-Type": "text/plain" },
  });
}
