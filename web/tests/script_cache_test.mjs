import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const worker = readFileSync(new URL("../static/sw.js", import.meta.url), "utf8");

function scriptRequest(fetch) {
  const handlers = {}, stored = new Map();
  const cache = {
    async match(request) { return stored.get(request.url)?.clone(); },
    async put(request, response) { stored.set(request.url, response); },
  };
  const context = vm.createContext({
    URL, Response, fetch,
    caches: { open: async () => cache },
    self: { location: { origin: "https://example.test" }, addEventListener: (name, fn) => { handlers[name] = fn; } },
  });
  vm.runInContext(worker, context);
  return {
    stored,
    async run() {
      let response;
      handlers.fetch({
        request: { url: "https://example.test/campaigndrafts.js", method: "GET", mode: "cors" },
        respondWith: value => { response = value; },
      });
      return response;
    },
  };
}

test("scripts fetch current code before using an older cached module", async () => {
  let options;
  const harness = scriptRequest(async (_, supplied) => {
    options = supplied;
    const response = new Response("current code");
    Object.defineProperty(response, "type", { value: "basic" });
    return response;
  });
  harness.stored.set("https://example.test/campaigndrafts.js", new Response("old DOM code"));
  assert.equal(await (await harness.run()).text(), "current code");
  assert.equal(options.cache, "no-store");
  assert.equal(await harness.stored.get("https://example.test/campaigndrafts.js").text(), "current code");
});

test("scripts still load from the offline shell when the network is unavailable", async () => {
  const harness = scriptRequest(async () => { throw new TypeError("offline"); });
  harness.stored.set("https://example.test/campaigndrafts.js", new Response("offline code"));
  assert.equal(await (await harness.run()).text(), "offline code");
  harness.stored.clear();
  assert.equal((await harness.run()).status, 503);
});

test("HTTP errors are not disguised by an old module", async () => {
  const harness = scriptRequest(async () => new Response("not found", { status: 404 }));
  harness.stored.set("https://example.test/campaigndrafts.js", new Response("old DOM code"));
  assert.equal((await harness.run()).status, 404);
});
