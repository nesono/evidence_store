// Driving a real browser over the Chrome DevTools Protocol.
//
// The plumbing every smoke check needs and none of them should repeat: find a
// Chrome, start it headless, talk to a tab, and put it back afterwards.
//
// No dependencies, deliberately. The rest of this repo's frontend testing is
// `node --test` against plain modules with nothing installed, and a smoke test
// that needed a package manager would be the reason nobody ran it.

import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";

const PORT = Number(process.env.SMOKE_CDP_PORT || 9223);

// Where a Chrome tends to be. CHROME overrides, which is what a CI image with
// its own path should set.
const CANDIDATES = [
  process.env.CHROME,
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
].filter(Boolean);

export function findChrome() {
  const found = CANDIDATES.find(p => existsSync(p));
  if (!found) {
    throw new Error(
      `no Chrome found. Tried:\n  ${CANDIDATES.join("\n  ")}\n` +
      "Set CHROME to the binary to use.");
  }
  return found;
}

// launchChrome starts a headless browser in a throwaway profile.
//
// Its own profile every time: these checks clear service workers and IndexedDB,
// and doing that to somebody's real browser would be rude.
export async function launchChrome({ userDataDir } = {}) {
  const bin = findChrome();
  const dir = userDataDir || `/tmp/evidence-smoke-${process.pid}`;
  const child = spawn(bin, [
    "--headless=new",
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${dir}`,
    "--no-first-run",
    "--disable-background-networking",
  ], { stdio: "ignore", detached: false });

  for (let i = 0; i < 60; i++) {
    try {
      const r = await fetch(`http://localhost:${PORT}/json/version`);
      if (r.ok) return { close: () => child.kill() };
    } catch { /* not up yet */ }
    await sleep(250);
  }
  child.kill();
  throw new Error(`Chrome did not open a debugging port on ${PORT} within 15s`);
}

// connect opens a session against a fresh tab.
export async function connect(url) {
  const created = await fetch(
    `http://localhost:${PORT}/json/new?${encodeURIComponent(url)}`, { method: "PUT" });
  const target = await created.json();
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve);
    ws.addEventListener("error", () => reject(new Error("could not attach to the tab")));
  });

  let id = 0;
  const pending = new Map();
  // Everything the page complained about, so a check can fail on a console
  // error it did not think to look for. Four real breaks during #124 announced
  // themselves here and nowhere else.
  const problems = [];

  ws.addEventListener("message", event => {
    const m = JSON.parse(event.data);
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m);
      pending.delete(m.id);
      return;
    }
    if (m.method === "Runtime.exceptionThrown") {
      const d = m.params.exceptionDetails;
      problems.push(`${d.url || "?"}:${d.lineNumber}  ${d.exception?.description || d.text}`);
    }
    if (m.method === "Runtime.consoleAPICalled" && m.params.type === "error") {
      problems.push("console.error: " + m.params.args.map(a => a.value ?? a.description).join(" "));
    }
  });

  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const n = ++id;
    const timer = setTimeout(
      () => { pending.delete(n); reject(new Error(`${method} timed out`)); }, 15000);
    pending.set(n, m => {
      clearTimeout(timer);
      if (m.error) reject(new Error(`${method}: ${m.error.message}`));
      else resolve(m.result);
    });
    ws.send(JSON.stringify({ id: n, method, params }));
  });

  await send("Page.enable");
  await send("Runtime.enable");
  await send("Network.enable");

  const session = {
    send,
    problems,

    // eval runs an expression in the page and hands back its value. An
    // exception in the page becomes an exception here, rather than a result
    // object a check might forget to look inside.
    async eval(expression) {
      const r = await send("Runtime.evaluate",
        { expression, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) {
        throw new Error("page threw: " +
          (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
      }
      return r.result?.value;
    },

    async goto(target) {
      await send("Page.navigate", { url: target });
      await sleep(500);
    },

    offline(on) {
      return send("Network.emulateNetworkConditions", {
        offline: on, latency: 0,
        downloadThroughput: on ? 0 : -1, uploadThroughput: on ? 0 : -1,
      });
    },

    // waitFor polls an expression until it is true. Better than a fixed sleep:
    // the checks then fail with "waited for X" rather than intermittently.
    async waitFor(expression, { timeout = 10000, what = expression } = {}) {
      const deadline = Date.now() + timeout;
      while (Date.now() < deadline) {
        if (await session.eval(expression)) return;
        await sleep(100);
      }
      throw new Error(`waited ${timeout}ms for: ${what}`);
    },

    // A clean device: no worker serving the previous build, no queue left by an
    // earlier run. The worker cache in particular has caught people out — it
    // serves the last build until it updates, which looks exactly like a change
    // that did not work.
    async reset() {
      await session.eval(`(async () => {
        for (const r of await navigator.serviceWorker.getRegistrations()) await r.unregister();
        for (const k of await caches.keys()) await caches.delete(k);
        await new Promise(done => {
          const req = indexedDB.deleteDatabase('evidence-outbox');
          req.onsuccess = done; req.onerror = done; req.onblocked = done;
        });
        localStorage.removeItem('evidence_templates');
        return true;
      })()`);
    },

    close: () => ws.close(),
  };
  return session;
}

// serverIsUp reports whether there is something to test against, so a check can
// say so instead of failing on a blank page.
export async function serverIsUp(base) {
  try {
    const r = await fetch(`${base}/healthz`, { signal: AbortSignal.timeout(3000) });
    return r.ok;
  } catch {
    return false;
  }
}

export const BASE = process.env.SMOKE_BASE_URL || "http://localhost:8000";
