// Getting the queue into the store.
//
// The sequence is deliberately dull, because the interesting part is what
// happens to each record afterwards. A submission the store already has is not
// a failure. A record it refuses is not something to retry forever. A link
// that drops halfway is not a reason to drop anything at all.
//
// Nothing is removed from the queue until the server has said what became of
// it. There is no state in which the page has forgotten a record the store
// never received.

import { BLOCKED, heldFrom, sendableBy } from "./outbox.js";

// How many records go in one request. The server's own limit is far higher, so
// this is not about the limit: a smaller chunk means a link that dies partway
// through a large queue still banks what it managed to send.
export const CHUNK = 50;

// emptySummary is what a sync that did nothing looks like, and the shape every
// sync reports in.
export function emptySummary() {
  return {
    filed: 0,       // the store did not have it, and now does
    duplicate: 0,   // the store already had this submission
    blocked: 0,     // refused, and it will stay refused until the record changes
    heldBack: 0,    // not this session's to send, or already blocked
    retryable: 0,   // nothing wrong with it; the link was
    authRequired: false,
  };
}

// syncOutbox drains what it can and reports what happened.
//
// `post` is injected rather than imported so that this can be tested against a
// fake, and so that the caller decides what a 401 does. It must not be a
// wrapper that redirects to a login page on its own: a background sync that
// throws a tester out of a half-written record to an identity provider is a
// worse outcome than a queue that waits.
export async function syncOutbox({ outbox, post, subject }) {
  const summary = emptySummary();
  const entries = await outbox.list();

  const ready = [];
  for (const entry of entries) {
    if (sendableBy(entry, subject)) ready.push(entry);
    else summary.heldBack++;
  }
  if (ready.length === 0) return summary;

  for (let i = 0; i < ready.length; i += CHUNK) {
    const chunk = ready.slice(i, i + CHUNK);
    const done = await sendChunk({ outbox, post, chunk, summary });
    // A link that has gone, or a session that has expired. Everything still
    // queued stays queued; stopping here saves a run of certain failures.
    if (!done) break;
  }

  return summary;
}

async function sendChunk({ outbox, post, chunk, summary }) {
  let response;
  try {
    response = await post("/evidence/batch", { records: chunk.map(e => e.record) });
  } catch {
    // The link went. Nothing is wrong with these records.
    for (const entry of chunk) await outbox.recordAttempt(entry.id);
    summary.retryable += chunk.length;
    return false;
  }

  if (response.status === 401) {
    summary.authRequired = true;
    return false;
  }

  // The store is there and unwell, which is not something a record can fix by
  // being different. Keep them and try later.
  if (response.status >= 500 || response.status === 0) {
    for (const entry of chunk) await outbox.recordAttempt(entry.id);
    summary.retryable += chunk.length;
    return false;
  }

  // Per-record results decide, whenever the store sent them — including on a
  // 207, which is the store saying it filed some of these and not others. The
  // status alone cannot be the test: a partial success and a flat refusal are
  // different answers, and only the body distinguishes them.
  if (!response.data || !Array.isArray(response.data.results)) {
    // No per-record answer: the batch was refused as a whole, because the
    // source on one of these is not the sender's to write, or the request
    // itself was malformed. Retrying sends exactly the same bytes, so these
    // wait for a person rather than for a signal.
    const message = batchError(response);
    for (const entry of chunk) {
      await outbox.block(entry.id, message);
      summary.blocked++;
    }
    return true;
  }

  for (const result of response.data.results) {
    const entry = chunk[result.index];
    if (!entry) continue;

    if (result.status === "created" || result.status === "duplicate") {
      // Filed. A duplicate means an earlier attempt got through and the
      // response did not — exactly what the token is for — so it leaves the
      // queue for the same reason a created record does.
      await outbox.settle(entry.id);
      if (result.status === "created") summary.filed++;
      else summary.duplicate++;
      continue;
    }

    await outbox.block(entry.id, result.error || "the store refused this record");
    summary.blocked++;
  }

  return true;
}

function batchError(response) {
  const data = response.data;
  if (data && typeof data.error === "string") return data.error;
  if (data && Array.isArray(data.errors)) return data.errors.map(e => e.message || e).join("; ");
  return `the store refused this batch (HTTP ${response.status})`;
}

// describeSync writes the one line a tester reads after a sync.
//
// It reports rather than asks, which is the trade the plan makes for syncing
// automatically: nobody is standing over the page at the moment a signal
// returns, so the honest account has to come afterwards.
export function describeSync(summary) {
  if (summary.authRequired) return "Sign in to send the records waiting here.";

  const parts = [];
  if (summary.filed) parts.push(`${summary.filed} filed`);
  // Worth saying rather than folding into "filed": it tells a tester their
  // earlier attempt did get through, which is otherwise invisible.
  if (summary.duplicate) parts.push(`${summary.duplicate} already filed`);
  if (summary.blocked) parts.push(`${summary.blocked} need${summary.blocked === 1 ? "s" : ""} attention`);
  if (summary.heldBack) parts.push(`${summary.heldBack} held back`);
  if (summary.retryable) parts.push(`${summary.retryable} still waiting`);

  if (parts.length === 0) return "Nothing waiting to send.";
  return parts.join(", ") + ".";
}

export { BLOCKED, heldFrom };
