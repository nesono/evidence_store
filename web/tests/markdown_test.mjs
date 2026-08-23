// Unit tests for web/static/markdown.js, run with `node --test`.
//
// A test log is written by a person and rendered as HTML, so the renderer is
// the one place in the UI where untrusted text becomes markup. Both halves of
// that need holding down: the formatting a tester expects to work, and the
// escaping that keeps a log from carrying script into the dialog. Like
// datetime.js, the module is DOM-free so it can be tested here.

import test from "node:test";
import assert from "node:assert/strict";

import { renderMarkdown } from "../static/markdown.js";

// --- Blocks ---

test("renders paragraphs separated by a blank line", () => {
  assert.equal(
    renderMarkdown("First observation.\n\nSecond observation."),
    "<p>First observation.</p>\n<p>Second observation.</p>",
  );
});

test("keeps single newlines inside a paragraph as line breaks", () => {
  // A tester writing a log presses Enter between steps and expects the step to
  // end there, not to be reflowed into the previous one.
  assert.equal(
    renderMarkdown("Step 1: open the door\nStep 2: close it"),
    "<p>Step 1: open the door<br>Step 2: close it</p>",
  );
});

test("renders headings at their level", () => {
  assert.equal(renderMarkdown("# Setup"), "<h1>Setup</h1>");
  assert.equal(renderMarkdown("### Run 3"), "<h3>Run 3</h3>");
  assert.equal(renderMarkdown("###### Deepest"), "<h6>Deepest</h6>");
});

test("does not treat a hash without a space as a heading", () => {
  assert.equal(renderMarkdown("#42 failed"), "<p>#42 failed</p>");
});

test("renders an unordered list", () => {
  assert.equal(
    renderMarkdown("- brake pedal firm\n- no warning lights"),
    "<ul>\n<li>brake pedal firm</li>\n<li>no warning lights</li>\n</ul>",
  );
});

test("renders an ordered list", () => {
  assert.equal(
    renderMarkdown("1. power on\n2. wait for beep"),
    "<ol>\n<li>power on</li>\n<li>wait for beep</li>\n</ol>",
  );
});

test("starts a paragraph and a list without a blank line between them", () => {
  assert.equal(
    renderMarkdown("Observed:\n- one\n- two"),
    "<p>Observed:</p>\n<ul>\n<li>one</li>\n<li>two</li>\n</ul>",
  );
});

test("renders a blockquote, formatting its contents", () => {
  assert.equal(
    renderMarkdown("> quoted **note**"),
    "<blockquote>\n<p>quoted <strong>note</strong></p>\n</blockquote>",
  );
});

test("renders a horizontal rule", () => {
  assert.equal(renderMarkdown("a\n\n---\n\nb"), "<p>a</p>\n<hr>\n<p>b</p>");
});

test("renders a fenced code block without formatting its contents", () => {
  assert.equal(
    renderMarkdown("```\nerror: **not bold**\n```"),
    "<pre><code>error: **not bold**</code></pre>",
  );
});

test("keeps the language of a fenced code block as a class", () => {
  assert.equal(
    renderMarkdown("```json\n{\"a\": 1}\n```"),
    '<pre><code class="language-json">{&quot;a&quot;: 1}</code></pre>',
  );
});

test("closes an unterminated fence at the end of the log", () => {
  // Logs get pasted in half-finished; an open fence must not swallow the rest
  // of the document into nothing.
  assert.equal(renderMarkdown("```\nstill code"), "<pre><code>still code</code></pre>");
});

test("returns nothing for empty or blank input", () => {
  assert.equal(renderMarkdown(""), "");
  assert.equal(renderMarkdown("   \n\n  "), "");
  assert.equal(renderMarkdown(null), "");
  assert.equal(renderMarkdown(undefined), "");
});

test("treats CRLF the same as LF", () => {
  assert.equal(renderMarkdown("a\r\n\r\nb"), "<p>a</p>\n<p>b</p>");
});

// --- Inline formatting ---

test("renders bold, italic and inline code", () => {
  assert.equal(renderMarkdown("**loud**"), "<p><strong>loud</strong></p>");
  assert.equal(renderMarkdown("__loud__"), "<p><strong>loud</strong></p>");
  assert.equal(renderMarkdown("*quiet*"), "<p><em>quiet</em></p>");
  assert.equal(renderMarkdown("_quiet_"), "<p><em>quiet</em></p>");
  assert.equal(renderMarkdown("`literal`"), "<p><code>literal</code></p>");
});

