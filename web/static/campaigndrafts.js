// Online, explicit-save attempt drafts. A saved draft is not finalized evidence.
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

export function mountCampaignDrafts(root, me) {
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
    offset = 0;
  const finishTime = mountFinishTime(root, () => attempt?.created_at, () => { if (attempt && !attempt.finalized_at) dirty = true; refreshBlockers(); });
  const allowed = mayDo(me, "evidence:write");
  let onChanged = () => {};
  const toolsHome = el("campaign-attempt-tools").parentElement;
  const dialogHome = dialog.parentElement;
  el("campaign-attempt-tools").hidden = !allowed;
  const status = (text) => {
    el("campaign-draft-status").textContent = text;
    el("campaign-finalize-status").textContent = text;
  };
  const manual = mountDraftManual(root, me, {
    changed: () => {
      if (!attempt?.finalized_at) {
        dirty = true;
        status("Unsaved changes — Save draft to store them.");
        refreshBlockers();
      }
    },
    status,
    contextID: () => attempt?.id,
    editable: () => allowed && !!attempt && !busy && !attempt.finalized_at && !context?.execution?.closed_at,
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
      throw new Error(message);
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
    const readOnly = finalized || !allowed || !!context?.execution?.closed_at;
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
    manual.restore(row, scenario.definition);
    for (const name of ["repo", "branch", "rcs_ref"]) form.elements.namedItem(name).readOnly = !!context.execution;
    el("draft-software-context").querySelector("p").textContent = context.execution
      ? "Fixed for this campaign. Start a new campaign to test another product revision."
      : "Reused from your last saved attempt. Confirm the actual deployed build.";
    if (context.execution && !row.finalized_at) {
      form.elements.namedItem("source").value = me.subject || "anonymous";
      form.elements.namedItem("source").readOnly = true;
    }
    el("campaign-finalize-help").textContent = context.execution
      ? "Save and finalize publishes this attempt as immutable evidence. Use Next procedure to continue, or Retry to run this procedure again."
      : "Save and finalize stores your current entries and publishes immutable evidence. The dialog closes after success.";
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
        : `Saved on server · revision ${row.revision}. This draft is not test evidence.`,
    );
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
      const query = new URLSearchParams({
        [ctx.execution ? "execution_id" : "campaign_id"]: ctx.execution?.id || ctx.packageID,
        procedure_ref: ctx.scenario.definition.procedure_ref,
        offset: String(nextOffset),
      });
      const page = await request(`/campaign-attempts?${query}`);
      if (version !== listVersion) return;
      offset = nextOffset;
      el("campaign-attempt-list").innerHTML = page.attempts
        .map(
          (a) =>
            `<li><button type="button" class="secondary outline" data-attempt-id="${esc(a.id)}">${a.finalized_at ? (a.evidence_id ? "Final: " : "Expired: ") : "Draft: "}${esc(a.updated_at)} · revision ${a.revision}</button></li>`,
        )
        .join("");
      el("campaign-attempt-list-status").textContent = page.attempts.length
        ? "Attempts (newest first)"
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
      const row = await send("/campaign-attempts", "POST", pendingCreate);
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
      const row = await request(`/campaign-attempts/${encodeURIComponent(id)}`);
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
      status("Saved on server.");
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
      const finalized = await send(
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
  });
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
      showAttempt(await request(`/campaign-attempts/${attempt.id}`));
    } catch (error) {
      status(error.message);
    } finally {
      setBusy(false);
    }
  });
  window.addEventListener("beforeunload", (event) => {
    if (dirty || busy) {
      event.preventDefault();
      event.returnValue = "";
    }
  });
  return {
    async select(packageID, scenario, entry, execution = null, changed = () => {}) {
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
        const progress = execution.procedures.find(p => p.procedure_ref === scenario.definition.procedure_ref);
        if (progress?.draft_id || progress?.latest_id) await resume(progress.draft_id || progress.latest_id);
        else if (!execution.closed_at) await start();
      }
    },
    saveBeforeLeave,
    updateExecution(execution) { if (context?.execution?.id === execution.id) { context.execution = execution; setBusy(busy); } },
    clear() {
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
