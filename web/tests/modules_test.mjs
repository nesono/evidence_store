// Every module in web/static parses and links, run with `node --test`.
//
// This is the cheapest test in the repo and it earns its place on history. Four
// breaks during #124 were of exactly one shape — a name used in a module that
// does not import it — and `node --check`, `go test ./...` and `bazel test //...`
// passed every one. A fifth was still on main when this file was written:
// search.js used EVIDENCE_TYPES and imported only evidenceTypeLabel, so any
// link carrying ?evidence_type= threw during startup and showed no results.
//
// ES modules resolve their imports before a line of the module runs, so a
// missing export is a SyntaxError at link time and dynamic import finds it
// without a browser. What import cannot find is a *global* that is never
// imported at all — EVIDENCE_TYPES was that — because the reference is only
// evaluated when the function runs. That is what the smoke test and the unit
// tests around parseSearchState are for; this file covers the rest.

import test from "node:test";
import assert from "node:assert/strict";
import { readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const staticDir = join(dirname(fileURLToPath(import.meta.url)), "..", "static");
const modules = readdirSync(staticDir).filter(f => f.endsWith(".js")).sort();

test("there are modules to check", () => {
  assert.ok(modules.length > 10, `found only ${modules.length} modules; the path is probably wrong`);
});

for (const file of modules) {
  test(`${file} parses and its imports resolve`, async () => {
    // sw.js is a service worker: a classic script by design, so that the layer
    // whose job is to work when things are degraded is written to the oldest
    // API. It has no imports and is not a module, so importing it would prove
    // nothing.
    if (file === "sw.js") return;

    try {
      await import(join(staticDir, file));
    } catch (err) {
      // A module that touches the document while it is being evaluated cannot
      // run here, and that is not what this test is about. Anything that fails
      // at parse or link time is.
      if (err instanceof SyntaxError) {
        assert.fail(`${file} does not parse or link: ${err.message}`);
      }
      const domish = /is not defined|Cannot read properties of (null|undefined)/.test(err.message);
      assert.ok(domish,
        `${file} failed to load for a reason that is not the missing DOM: ${err.message}`);
    }
  });
}
