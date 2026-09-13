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

// The one shape import cannot catch, caught statically instead: a module uses
// a name that another module in web/static exports, without importing it or
// declaring it itself. The reference is only evaluated when the function runs,
// so the page loads, every test above passes, and the break waits for the one
// record that reaches that line. search.js called mapURL from location.js
// without importing it, and every record whose location was a pair of
// coordinates failed to open with "Can't find variable: mapURL" — found by
// hand, long after the move out of app.js (#136) that dropped the import.
test("no module uses another module's export without importing it", async () => {
  const { readFileSync } = await import("node:fs");
  const source = f => readFileSync(join(staticDir, f), "utf8");

  // Comments name functions all the time; code in a template string is kept,
  // since `${mapURL(coords)}` is exactly where this one hid.
  const code = text => text
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|[^:"'`\\])\/\/.*$/gm, "$1");

  const exportsOf = new Map();
  for (const file of modules) {
    if (file === "sw.js") continue;
    const names = [...source(file).matchAll(/^export\s+(?:async\s+)?(?:function\*?|const|let|class)\s+([A-Za-z_$][\w$]*)/gm)]
      .map(m => m[1]);
    exportsOf.set(file, names);
  }

  const problems = [];
  for (const file of modules) {
    if (file === "sw.js") continue;
    const full = code(source(file));
    // Both halves of `name as alias` count as imported, and the import lines
    // themselves are not uses.
    const imported = new Set([...full.matchAll(/^import\s*\{([^}]*)\}/gm)]
      .flatMap(m => m[1].split(",").flatMap(s => s.trim().split(/\s+as\s+/)).filter(Boolean)));
    const text = full.replace(/^import\s[^;]*;/gm, "");
    const declared = new Set([...text.matchAll(/\b(?:function\*?|const|let|var|class)\s+([A-Za-z_$][\w$]*)/g)].map(m => m[1]));
    // Destructured and parameter names count as declared too, loosely: any
    // name followed by `=>` or inside a parameter list is local.
    for (const m of text.matchAll(/\(([^()]*)\)\s*=>|function\s*[\w$]*\s*\(([^()]*)\)/g)) {
      for (const p of (m[1] ?? m[2] ?? "").split(/[,{}\s=]+/)) if (p) declared.add(p);
    }

    for (const [other, names] of exportsOf) {
      if (other === file) continue;
      for (const name of names) {
        if (imported.has(name) || declared.has(name)) continue;
        // A use, not a property (`x.name`) or an object key (`name:`).
        // A hyphen on either side is a class name or an id (`outbox-edit`).
        if (new RegExp(`(?<![\\w$.-])${name.replace(/\$/g, "\\$")}(?![\\w$-]|\\s*:)`).test(text)) {
          problems.push(`${file} uses ${name}, which ${other} exports, without importing it`);
        }
      }
    }
  }
  assert.deepEqual(problems, []);
});