test("leaves markdown inside inline code alone", () => {
  assert.equal(
    renderMarkdown("run `make **all**`"),
    "<p>run <code>make **all**</code></p>",
  );
});

test("does not italicise identifiers with underscores", () => {
  // Test logs are full of snake_case target names; each one turning into an
  // emphasis run would make the log unreadable.
  assert.equal(
    renderMarkdown("checked brake_pedal_test and MAX_SPEED_KPH"),
    "<p>checked brake_pedal_test and MAX_SPEED_KPH</p>",
  );
});

test("formats inline markup inside list items and headings", () => {
  assert.equal(
    renderMarkdown("- saw `ERR_42` on **screen 2**"),
    "<ul>\n<li>saw <code>ERR_42</code> on <strong>screen 2</strong></li>\n</ul>",
  );
  assert.equal(renderMarkdown("## Run `3`"), "<h2>Run <code>3</code></h2>");
});

// --- Links ---

test("renders a markdown link that opens in a new tab", () => {
  assert.equal(
    renderMarkdown("[dashboard](https://example.com/run/7)"),
    '<p><a href="https://example.com/run/7" target="_blank" rel="noopener noreferrer">dashboard</a></p>',
  );
});

test("links a bare URL", () => {
  assert.equal(
    renderMarkdown("see https://example.com/log.txt for details"),
    '<p>see <a href="https://example.com/log.txt" target="_blank" rel="noopener noreferrer">https://example.com/log.txt</a> for details</p>',
  );
});

test("does not link a bare URL twice", () => {
  assert.equal(
    renderMarkdown("[log](https://example.com/a)"),
    '<p><a href="https://example.com/a" target="_blank" rel="noopener noreferrer">log</a></p>',
  );
});

test("keeps query separators in a linked URL", () => {
  assert.equal(
    renderMarkdown("[run](https://example.com/?a=1&b=2)"),
    '<p><a href="https://example.com/?a=1&amp;b=2" target="_blank" rel="noopener noreferrer">run</a></p>',
  );
});

test("allows mailto, root-relative and fragment targets", () => {
  assert.ok(renderMarkdown("[mail](mailto:qa@example.com)").includes('href="mailto:qa@example.com"'));
  assert.ok(renderMarkdown("[here](/api/v1/evidence)").includes('href="/api/v1/evidence"'));
  assert.ok(renderMarkdown("[top](#summary)").includes('href="#summary"'));
});

// --- Escaping ---

test("escapes HTML in the log text", () => {
  assert.equal(
    renderMarkdown("saw <script>alert(1)</script> in the field"),
    "<p>saw &lt;script&gt;alert(1)&lt;/script&gt; in the field</p>",
  );
});

test("escapes HTML inside code spans and fences", () => {
  assert.equal(renderMarkdown("`<b>`"), "<p><code>&lt;b&gt;</code></p>");
  assert.equal(
    renderMarkdown("```\n<img src=x onerror=alert(1)>\n```"),
    "<pre><code>&lt;img src=x onerror=alert(1)&gt;</code></pre>",
  );
});

test("refuses to build a link out of a script URL", () => {
  // The unlinked text stays visible — a log that says what the tester wrote is
  // more use than one with a hole in it.
  const html = renderMarkdown("[click](javascript:alert(1))");
  assert.ok(!html.includes("<a "), html);
  assert.ok(!html.includes("href="), html);
  assert.ok(html.includes("[click]"), html);
});

test("refuses to build a link out of a data URL", () => {
  const html = renderMarkdown("[click](data:text/html;base64,PHNjcmlwdD4=)");
  assert.ok(!html.includes("<a "), html);
  assert.ok(!html.includes("href="), html);
});

test("cannot break out of the href attribute", () => {
  const html = renderMarkdown('[x](https://example.com/" onmouseover="alert(1))');
  assert.ok(!html.includes('onmouseover="alert(1)"'), html);
  assert.ok(html.includes("&quot;"), html);
});

test("strips control characters used to forge code placeholders", () => {
  // Code spans are parked behind NUL-delimited markers while the rest of the
  // line is transformed. A log containing NUL must not be able to address one.
  const html = renderMarkdown("\u00000\u0000 and `real`");
  assert.equal(html, "<p>0 and <code>real</code></p>");
});
