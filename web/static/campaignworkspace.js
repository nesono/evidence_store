import { API_BASE, apiFetchNoRedirect, esc, mayDo } from "./common.js";
import { ONLINE, onConnectionChange } from "./offline.js";
import { packageListHTML, copyPackageCommit, campaignRepositoryHTML, campaignCommitHTML } from "./campaignview.js";

export function campaignProgress(procedures = []) {
  const passed = procedures.filter(p => p.result === "PASS").length;
  const exercised = procedures.filter(p => ["PASS", "FAIL", "ERROR"].includes(p.result)).length;
  const skipped = procedures.filter(p => p.result === "SKIPPED").length;
  const drafts = procedures.filter(p => p.draft_id).length;
  return { total: procedures.length, passed, exercised, skipped, drafts,
    unfinished: procedures.filter(p => p.draft_id || !p.latest_id).length };
}

export function procedureState(procedure, local) {
  if (local?.local_pending || procedure.draft_id) return { kind: "started", label: local?.local_complete ? "Completed locally, waiting to synchronize" : "In progress" };
  const results = { PASS: ["passed", "Passed"], FAIL: ["failed", "Failed"], SKIPPED: ["skipped", "Skipped"], ERROR: ["error", "Error"] };
  if (results[procedure.result]) {
    const [kind, label] = results[procedure.result];
    return { kind, label };
  }
  return { kind: "not-started", label: procedure.latest_id ? "Result expired" : "Not started" };
}

export function campaignExecutionRows(campaigns) {
  if (!campaigns.length) return '<tr><td colspan="7" class="empty-state">No matching campaigns.</td></tr>';
  return campaigns.map(c => {
    const p = campaignProgress(c.procedures);
    const state = c.closed_at ? "Finalized" : "In progress";
    return `<tr class="campaign-execution-row"><td><button type="button" class="campaign-execution-name" data-execution="${esc(c.id)}"><span class="procedure-state procedure-state-${c.closed_at ? "passed" : "started"}" role="img" aria-label="${state}" title="${state}"></span><span>${esc(c.name)}</span></button></td>
      <td>${campaignRepositoryHTML(c.repo)}</td><td>${esc(c.branch)}</td><td>${campaignCommitHTML(c.rcs_ref)}</td>
      <td class="campaign-progress-count">${p.exercised}/${p.total}</td><td class="campaign-progress-count">${p.passed}/${p.total}</td><td class="campaign-progress-count">${p.skipped}/${p.total}</td></tr>`;
  }).join("");
}

export function fuzzyMatch(text, query) {
  let index = 0;
  const value = String(text).toLowerCase();
  for (const char of query.toLowerCase().replace(/\s/g, "")) {
    index = value.indexOf(char, index);
    if (index < 0) return false;
    index++;
  }
  return true;
}

function progressText(procedures) {
  const p = campaignProgress(procedures);
  return `${p.exercised}/${p.total} exercised · ${p.passed}/${p.total} passed · ${p.skipped} skipped${p.drafts ? ` · ${p.drafts} in progress` : ""}`;
}

