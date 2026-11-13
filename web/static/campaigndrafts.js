// Server drafts and prepared local attempts. A saved draft is not finalized evidence.
import { API_BASE, apiFetchNoRedirect, mayDo } from "./common.js";
import { escapeHTML as esc, renderMarkdown } from "./markdown.js";
import { resolveAssetImages } from "./campaignview.js";
import { hydrateImages, releaseImages } from "./images.js";
import { mountFinishTime } from "./finishtime.js";
import { mountDraftManual, readManualMetadata } from "./draftmanual.js";
import { finalizationBlockers } from "./finalizationcheck.js";

export function draftControlsHTML(procedure, data, descriptionPath, assets) {
  return procedure.blocks
    .map((b, i) => {
      if (b.kind === "markdown")
        return `<div class="test-log">${renderMarkdown(resolveAssetImages(b.markdown, descriptionPath, assets))}</div>`;
      const f = b.field || b.check,
        id = `draft-input-${i}`;
      const value = data.answers[f.id];
      const attrs = `id="${id}" data-answer="${esc(f.id)}"${f.required && !b.check ? ' data-completion-required="true"' : ""}`;
      if (b.check)
        return `<div class="campaign-control campaign-check"><p class="campaign-check-scope">Individual check</p>
      <label for="${id}">${esc(f.label)} <small>(required to finalize)</small></label>
      <select ${attrs} data-type="check" aria-required="true" aria-describedby="${id}-scope">${optionsHTML(["PASS", "FAIL", "NOT_EXECUTED"], value?.outcome, {
        "": "This check: not answered", PASS: "This check: passed", FAIL: "This check: failed", NOT_EXECUTED: "This check: not executed",
      })}</select>
      <small id="${id}-scope">Applies only to this check, not to the whole procedure.</small>
      <label for="${id}-reason">Reason / observation <small>(required when NOT_EXECUTED)</small></label><input id="${id}-reason" data-reason="${esc(f.id)}" value="${esc(value?.reason || "")}"></div>`;
      if (f.bind)
        return `<p>Record the verdict for the whole attempt in <strong>Overall procedure result</strong> below.</p>`;
      let input;
      if (f.type === "choice")
        input = `<select ${attrs} data-type="choice">${optionsHTML(f.options, value)}</select>`;
      else if (f.type === "measurement")
        input = `<input ${attrs} data-type="measurement" type="number" step="any" value="${esc(value ?? "")}"><small>Unit: ${esc(f.unit)}</small>`;
      else if (f.type === "evidence")
        input = `<textarea ${attrs} data-type="evidence" rows="2" placeholder="One external HTTPS link per line">${esc((value || []).map((link) => link.uri).join("\n"))}</textarea><small>External links only in this draft version; file uploads follow separately.</small>`;
      else
        input = `<textarea ${attrs} data-type="text" rows="${f.multiline ? 3 : 1}">${esc(value ?? "")}</textarea>`;
      return `<div class="campaign-control"><label for="${id}">${esc(f.label)}${f.required ? " (required for PASS; otherwise explain omission in notes)" : ""}</label>${input}</div>`;
    })
    .join("");
}

function optionsHTML(options, value, labels = {}) {
  return (
    `<option value="">${esc(labels[""] || "Not answered")}</option>` +
    options
      .map(
        (v) =>
          `<option value="${esc(v)}"${v === value ? " selected" : ""}>${esc(labels[v] || v)}</option>`,
      )
      .join("")
  );
}

export function readDraftForm(form, previousAnswers = {}) {
  const data = { answers: {} };
  for (const name of ["repo", "branch", "rcs_ref", "notes", "result"])
    data[name] = form.elements.namedItem(name).value;
  if (form.elements.namedItem("evidence_type")) {
    data.evidence_type = form.elements.namedItem("evidence_type").value;
    data.source = form.elements.namedItem("source").value;
    data.metadata = readManualMetadata(form);
  }
  for (const input of form.querySelectorAll("[data-answer]")) {
    if (input.validity?.badInput)
      throw new Error("Correct the invalid input before saving.");
    const { answer: id, type } = input.dataset,
      value = input.value;
    if (value === "") {
      if (
        type === "check" &&
        [...form.querySelectorAll("[data-reason]")].find(
          (el) => el.dataset.reason === id,
        ).value
      )
        throw new Error(
          "Select a check outcome before saving its reason, or move the observation to tester notes.",
        );
      continue;
    }
    if (type === "measurement") {
      const n = Number(value);
      if (!Number.isFinite(n))
        throw new Error("Measurements must be finite numbers.");
      data.answers[id] = n;
    } else if (type === "check") {
      const reason = [...form.querySelectorAll("[data-reason]")].find(
        (el) => el.dataset.reason === id,
      ).value;
      data.answers[id] = { outcome: value, reason };
    } else if (type === "evidence") {
      data.answers[id] = value
        .split("\n")
        .map((uri) => uri.trim())
        .filter(Boolean)
        .map((uri) => ({
          uri,
          label:
            previousAnswers[id]?.find((link) => link.uri === uri)?.label ?? uri,
        }));
    } else data.answers[id] = value;
  }
  return data;
}

