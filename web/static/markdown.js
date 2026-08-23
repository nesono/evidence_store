// A small markdown renderer for test logs.
//
// Test logs are written by hand while running a procedure, so they arrive as
// prose with the shape people give it anyway: steps as a list, a pasted error
// in a fence, a link to a photo. Rendering that is the difference between a log
// being read and being skipped past.
//
// The subset is deliberate rather than a shortfall of a full CommonMark
// implementation: headings, lists, blockquotes, rules, fenced and inline code,
// bold, italic and links. Nested lists, tables, reference links, images and raw
// HTML are not supported — the first two are rare in a hand-written log, and
// the rest either fetch or embed, which a log has no business doing.
//
// Everything here is a pure string function so the rules can be tested under
// node (web/tests/markdown_test.mjs); the DOM only ever sees the result.
//
// Security: the input is untrusted text from whoever filed the evidence, and
// the output goes into innerHTML. Every path escapes before it emits, and link
// targets are checked against a scheme allowlist, so nothing in a log can turn
// into markup or a script URL.

const ALLOWED_URL = /^(?:https?:\/\/|mailto:|\/|#)/i;

// Which lines end a paragraph by starting something else.
const BLOCK_START = /^(?:```|#{1,6}\s|>|\s*(?:[-*+]|\d+[.)])\s|(?:-{3,}|\*{3,}|_{3,})\s*$)/;

const HEADING = /^(#{1,6})\s+(.*)$/;
const FENCE = /^```\s*([A-Za-z0-9_+-]*)\s*$/;
const RULE = /^(?:-{3,}|\*{3,}|_{3,})\s*$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const NUMBER = /^\s*\d+[.)]\s+(.*)$/;
const QUOTE = /^>\s?(.*)$/;

export function escapeHTML(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// Line endings vary by whoever pasted the log; control characters are dropped
// because NUL is what code spans are parked behind while a line is transformed,
// and the rest render as nothing useful anyway.
function normalize(src) {
  return String(src)
    .replace(/\r\n?/g, "\n")
    .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F]/g, "");
}

function link(url, label) {
  if (!ALLOWED_URL.test(url)) return null;
  return `<a href="${url}" target="_blank" rel="noopener noreferrer">${label}</a>`;
}

// Inline formatting for one run of text. The text is escaped first, so every
// transformation below operates on markup-free input and can only add the tags
// it means to.
function inline(text) {
  // Code spans win over everything else inside them, so they come out first and
  // go back in last.
  const spans = [];
  let s = String(text).replace(/`([^`]+)`/g, (_, code) => {
    spans.push(code);
    return `\u0000${spans.length - 1}\u0000`;
  });

  s = escapeHTML(s);

  s = s.replace(/\[([^\]\n]*)\]\(([^)\s]+)\)/g, (whole, label, url) => link(url, label) ?? whole);

  // A bare URL, but only where one starts: anywhere else it is already inside
  // the href or the label of a link built just above.
  s = s.replace(
    /(^|[\s(])(https?:\/\/[^\s<>"')]+)/g,
    (whole, before, url) => `${before}${link(url, url)}`,
  );

  s = s
    .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
    .replace(/__([^_\n]+)__/g, "<strong>$1</strong>")
    // The lookbehind on the opening marker keeps snake_case and file*globs from
    // becoming emphasis: a marker only opens when it does not follow a word.
    .replace(/(^|[^*\w])\*([^*\n]+)\*/g, "$1<em>$2</em>")
    .replace(/(^|[^_\w])_([^_\n]+)_/g, "$1<em>$2</em>");

  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${escapeHTML(spans[Number(i)])}</code>`);
}

export function renderMarkdown(src) {
  if (src === null || src === undefined) return "";
  const lines = normalize(src).split("\n");
  const out = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (!line.trim()) {
      i++;
      continue;
    }

    const fence = line.match(FENCE);
    if (fence) {
      const body = [];
      i++;
      // An unterminated fence runs to the end of the log rather than being
      // abandoned: half-pasted logs are common and their tail still matters.
      while (i < lines.length && !FENCE.test(lines[i])) {
        body.push(lines[i]);
        i++;
      }
      if (i < lines.length) i++;
      const cls = fence[1] ? ` class="language-${fence[1]}"` : "";
      out.push(`<pre><code${cls}>${escapeHTML(body.join("\n"))}</code></pre>`);
      continue;
    }

    if (RULE.test(line)) {
      out.push("<hr>");
      i++;
      continue;
    }

    const heading = line.match(HEADING);
    if (heading) {
      const level = heading[1].length;
      out.push(`<h${level}>${inline(heading[2].trim())}</h${level}>`);
      i++;
      continue;
    }

    if (QUOTE.test(line)) {
      const body = [];
      while (i < lines.length && QUOTE.test(lines[i])) {
        body.push(lines[i].match(QUOTE)[1]);
        i++;
      }
      out.push(`<blockquote>\n${renderMarkdown(body.join("\n"))}\n</blockquote>`);
      continue;
    }

    const marker = BULLET.test(line) ? BULLET : NUMBER.test(line) ? NUMBER : null;
    if (marker) {
      const tag = marker === BULLET ? "ul" : "ol";
      const items = [];
      while (i < lines.length && marker.test(lines[i])) {
        items.push(`<li>${inline(lines[i].match(marker)[1].trim())}</li>`);
        i++;
      }
      out.push(`<${tag}>\n${items.join("\n")}\n</${tag}>`);
      continue;
    }

    // Anything else is a paragraph, running until a blank line or the start of
    // another block. Its own line breaks are kept: a tester pressing Enter
    // between steps means the step ends there.
    const para = [];
    while (i < lines.length && lines[i].trim() && !BLOCK_START.test(lines[i])) {
      para.push(inline(lines[i].trim()));
      i++;
    }
    out.push(`<p>${para.join("<br>")}</p>`);
  }

  return out.join("\n");
}