export function mountCampaignWorkspace(root, me, drafts, packages, offline) {
  const el = id => root.querySelector(`#${id}`);
  const writable = mayDo(me, "evidence:write");
  let execution = null, definition = null, selected = null, runOffset = 0, packageOffset = 0;
  let runVersion = 0, packageVersion = 0, openVersion = 0, loaded = false, switching = false;
  let selectedPackage = null, createRequest = null, creating = false;
  let syncing = false, localView = false;
  let offlineUnavailable = "";
  let localAttempts = new Map();
  const message = (id, text) => { el(id).textContent = text; };
  async function request(path, method = "GET", data) {
    const response = await apiFetchNoRedirect(API_BASE + path, {
      method, ...(data ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(data) } : {}),
    });
    if (!response.ok) {
      let error = `HTTP ${response.status}`;
      try { error = (await response.json()).error || error; } catch { /* proxy response */ }
      throw Object.assign(new Error(error), { status: response.status });
    }
    return response.json();
  }
  function screen(id) {
    for (const view of ["campaign-home", "campaign-new-view", "campaign-workspace", "campaign-package-management"]) el(view).hidden = view !== id;
  }
  function clearWorkspace() {
    localAttempts = new Map();
    drafts.clear(); selected = null; execution = null; definition = null;
    for (const id of ["campaign-work-title", "campaign-work-context", "campaign-work-progress", "campaign-work-position", "campaign-work-empty"]) message(id, "");
    el("campaign-work-procedures").replaceChildren();
    el("campaign-work-history").hidden = true;
    for (const id of ["campaign-work-close", "campaign-work-prev", "campaign-work-next"]) el(id).disabled = true;
  }
  function address(id) {
    const url = new URL(location.href);
    if (id) url.searchParams.set("campaign_execution", id); else url.searchParams.delete("campaign_execution");
    url.hash = "campaigns";
    history.replaceState(null, "", url);
  }
  async function listRuns(offset = 0) {
    const version = ++runVersion;
    message("campaign-runs-status", "Loading campaigns…");
    try {
      const query = new URLSearchParams({ state: el("campaign-state").value, q: el("campaign-search").value, offset, limit: 25 });
      let page;
      try { page = await request(`/campaign-executions?${query}`); }
      catch (error) {
        if (error.status) throw error;
        const state = el("campaign-state").value, q = el("campaign-search").value.toLowerCase();
        const rows = (await offline.campaigns()).map(p => p.execution).filter(e => e.name.toLowerCase().includes(q) && (state === "all" || (state === "completed") === !!e.closed_at));
        page = { campaigns: rows.slice(offset, offset + 25), has_more: rows.length > offset + 25, local: true };
      }
      if (version !== runVersion) return;
      runOffset = offset;
      el("campaign-runs").innerHTML = campaignExecutionRows(page.campaigns);
      message("campaign-runs-status", page.local ? "Prepared campaigns on this device. Progress reflects the last server snapshot." : page.campaigns.length ? "" : "No matching campaigns. Start a new campaign from a package.");
      el("campaign-runs-prev").disabled = offset === 0;
      el("campaign-runs-next").disabled = !page.has_more;
    } catch (error) { if (version === runVersion) message("campaign-runs-status", `Could not load campaigns: ${error.message}`); }
  }
  async function listPackages(offset = 0) {
    const version = ++packageVersion;
    message("campaign-package-picker-status", "Loading packages…");
    try {
      const query = new URLSearchParams({ q: el("campaign-package-search").value, offset, limit: 25 });
      const page = await request(`/campaigns?${query}`);
      if (version !== packageVersion) return;
      packageOffset = offset;
      el("campaign-package-picker").innerHTML = packageListHTML(page.campaigns, selectedPackage, true);
      message("campaign-package-picker-status", page.campaigns.length ? "" : "No matching packages. Use Campaign packages to import one.");
      el("campaign-picker-prev").disabled = offset === 0;
      el("campaign-picker-next").disabled = !page.has_more;
    } catch (error) { if (version === packageVersion) message("campaign-package-picker-status", error.message); }
  }
  function paint() {
    if (!execution) return;
    message("campaign-work-title", execution.name);
    message("campaign-work-context", `${execution.repo} · ${execution.branch} · ${execution.rcs_ref}`);
    message("campaign-work-progress", `${execution.closed_at ? "Completed" : "Open"} · ${progressText(execution.procedures)}`);
    el("campaign-work-close").textContent = execution.closed_at ? "Reopen campaign" : "Finish campaign";
    el("campaign-work-close").hidden = !writable;
    el("campaign-work-close").disabled = switching || localView;
    el("campaign-work-prepare").disabled = switching || syncing || !execution || !!offlineUnavailable;
    el("campaign-work-sync").disabled = syncing || !!offlineUnavailable;
    el("campaign-work-procedures").innerHTML = execution.procedures.filter(p => fuzzyMatch(`${p.title} ${p.procedure_ref}`, el("campaign-work-search").value)).map(p => {
      const state = procedureState(p, localAttempts.get(p.procedure_ref));
      return `<li><button type="button" class="campaign-run campaign-procedure-run${selected === p.procedure_ref ? " selected" : ""}" data-procedure="${esc(p.procedure_ref)}" aria-pressed="${selected === p.procedure_ref}" title="${esc(p.title)} · ${esc(state.label)}${p.attempts > 1 ? ` · ${p.attempts} attempts` : ""}"><span class="procedure-state procedure-state-${state.kind}" role="img" aria-label="${esc(state.label)}"></span><span class="campaign-procedure-label"><strong>${esc(p.title)}</strong><small>${esc(p.procedure_ref)}</small></span></button></li>`;
    }).join("");
    const index = execution.procedures.findIndex(p => p.procedure_ref === selected);
    el("campaign-work-prev").disabled = switching || index <= 0;
    el("campaign-work-next").disabled = switching || index >= execution.procedures.length - 1;
    message("campaign-work-position", index >= 0 ? `${index + 1} / ${execution.procedures.length}` : "");
    el("campaign-work-history").hidden = !selected;
    drafts.updateExecution(execution);
  }
  async function refresh() {
    if (!execution) return;
    const id = execution.id;
    let current;
    try { current = await request(`/campaign-executions/${id}`); localView = false; }
    catch (error) {
      if (error.status || !await offline.prepared(id)) throw error;
      localView = true; await offlineStatus(); paint(); return;
    }
    if (execution?.id !== id) return;
    execution = current; paint();
    await offline.updateExecution(current);
    await offlineStatus();
  }
  async function choose(ref) {
    if (switching || !execution) return;
    switching = true; paint();
    try {
      if (!await drafts.saveBeforeLeave()) return;
      selected = ref;
      const index = definition.definition.scenarios.findIndex(s => s.definition.procedure_ref === ref);
      if (index < 0) return;
      message("campaign-work-status", "");
      message("campaign-work-empty", "");
      await drafts.select(definition.id, definition.definition.scenarios[index], definition.definition.manifest.scenarios[index], execution, refresh);
      paint();
    } catch (error) { message("campaign-work-status", error.message); }
    finally { switching = false; paint(); }
  }
  async function open(id) {
    if (switching) return;
    if (!await drafts.saveBeforeLeave()) return;
    const version = ++openVersion;
    screen("campaign-workspace");
    clearWorkspace();
    message("campaign-work-status", "Loading campaign…");
    try {
      let current, pkg;
      try {
        current = await request(`/campaign-executions/${id}`);
        pkg = await request(`/campaigns/${current.package_id}`);
        localView = false;
      } catch (error) {
        if (error.status) throw error;
        const prepared = await offline.prepared(id);
        if (!prepared) throw new Error("This campaign has not been prepared for offline use on this device.");
        current = prepared.execution; pkg = prepared.pkg; localView = true;
      }
      if (version !== openVersion) return;
      execution = current; definition = pkg;
      if (!localView) await offline.updateExecution(current);
      message("campaign-work-status", "");
      message("campaign-work-empty", "Choose any procedure to begin or continue.");
      el("campaign-work-search").value = "";
      address(id); paint();
      await offlineStatus();
    } catch (error) { if (version === openVersion) message("campaign-work-status", error.message); }
  }
  async function offlineStatus() {
    if (!execution) return;
    const id = execution.id;
    offlineUnavailable = await offline.unavailableReason();
    const prepared = await offline.prepared(id);
    const rows = prepared ? await offline.attempts(id) : [];
    if (execution?.id !== id) return;
    localAttempts = new Map(rows.filter(a => a.local_pending).map(a => [a.procedure_ref, a]));
    paint();
    const waiting = rows.filter(a => a.local_pending).length, blocked = rows.filter(a => a.local_error).length;
    message("campaign-offline-status", offlineUnavailable || (prepared
      ? `${localView ? "Offline snapshot · " : ""}Prepared ${prepared.prepared_at} · ${waiting} attempts waiting to synchronize${blocked ? ` · ${blocked} need conflict review` : ""}.`
      : "Prepare this campaign while connected to keep its procedures, assets and drafts on this device."));
  }
  async function synchronize() {
    if (syncing || switching) return;
    syncing = true;
    el("campaign-work-sync").disabled = true;
    try {
      if (!await drafts.saveBeforeLeave({ background: true })) return;
      await offline.sync();
      await drafts.synchronized();
      await refresh();
      await offlineStatus();
    } catch (error) { message("campaign-offline-status", `Synchronization paused: ${error.message}. Local work is retained.`); }
    finally { syncing = false; el("campaign-work-sync").disabled = false; paint(); }
  }
  el("campaign-work-sync").addEventListener("click", synchronize);
  onConnectionChange(state => { if (state === ONLINE) synchronize(); });
  // Also retry while reachable: saves made after the reconnect event and
  // transient server failures otherwise wait forever for another transition.
  const syncTimer = setInterval(() => { if (navigator.onLine && !document.hidden) synchronize(); }, 15000);
  window.addEventListener("pagehide", () => clearInterval(syncTimer), { once: true });
  el("campaign-work-prepare").addEventListener("click", async () => {
    if (!execution || switching || syncing) return;
    switching = true; paint();
    const id = execution.id;
    message("campaign-offline-status", "Downloading and verifying campaign assets…");
    try {
      if (!await drafts.saveBeforeLeave()) return;
      if (!navigator.serviceWorker?.controller) throw new Error("The offline application is not ready. Reload once while connected, then prepare again.");
      await offline.prepare(id);
      const durable = await navigator.storage?.persist?.();
      await offlineStatus();
      if (!durable) el("campaign-offline-status").textContent += " Browser storage can be evicted; keep this site installed and synchronize regularly.";
      // Remount the selected server draft as its durable local counterpart.
      switching = false;
      if (selected) await choose(selected);
    } catch (error) { message("campaign-offline-status", `Preparation failed: ${error.message}`); }
    finally { switching = false; paint(); }
  });
  async function home() {
    if (switching) return;
    if (!await drafts.saveBeforeLeave()) return;
    ++openVersion; selectedPackage = null; clearWorkspace();
    screen("campaign-home"); address(null); listRuns();
  }
  function debounce(input, action, invalidate) {
    let timer;
    input.addEventListener("input", () => { invalidate(); clearTimeout(timer); timer = setTimeout(action, 150); });
  }
  el("campaign-new").hidden = !writable;
  el("campaign-new").addEventListener("click", () => {
    selectedPackage = null; createRequest = null;
    el("campaign-start-form").reset();
    el("campaign-start-form").hidden = true;
    message("campaign-start-status", "");
    screen("campaign-new-view"); listPackages();
  });
  el("campaign-new-back").addEventListener("click", event => { event.preventDefault(); if (!creating) home(); });
  el("campaign-work-back").addEventListener("click", home);
  el("campaign-packages-back").addEventListener("click", event => { event.preventDefault(); home(); });
  el("campaign-packages").addEventListener("click", () => { screen("campaign-package-management"); packages.show(); });
  debounce(el("campaign-search"), () => listRuns(), () => { ++runVersion; });
  debounce(el("campaign-package-search"), () => listPackages(), () => { ++packageVersion; });
  el("campaign-state").addEventListener("change", () => listRuns());
  el("campaign-runs-refresh").addEventListener("click", () => listRuns(runOffset));
  el("campaign-runs-prev").addEventListener("click", () => listRuns(Math.max(0, runOffset - 25)));
  el("campaign-runs-next").addEventListener("click", () => listRuns(runOffset + 25));
  el("campaign-picker-prev").addEventListener("click", () => listPackages(Math.max(0, packageOffset - 25)));
  el("campaign-picker-next").addEventListener("click", () => listPackages(packageOffset + 25));
  el("campaign-runs").addEventListener("click", event => {
    const copy = event.target.closest("[data-copy-commit]");
    if (copy) { event.preventDefault(); copyPackageCommit(copy, text => message("campaign-runs-status", text)); return; }
    if (event.target.closest("a")) return;
    const item = event.target.closest(".campaign-execution-row")?.querySelector("[data-execution]");
    if (item) open(item.dataset.execution);
  });
  async function selectPackage(id) {
    if (creating) return;
    selectedPackage = id;
    el("campaign-start-form").hidden = true;
    message("campaign-package-picker-status", "Loading selected package…");
    try {
      const pkg = await request(`/campaigns/${id}`);
      if (selectedPackage !== id) return;
      el("campaign-start-form").hidden = false;
      const form = el("campaign-start-form"), manifest = pkg.definition.manifest;
      form.reset();
      form.elements.namedItem("name").value = `${new Date().toISOString().slice(0, 10)} ${manifest.campaign.name}`;
      form.elements.namedItem("repo").value = manifest.product?.repository || "";
      form.elements.namedItem("branch").value = manifest.product?.branch || "";
      form.elements.namedItem("rcs_ref").value = manifest.product?.commit || "";
      message("campaign-start-package", pkg.definition.manifest.campaign.name);
      createRequest = null;
      message("campaign-package-picker-status", "");
      el("campaign-start-form").elements.namedItem("name").focus();
    } catch (error) { if (selectedPackage === id) { selectedPackage = null; message("campaign-package-picker-status", error.message); } }
  }
  el("campaign-package-picker").addEventListener("click", event => {
    const copy = event.target.closest("[data-copy-commit]");
    if (copy) { event.preventDefault(); copyPackageCommit(copy, text => message("campaign-package-picker-status", text)); return; }
    if (event.target.closest("a")) return;
    const item = event.target.closest(".campaign-package-row")?.querySelector("[data-package]"); if (item) selectPackage(item.dataset.package);
  });
  el("campaign-start-form").addEventListener("submit", async event => {
    event.preventDefault();
    if (creating || !selectedPackage) return;
    const fields = Object.fromEntries(new FormData(event.target));
    const data = { ...fields, package_id: selectedPackage };
    if (!createRequest || JSON.stringify(createRequest.data) !== JSON.stringify(data)) createRequest = { id: crypto.randomUUID(), data };
    el("campaign-start").disabled = true;
    creating = true;
    el("campaign-new-back").setAttribute("aria-disabled", "true");
    try { const row = await request("/campaign-executions", "POST", { id: createRequest.id, ...data }); createRequest = null; await open(row.id); }
    catch (error) { message("campaign-start-status", error.message); }
    finally { creating = false; el("campaign-start").disabled = false; el("campaign-new-back").removeAttribute("aria-disabled"); }
  });
  el("campaign-work-search").addEventListener("input", paint);
  el("campaign-work-procedures").addEventListener("click", event => { const item = event.target.closest("[data-procedure]"); if (item) choose(item.dataset.procedure); });
  for (const [id, delta] of [["campaign-work-prev", -1], ["campaign-work-next", 1]]) el(id).addEventListener("click", () => {
    if (!execution) return;
    const index = execution.procedures.findIndex(p => p.procedure_ref === selected);
    const next = execution.procedures[index + delta]; if (next) choose(next.procedure_ref);
  });
  el("campaign-work-refresh").addEventListener("click", async () => {
    try { if (await drafts.saveBeforeLeave()) { await refresh(); if (selected) await choose(selected); } }
    catch (error) { message("campaign-work-status", error.message); }
  });
  el("campaign-work-close").addEventListener("click", async () => {
    if (!execution || switching) return;
    const closed = !execution.closed_at;
    switching = true; el("campaign-work-close").disabled = true;
    try {
      if (!await drafts.saveBeforeLeave()) return;
      // Reopening must remain possible when closure rejected an offline create.
      // It changes campaign state only; the user still explicitly retries it.
      if (closed) {
        await offline.sync();
        const waiting = (await offline.attempts(execution.id)).some(a => a.local_pending);
        if (waiting) throw new Error("Resolve and synchronize local attempts before finishing the campaign.");
      }
      await refresh();
      if (!!execution.closed_at === closed) {
        message("campaign-work-status", `A colleague already ${closed ? "closed" : "reopened"} this campaign. Refreshed its state.`);
        return;
      }
      const count = campaignProgress(execution.procedures).unfinished;
      if (closed && !confirm(`Finish campaign? ${count} unfinished procedures or retries will be recorded as SKIPPED. Entered observations and completed checks are preserved. You can reopen this campaign later.`)) return;
      execution = await request(`/campaign-executions/${execution.id}/state`, "PUT", { revision: execution.revision, closed });
      drafts.clear(); selected = null;
      message("campaign-work-empty", closed ? "Campaign completed. Select a procedure to review its results, or reopen to continue testing." : "Campaign reopened. Choose a procedure to continue.");
      message("campaign-work-status", ""); paint();
    } catch (error) { message("campaign-work-status", `Could not confirm campaign state: ${error.message}. Refresh to check before retrying.`); }
    finally { switching = false; el("campaign-work-close").disabled = false; paint(); }
  });
  return { async startPackage(id) {
    if (!writable || creating || !await drafts.saveBeforeLeave()) return;
    drafts.clear(); createRequest = null;
    screen("campaign-new-view");
    await selectPackage(id);
  }, show() {
    if (loaded) return;
    loaded = true;
    const id = new URLSearchParams(location.search).get("campaign_execution");
    if (id) open(id); else listRuns();
    synchronize();
  } };
}
