// Unit tests for the offline app shell, run with `node --test`.
//
// The failure worth testing here is not subtle logic — it is a file that is
// loaded by the page and not listed in the worker's shell, which works
// perfectly in the office and produces a blank page on a proving ground. That
// bug is invisible to every other test in this repo, and it is introduced by
// the most ordinary change there is: adding a module.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import {
  ONLINE, OFFLINE, UNHEALTHY,
  checkHealth, connectionLabel, nextDelay,
} from "../static/offline.js";

const here = dirname(fileURLToPath(import.meta.url));
const staticDir = join(here, "..", "static");
const webDir = join(here, "..");

const indexHTML = readFileSync(join(staticDir, "index.html"), "utf8");
const swSource = readFileSync(join(staticDir, "sw.js"), "utf8");

// The shell list is read back out of the worker rather than imported from it.
// sw.js is a classic service worker script on purpose — it is the layer whose
// job is to work when things are degraded, so it is written to the oldest API
// rather than the newest — and a classic script has nothing to import from.
function shellList() {
  const match = swSource.match(/const SHELL = \[([\s\S]*?)\];/);
  assert.ok(match, "sw.js should declare a SHELL array");
  return [...match[1].matchAll(/"([^"]+)"/g)].map(m => m[1]);
}

// The files index.html pulls in on its own account: stylesheets, the module
// entry point, the manifest, the icons.
function referencedAssets() {
  return [...indexHTML.matchAll(/(?:href|src)="([^"]+)"/g)]
    .map(m => m[1])
    .filter(ref => !ref.startsWith("#") && !ref.startsWith("data:"));
}

// A path in the shell list, as a file on disk. "/" is the same document as
// "/index.html" — a tester who opens the bare origin has to get the page too.
function shellPathToFile(path) {
  const rel = path === "/" ? "index.html" : path.replace(/^\//, "");
  return join(staticDir, rel);
}

// --- The shell covers what the page actually loads ---

test("every file in the shell exists", () => {
  for (const path of shellList()) {
    assert.ok(existsSync(shellPathToFile(path)),
      `sw.js precaches ${path}, which is not in web/static — offline it would fail to install`);
  }
});

test("every asset index.html loads is in the shell", () => {
  const shell = new Set(shellList());
  for (const ref of referencedAssets()) {
    if (/^https?:\/\//.test(ref)) continue; // covered by its own test below
    const path = ref.startsWith("/") ? ref : `/${ref}`;
    assert.ok(shell.has(path),
      `index.html loads ${ref}, which sw.js does not precache — the page would be broken offline`);
  }
});

// Every module app.js pulls in is loaded by the page just as surely as if
// index.html named it, and is just as invisible when it is missing.
test("every module the entry point imports is in the shell", () => {
  const shell = new Set(shellList());
  const seen = new Set();

  const walk = file => {
    if (seen.has(file)) return;
    seen.add(file);
    const source = readFileSync(join(staticDir, file), "utf8");
    for (const [, spec] of source.matchAll(/from\s+"\.\/([^"]+)"/g)) {
      assert.ok(shell.has(`/${spec}`),
        `${file} imports ${spec}, which sw.js does not precache`);
      walk(spec);
    }
  };

  walk("app.js");
  assert.ok(seen.size > 1, "app.js should import something; the walk found nothing");
});

test("the page asks no other host for anything", () => {
  const external = referencedAssets().filter(ref => /^https?:\/\//.test(ref));
  assert.deepEqual(external, [],
    "an external asset is unreachable behind a firewall and on a campaign; vendor it instead");
});

// embed.go uses a glob, so Go picks new files up on its own. The Bazel target
// lists them one by one, and a file missing from that list is served in
// `go run` and missing from the container.
test("every static file is embedded by the Bazel target", () => {
  const build = readFileSync(join(webDir, "BUILD.bazel"), "utf8");
  for (const name of readdirSync(staticDir)) {
    assert.ok(build.includes(`"static/${name}"`),
      `web/static/${name} is not in web/BUILD.bazel embedsrcs — it would be missing from a Bazel build`);
  }
});

// --- What the header says ---

test("the three connection states read differently", () => {
  assert.equal(connectionLabel(ONLINE).text, "Connected");
  assert.equal(connectionLabel(OFFLINE).text, "Offline");
  // The server refusing to serve is not the same as this device having no
  // network, and a tester deciding whether to keep working needs to tell them
  // apart: one of them waiting will fix.
  assert.equal(connectionLabel(UNHEALTHY).text, "Unhealthy");
  assert.equal(connectionLabel(ONLINE).className, "health-ok");
  assert.equal(connectionLabel(UNHEALTHY).className, "health-fail");
});

test("checks back off while they keep failing, and never stop", () => {
  assert.equal(nextDelay(ONLINE, 0), 5000);
  assert.equal(nextDelay(ONLINE, 9), 5000, "one success returns to the short interval");

  assert.equal(nextDelay(OFFLINE, 1), 5000);
  assert.equal(nextDelay(OFFLINE, 2), 10000);
  assert.equal(nextDelay(OFFLINE, 3), 20000);

  // Capped, and finite however long it has been failing: the point of the
  // indicator is to notice the moment a signal comes back.
  assert.equal(nextDelay(OFFLINE, 40), 60000);
  assert.ok(Number.isFinite(nextDelay(OFFLINE, 1000)));
});

// --- Asking the server how it is ---

test("a browser that reports no network is not asked twice", async () => {
  let called = false;
  const state = await checkHealth(() => { called = true; }, () => false);

  assert.equal(state, OFFLINE);
  assert.equal(called, false,
    "navigator.onLine saying no is right, and asking anyway costs a failed request every few seconds");
});

test("a server that answers badly is unhealthy, not offline", async () => {
  const state = await checkHealth(async () => ({ ok: false }), () => true);
  assert.equal(state, UNHEALTHY);
});

test("a request that cannot complete is offline", async () => {
  // The captive-portal case: the browser believes it has a network and the
  // request goes nowhere.
  const state = await checkHealth(async () => { throw new Error("network"); }, () => true);
  assert.equal(state, OFFLINE);
});

test("a server that answers is online", async () => {
  const state = await checkHealth(async () => ({ ok: true }), () => true);
  assert.equal(state, ONLINE);
});