// Server offsets stay independent of the pending local rows pinned to page one.
// A prepared snapshot is only a fallback for transport failures, never auth errors.
export async function attemptHistory({ request, offline, executionID, packageID, ref, offset = 0 }) {
  const prepared = executionID && await offline?.prepared(executionID);
  const local = prepared ? await offline.attempts(executionID, ref) : [];
  const query = new URLSearchParams({
    [executionID ? "execution_id" : "campaign_id"]: executionID || packageID,
    procedure_ref: ref, offset: String(offset),
  });
  let page;
  try { page = await request(`/campaign-attempts?${query}`); }
  catch (error) {
    if (error.status || !prepared) throw error;
    return { attempts: local.slice(offset, offset + 25), has_more: local.length > offset + 25, local: true };
  }
  const pending = new Map(local.filter(a => a.local_pending).map(a => [a.id, a]));
  return { ...page, attempts: [
    ...(offset === 0 ? pending.values() : []),
    ...page.attempts.filter(a => !pending.has(a.id)),
  ] };
}

export function mountCampaignDrafts(root, me, offline) {
  const el = (id) => root.querySelector(`#${id}`);
  const dialog = el("campaign-draft-dialog"),
    form = el("campaign-draft-form");
  let context = null,
    attempt = null,
    dirty = false,
    busy = false,
    pendingCreate = null,
    pendingSave = null,
    pendingFinalize = null,
    listVersion = 0,
    offset = 0,
    autosaveTimer;
  const isLocal = () => attempt?.local_version !== undefined;
  const finishTime = mountFinishTime(root, () => attempt?.created_at, () => { if (attempt && !attempt.finalized_at) { dirty = true; scheduleLocalSave(); } refreshBlockers(); });
  const allowed = mayDo(me, "evidence:write");
  let onChanged = () => {};
  const toolsHome = el("campaign-attempt-tools").parentElement;
  const dialogHome = dialog.parentElement;
  el("campaign-attempt-tools").hidden = !allowed;
  const status = (text) => {
    el("campaign-draft-status").textContent = text;
  };
  const manual = mountDraftManual(root, me, {
    changed: () => {
      if (!attempt?.finalized_at) {
        dirty = true;
        status("Unsaved changes — Save draft to store them.");
        refreshBlockers();
        scheduleLocalSave();
      }
    },
    status,
    attachmentStore: offline,
    contextID: () => attempt?.id,
    editable: () => allowed && !!attempt && !busy && !attempt.finalized_at && !attempt.local_complete && !context?.execution?.closed_at,
  });
  async function request(path, options = {}) {
    const response = await apiFetchNoRedirect(API_BASE + path, options);
    if (!response.ok) {
      let message = `HTTP ${response.status}`;
      try {
        message = (await response.json()).error || message;
      } catch {
        /* non-JSON server response */
      }
      if (response.status === 401)
        message =
          "Sign in again, then retry. Your unsaved inputs are still in this dialog.";
      throw Object.assign(new Error(message), { status: response.status });
    }
    return response.json();
  }
  const send = (path, method, data) =>
    request(path, {
      method,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    });
  function setBusy(value) {
    busy = value;
    const finalized = !!attempt?.finalized_at;
    const readOnly = finalized || !!attempt?.local_complete || !allowed || !!context?.execution?.closed_at;
    el("campaign-draft-fields").disabled = value || readOnly;
    for (const id of [
      "campaign-draft-save",
      "campaign-draft-close",
      "campaign-draft-reload",
    ])
      el(id).disabled = value;
    el("campaign-attempt-start").disabled = value || !allowed || !!context?.execution?.closed_at;
    el("campaign-draft-save").disabled = value || readOnly;
    el("campaign-draft-finalize").disabled = value || readOnly;
    finishTime.lock(value || readOnly);
    el("campaign-attempt-start").textContent = context?.execution
      ? (attempt?.finalized_at ? "Retry procedure" : "Continue / start attempt") : "Start new attempt";
  }
  function showAttempt(row) {
    attempt = row;
    pendingSave = null;
    pendingFinalize = null;
    dirty = false;
    const { scenario, entry } = context;
    el("campaign-draft-title").textContent =
      `${row.finalized_at ? "Finalized" : "Draft"}: ${scenario.definition.title}`;
    el("campaign-draft-identity").textContent =
      `${row.procedure_ref} · Started by ${row.owner}${row.updated_by ? ` · Last saved by ${row.updated_by}` : ""}${row.finalized_by ? ` · Finalized by ${row.finalized_by}` : ""}`;
    for (const name of ["repo", "branch", "rcs_ref", "notes", "result"])
      form.elements.namedItem(name).value = row.data[name] || "";
    for (const radio of form.querySelectorAll('[name="result"]'))
      radio.checked = radio.value === row.data.result;
    manual.restore(row, scenario.definition, context.execution);
    el("draft-software-context").querySelector("p").textContent = context.execution
      ? "Defaults from this campaign. Overrides apply only to this attempt; changed fields are highlighted in yellow."
      : "Reused from your last saved attempt. Confirm the actual deployed build.";
    if (context.execution && !row.finalized_at) {
      form.elements.namedItem("source").value = me.subject || "anonymous";
      form.elements.namedItem("source").readOnly = true;
    }
    const base = entry.path.slice(0, entry.path.lastIndexOf("/") + 1);
    el("campaign-draft-controls").innerHTML = draftControlsHTML(
      scenario.procedure,
      { ...row.data, answers: row.data.answers || {} },
      base + scenario.definition.description,
      scenario.assets,
    );
    status(
      row.finalized_at
        ? row.evidence_id
          ? "Finalized as immutable evidence."
          : "Finalized evidence has expired; its answers are no longer retained."
        : row.local_error ? `Needs attention: ${row.local_error}`
          : row.local_complete ? "Completed locally, waiting to synchronize. Not yet server-confirmed evidence."
            : row.local_pending ? "Saved on this device, waiting to synchronize. This draft is not test evidence."
              : `Saved on server · revision ${row.revision}. This draft is not test evidence.`,
    );
    el("campaign-draft-conflict").hidden = !row.local_error;
    el("campaign-conflict-help").textContent = row.local_create_failed
      ? "This local attempt has not been confirmed on the server. Retry after the campaign reopens, or archive it on this device to stop synchronization."
      : "Compare your local answers with the server version. Finalized evidence cannot be overwritten.";
    for (const choice of ["local", "remote"]) el(`campaign-conflict-${choice}`).hidden = row.local_create_failed;
    for (const choice of ["retry", "archive"]) el(`campaign-conflict-${choice}`).hidden = !row.local_create_failed;
    el("campaign-conflict-versions").textContent = row.local_error
      ? JSON.stringify({ local: row.data, server: row.local_conflict?.data || (row.local_create_failed ? "Creation was not confirmed." : "Reload conflict to fetch current server answers.") }, null, 2) : "";
    finishTime.set(row.finished_at || row.data.finished_at || "");
    dirty = false;
    const link = el("campaign-final-evidence");
    link.hidden = !row.evidence_id;
    link.href = row.evidence_id
      ? `/?detail=${encodeURIComponent(row.evidence_id)}&view=record`
      : "#";
    if (row.finalized_at && !row.evidence_id)
      el("campaign-draft-controls").replaceChildren();
    setBusy(busy);
    refreshBlockers();
    if (!dialog.open) { if (context.execution) dialog.show(); else dialog.showModal(); }
    if (mayDo(me, "blob:read")) hydrateImages(el("campaign-draft-controls"));
    else
      for (const img of el("campaign-draft-controls").querySelectorAll(
        "img[data-blob]",
      ))
        img.replaceWith(
          document.createTextNode("Setup image requires blob:read permission."),
        );
  }
  async function listDrafts(nextOffset = 0) {
    if (!context || (!allowed && !context.execution)) return;
    const version = ++listVersion,
      ctx = context;
    el("campaign-attempt-list-status").textContent = "Loading attempts…";
    try {
      const page = await attemptHistory({
        request, offline, executionID: ctx.execution?.id, packageID: ctx.packageID,
        ref: ctx.scenario.definition.procedure_ref, offset: nextOffset,
      });
      if (version !== listVersion) return;
      offset = nextOffset;
      el("campaign-attempt-list").innerHTML = page.attempts
        .map(
          (a) =>
            `<li><button type="button" class="secondary outline" data-attempt-id="${esc(a.id)}">${a.local_error ? "Needs attention: " : a.local_complete ? "Waiting to publish: " : a.local_pending ? "Local draft: " : a.finalized_at ? (a.evidence_id ? "Final: " : "Expired: ") : "Draft: "}${esc(a.updated_at)} · revision ${a.revision}</button></li>`,
        )
        .join("");
      el("campaign-attempt-list-status").textContent = page.local
        ? "Offline: prepared attempts only. Older server history is available when connected."
        : page.attempts.length ? "Attempts (pending local work first, then newest server attempts)"
        : "No saved drafts for this procedure.";
      el("campaign-attempt-prev").disabled = offset === 0;
      el("campaign-attempt-next").disabled = !page.has_more;
    } catch (error) {
      if (version === listVersion)
        el("campaign-attempt-list-status").textContent =
          `Could not load drafts: ${error.message}`;
    }
  }
  async function start() {
    if (!context || busy || !allowed || context.execution?.closed_at) return;
    if (!await saveBeforeLeave()) return;
    const ctx = context;
    pendingCreate ||= {
      id: crypto.randomUUID(),
      [ctx.execution ? "execution_id" : "campaign_id"]: ctx.execution?.id || ctx.packageID,
      procedure_ref: ctx.scenario.definition.procedure_ref,
    };
    setBusy(true);
    try {
      const prepared = ctx.execution && await offline?.prepared(ctx.execution.id);
      const row = prepared ? await offline.start(ctx.execution.id, pendingCreate.procedure_ref)
        : await send("/campaign-attempts", "POST", pendingCreate);
      if (context !== ctx) return;
      pendingCreate = null;
      showAttempt(row);
      listDrafts();
      await onChanged();
    } catch (error) {
      el("campaign-attempt-list-status").textContent =
        `Could not start: ${error.message}. Retry uses the same attempt ID.`;
      if (context?.execution) el("campaign-work-history").open = true;
    } finally {
      setBusy(false);
    }
  }
  async function resume(id) {
    if (busy) return;
    if (!await saveBeforeLeave()) return;
    const ctx = context;
    setBusy(true);
    try {
      const local = await offline?.get(id);
      let row = local;
      if (!local?.local_pending) {
        try {
          row = await request(`/campaign-attempts/${encodeURIComponent(id)}`);
          if (ctx.execution && await offline?.prepared(ctx.execution.id)) row = await offline.remember(row);
        } catch (error) { if (!local || error.status) throw error; }
      }
      if (context === ctx) showAttempt(row);
    } catch (error) {
      el("campaign-attempt-list-status").textContent = error.message;
      if (context?.execution) el("campaign-work-history").open = true;
    } finally {
      setBusy(false);
    }
  }
  async function save(event) {
    event.preventDefault();
    if (!attempt || busy || attempt.finalized_at) return;
    let data;
    try {
      data = readDraftForm(form, attempt.data.answers);
    } catch (error) {
      status(error.message);
      return;
    }
    setBusy(true);
    status("Saving draft…");
    try {
      const row = await persistDraft(data);
      showAttempt(row);
      listDrafts();
      await onChanged();
    } catch (error) {
      status(`Not saved: ${error.message}`);
      dirty = true;
    } finally {
      setBusy(false);
    }
  }

  async function persistDraft(data) {
    const finished = el("campaign-finished-at").value.trim();
    if (finished) {
      const date = new Date(finished);
      if (Number.isNaN(date.getTime()) || !/(Z|[+-]\d{2}:\d{2})$/.test(finished)) throw new Error("Correct the finish time and include a timezone before saving.");
      data.finished_at = finished;
    }
    if (isLocal()) return offline.save(attempt, data);
    if (!pendingSave || JSON.stringify(pendingSave.data) !== JSON.stringify(data)) {
      pendingSave = {revision: attempt.revision, operation_id: crypto.randomUUID(), data};
    }
    await manual.prepare(data);
    return send(`/campaign-attempts/${attempt.id}`, "PUT", pendingSave);
  }
  async function saveBeforeLeave() {
    if (busy || manual.pending()) { status("Wait for the current operation before navigating."); return false; }
    if (!attempt || attempt.finalized_at || !dirty) return true;
    setBusy(true);
    try {
      const row = await persistDraft(readDraftForm(form, attempt.data.answers));
      attempt = row;
      dirty = false;
      pendingSave = null;
      await onChanged();
      status(row.local_error ? `Saved locally; needs attention: ${row.local_error}` : row.local_pending ? "Saved on this device, waiting to synchronize." : "Saved on server.");
      return true;
    } catch (error) {
      status(`Not saved: ${error.message}. Your entries remain here.`);
      return false;
    } finally { setBusy(false); }
  }
  async function close() {
    if (context?.execution) {
      if (await saveBeforeLeave()) dismissAttempt();
      return;
    }
    if (manual.pending()) {
      status("Wait for pending lookups or attachments before closing.");
      return;
    }
    if (
      busy ||
      (dirty &&
        !confirm(
          "Discard unsaved changes? The last server-saved draft will remain.",
        ))
    )
      return;
    dismissAttempt();
  }

  function dismissAttempt() {
    clearTimeout(autosaveTimer);
    dialog.close();
    dirty = false;
    attempt = null;
    pendingSave = null;
    pendingFinalize = null;
    el("campaign-draft-controls").replaceChildren();
    releaseImages();
    el("campaign-attempt-start").focus();
  }

  function blockerTarget(target) {
    if (target === "save") return el("campaign-draft-save");
    if (target === "finished") return el("campaign-finished-at");
    if (target === "result") return form.querySelector('[name="result"]');
    if (target.startsWith("answer:")) return [...form.querySelectorAll("[data-answer]")].find(input => input.dataset.answer === target.slice(7));
    if (target.startsWith("reason:")) return [...form.querySelectorAll("[data-reason]")].find(input => input.dataset.reason === target.slice(7));
    return form.elements.namedItem(target);
  }

  function refreshBlockers() {
    for (const input of root.querySelectorAll('[aria-invalid="true"]')) input.removeAttribute("aria-invalid");
    if (!attempt || attempt.finalized_at) return [];
    const result = form.elements.namedItem("result").value;
    for (const input of form.querySelectorAll('[data-completion-required]')) {
      input.setAttribute("aria-required", String(result === "PASS"));
    }
    for (const reason of form.querySelectorAll('[data-reason]')) {
      const check = blockerTarget(`answer:${reason.dataset.reason}`);
      reason.setAttribute("aria-required", String(check?.value === "NOT_EXECUTED"));
    }
    for (const label of form.querySelectorAll('#campaign-draft-controls label[for]')) {
      label.classList.toggle("draft-required-label", el(label.htmlFor)?.getAttribute("aria-required") === "true");
    }
    let data, issues;
    try {
      data = readDraftForm(form, attempt.data.answers);
      issues = finalizationBlockers(context.scenario.procedure, data, el("campaign-finished-at").value);
      const notesRequired = finalizationBlockers(context.scenario.procedure, {...data, notes:""}, el("campaign-finished-at").value).some(issue => issue.target === "notes");
      form.elements.namedItem("notes").setAttribute("aria-required", String(notesRequired));
    } catch (error) { issues = [{target:"save", message:error.message}]; }
    for (const label of form.querySelectorAll("label:not([for])")) {
      const input = label.querySelector('input,select,textarea');
      if (!input) continue;
      let marker = label.querySelector(".draft-required-marker");
      const required = input.getAttribute("aria-required") === "true";
      if (!marker && required) {
        marker = document.createElement("span");
        marker.className = "draft-required-marker";
        marker.textContent = " * ";
        marker.setAttribute("aria-hidden", "true");
        label.insertBefore(marker, label.firstElementChild);
      }
      if (marker) marker.hidden = !required;
    }
    for (const issue of issues) {
      const input = blockerTarget(issue.target);
      if (input && issue.target !== "save") input.setAttribute("aria-invalid", "true");
      if (issue.target === "result") el("campaign-overall-result").setAttribute("aria-invalid", "true");
    }
    return issues;
  }

  async function finalize() {
    if (!attempt || busy || attempt.finalized_at) return;
    if (manual.pending()) { status("Wait for pending lookups or attachments before finalizing."); return; }
    const issues = refreshBlockers();
    if (issues.length) {
      const issue = issues.find(item => item.target !== "save") || issues[0];
      status(`Cannot finalize: ${issue.message}`);
      const input = blockerTarget(issue.target);
      if (input?.closest("details")) input.closest("details").open = true;
      input?.focus();
      return;
    }
    const raw = el("campaign-finished-at").value.trim();
    const finished_at = new Date(raw).toISOString();
    const data = readDraftForm(form, attempt.data.answers);
    setBusy(true);
    status("Saving and finalizing attempt…");
    try {
      // Once saving succeeds, keep that revision even if finalization loses its
      // response. An unchanged retry must not attempt another save on an already
      // finalized row. Failed saves retain their operation ID for safe replay.
      if (dirty || (!pendingFinalize && JSON.stringify(data) !== JSON.stringify(attempt.data))) {
        attempt = await persistDraft(data);
        dirty = false;
        pendingSave = null;
        pendingFinalize = null;
      }
      if (!pendingFinalize || pendingFinalize.finished_at !== finished_at)
        pendingFinalize = { revision: attempt.revision, finished_at };
      const finalized = isLocal() ? await offline.complete(attempt, finished_at) : await send(
          `/campaign-attempts/${attempt.id}/finalize`,
          "POST",
          pendingFinalize,
      );
      if (context.execution) showAttempt(finalized); else dismissAttempt();
      setBusy(false);
      el("campaign-attempt-start").focus();
      listDrafts();
      await onChanged();
    } catch (error) {
      status(
        `Not finalized: ${error.message}. Retry with the same finish time if the response was lost.`,
      );
    } finally {
      setBusy(false);
    }
  }
  el("campaign-draft-finalize").addEventListener("click", finalize);
  el("campaign-attempt-start").addEventListener("click", start);
  el("campaign-attempt-refresh").addEventListener("click", () => listDrafts());
  el("campaign-attempt-prev").addEventListener("click", () =>
    listDrafts(Math.max(0, offset - 25)),
  );
  el("campaign-attempt-next").addEventListener("click", () =>
    listDrafts(offset + 25),
  );
  el("campaign-attempt-list").addEventListener("click", (event) => {
    const button = event.target.closest("[data-attempt-id]");
    if (button) resume(button.dataset.attemptId);
  });
  form.addEventListener("input", () => {
    if (attempt?.finalized_at) return;
    dirty = true;
    status("Unsaved changes — select Save draft to store them on the server.");
    refreshBlockers();
    scheduleLocalSave();
  });
  function scheduleLocalSave() {
    clearTimeout(autosaveTimer);
    if (!isLocal() || attempt.finalized_at || attempt.local_complete) return;
    autosaveTimer = setTimeout(async () => {
      if (!isLocal() || !dirty || busy || manual.pending()) return;
      // Save without setBusy: disabling the fieldset would steal focus from
      // the field the tester is typing in.
      const current = attempt;
      let data;
      try { data = readDraftForm(form, current.data.answers); } catch { return; }
      const snapshot = JSON.stringify(data);
      busy = true;
      try {
        const row = await persistDraft(data);
        if (attempt !== current) return;
        attempt = row;
        pendingSave = null;
        let now;
        try { now = JSON.stringify(readDraftForm(form, row.data.answers)); } catch { now = null; }
        dirty = now !== snapshot;
        if (!dirty) status(row.local_error ? `Saved locally; needs attention: ${row.local_error}` : "Saved on this device, waiting to synchronize.");
        await onChanged();
      } catch (error) {
        status(`Not saved: ${error.message}. Your entries remain here.`);
      } finally {
        busy = false;
        if (dirty && attempt === current) scheduleLocalSave();
      }
    }, 350);
  }
  form.addEventListener("submit", save);
  el("campaign-draft-close").addEventListener("click", close);
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    close();
  });
  el("campaign-draft-reload").addEventListener("click", async () => {
    if (
      busy ||
      !attempt ||
      (dirty &&
        !confirm("Discard unsaved changes and reload the server draft?"))
    )
      return;
    setBusy(true);
    try {
      if (isLocal()) {
        // Local recovery must never silently throw away a queued operation.
        showAttempt(attempt.local_error ? await offline.reloadConflict(attempt.id) : await offline.get(attempt.id));
      } else showAttempt(await request(`/campaign-attempts/${attempt.id}`));
    } catch (error) {
      status(error.message);
    } finally {
      setBusy(false);
    }
  });
  for (const choice of ["local", "remote"]) el(`campaign-conflict-${choice}`).addEventListener("click", async () => {
    if (!attempt || busy || !await saveBeforeLeave() || !confirm(choice === "local" ? "Replace the displayed server answers with your local answers? The save still checks the server revision." : "Use the server answers? Your local copy will be archived on this device.")) return;
    setBusy(true);
    try { showAttempt(await offline.resolve(attempt.id, choice)); await onChanged(); }
    catch (error) { status(error.message); }
    finally { setBusy(false); }
  });
  el("campaign-conflict-retry").addEventListener("click", async () => {
    if (!attempt || busy || !await saveBeforeLeave()) return;
    setBusy(true);
    try { showAttempt(await offline.retryCreate(attempt.id)); await onChanged(); await listDrafts(); }
    catch (error) { status(error.message); }
    finally { setBusy(false); }
  });
  el("campaign-conflict-archive").addEventListener("click", async () => {
    if (!attempt || busy || !await saveBeforeLeave() || !confirm("Archive this local attempt and stop trying to synchronize it? Its answers and attachments will remain on this device.")) return;
    setBusy(true);
    try {
      await offline.archiveFailedCreate(attempt.id);
      dismissAttempt();
      await onChanged(); await listDrafts();
      status("Local attempt archived on this device.");
    } catch (error) { status(error.message); }
    finally { setBusy(false); }
  });
  el("campaign-conflict-export").addEventListener("click", async () => {
    if (!attempt) return;
    const data = await offline.export(attempt.id);
    const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
    const link = document.createElement("a"); link.href = url; link.download = `attempt-${attempt.id}.json`; link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  window.addEventListener("beforeunload", (event) => {
    if (dirty || busy) {
      event.preventDefault();
      event.returnValue = "";
    }
  });
  return {
    async select(packageID, scenario, entry, execution = null, changed = () => {}) {
      clearTimeout(autosaveTimer);
      context = { packageID, scenario, entry, execution };
      onChanged = changed;
      pendingCreate = null;
      if (dialog.open) dialog.close();
      attempt = null;
      dirty = false;
      el("campaign-attempt-tools").hidden = !allowed && !execution;
      el("campaign-attempt-start").hidden = !allowed;
      dialog.classList.toggle("campaign-inline-attempt", !!execution);
      (execution ? el("campaign-work-editor") : dialogHome).append(dialog);
      (execution ? el("campaign-work-attempts") : toolsHome).append(el("campaign-attempt-tools"));
      el("campaign-attempt-start").textContent = execution ? "Retry / new attempt" : "Start new attempt";
      el("campaign-attempt-start").disabled = !!execution?.closed_at;
      listDrafts();
      if (execution) {
        const local = (await offline?.attempts(execution.id, scenario.definition.procedure_ref) || []).find(a => a.local_pending);
        const progress = execution.procedures.find(p => p.procedure_ref === scenario.definition.procedure_ref);
        if (local) await resume(local.id);
        else if (progress?.draft_id || progress?.latest_id) await resume(progress.draft_id || progress.latest_id);
        else if (!execution.closed_at) await start();
      }
    },
    saveBeforeLeave,
    async synchronized() {
      if (isLocal() && !dirty && !busy) {
        const row = await offline.get(attempt.id);
        // A periodic empty sync must not reset focus, selection or disclosure
        // state while somebody is reading the procedure.
        if (row && JSON.stringify(row) !== JSON.stringify(attempt)) showAttempt(row);
        await listDrafts();
      }
    },
    updateExecution(execution) { if (context?.execution?.id === execution.id) { context.execution = execution; setBusy(busy); } },
    clear() {
      clearTimeout(autosaveTimer);
      if (dialog.open) dialog.close();
      attempt = null;
      dirty = false;
      context = null;
      ++listVersion;
      pendingCreate = null;
      el("campaign-attempt-list").replaceChildren();
    },
  };
}
