import { API_BASE, apiFetchNoRedirect, esc, mayDo, resultBadge } from "./common.js";

export function campaignProgress(procedures = []) {
  const passed = procedures.filter(p => p.result === "PASS").length;
  const exercised = procedures.filter(p => ["PASS", "FAIL", "ERROR"].includes(p.result)).length;
  const skipped = procedures.filter(p => p.result === "SKIPPED").length;
  const drafts = procedures.filter(p => p.draft_id).length;
  return { total: procedures.length, passed, exercised, skipped, drafts,
    unfinished: procedures.filter(p => p.draft_id || !p.latest_id).length };
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

export function mountCampaignWorkspace(root, me, drafts, packages) {
  const el = id => root.querySelector(`#${id}`);
  const writable = mayDo(me, "evidence:write");
  let execution = null, definition = null, selected = null, runOffset = 0, packageOffset = 0;
  let runVersion = 0, packageVersion = 0, openVersion = 0, loaded = false, switching = false;
  let selectedPackage = null, createRequest = null, creating = false;
  const message = (id, text) => { el(id).textContent = text; };
  async function request(path, method = "GET", data) {
    const response = await apiFetchNoRedirect(API_BASE + path, {
      method, ...(data ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(data) } : {}),
    });
    if (!response.ok) {
      let error = `HTTP ${response.status}`;
      try { error = (await response.json()).error || error; } catch { /* proxy response */ }
      throw new Error(error);
    }
    return response.json();
  }
  function screen(id) {
    for (const view of ["campaign-home", "campaign-new-view", "campaign-workspace", "campaign-package-management"]) el(view).hidden = view !== id;
  }
  function clearWorkspace() {
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
      const page = await request(`/campaign-executions?${query}`);
      if (version !== runVersion) return;
      runOffset = offset;
      el("campaign-runs").innerHTML = page.campaigns.map(c => `<li><button type="button" class="campaign-run" data-execution="${esc(c.id)}"><strong>${esc(c.name)}</strong><span>${c.closed_at ? "Completed" : "Open"} · ${esc(c.repo)} · ${esc(c.branch)} · ${esc(c.rcs_ref)}</span><small>${progressText(c.procedures)}</small></button></li>`).join("");
      message("campaign-runs-status", page.campaigns.length ? "" : "No matching campaigns. Start a new campaign from a package.");
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
      el("campaign-package-picker").innerHTML = page.campaigns.map(p => `<li><button type="button" class="campaign-run" data-package="${esc(p.id)}"><strong>${esc(p.manifest.campaign.name)}</strong><span>${p.scenario_count} procedures · ${esc(p.manifest.campaign.id)}</span><small>${esc(p.manifest.source.repository)}</small><code>${esc(p.digest)}</code></button></li>`).join("");
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
    el("campaign-work-close").disabled = switching;
    el("campaign-work-procedures").innerHTML = execution.procedures.filter(p => fuzzyMatch(`${p.title} ${p.procedure_ref}`, el("campaign-work-search").value)).map(p => `<li><button type="button" class="campaign-run${selected === p.procedure_ref ? " selected" : ""}" data-procedure="${esc(p.procedure_ref)}" aria-pressed="${selected === p.procedure_ref}"><strong>${esc(p.title)}</strong><small>${esc(p.procedure_ref)}</small><span>${p.result ? resultBadge(p.result) : p.latest_id ? "Expired result" : "Not run"}${p.draft_id ? " · In progress" : ""}${p.attempts > 1 ? ` <small>· ${p.attempts} tries</small>` : ""}</span></button></li>`).join("");
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
    const current = await request(`/campaign-executions/${id}`);
    if (execution?.id !== id) return;
    execution = current; paint();
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
      const current = await request(`/campaign-executions/${id}`);
      const pkg = await request(`/campaigns/${current.package_id}`);
      if (version !== openVersion) return;
      execution = current; definition = pkg;
      message("campaign-work-status", "");
      message("campaign-work-empty", "Choose any procedure to begin or continue.");
      el("campaign-work-search").value = "";
      address(id); paint();
    } catch (error) { if (version === openVersion) message("campaign-work-status", error.message); }
  }
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
  el("campaign-runs").addEventListener("click", event => { const item = event.target.closest("[data-execution]"); if (item) open(item.dataset.execution); });
  el("campaign-package-picker").addEventListener("click", async event => {
    if (creating) return;
    const item = event.target.closest("[data-package]"); if (!item) return;
    const id = item.dataset.package;
    selectedPackage = id;
    el("campaign-start-form").hidden = true;
    message("campaign-package-picker-status", "Loading selected package…");
    try {
      const pkg = await request(`/campaigns/${id}`);
      if (selectedPackage !== id) return;
      el("campaign-start-form").hidden = false;
      el("campaign-start-form").elements.namedItem("name").value = pkg.definition.manifest.campaign.name;
      message("campaign-start-package", pkg.definition.manifest.campaign.name);
      createRequest = null;
      message("campaign-package-picker-status", "");
      el("campaign-start-form").elements.namedItem("name").focus();
    } catch (error) { if (selectedPackage === id) { selectedPackage = null; message("campaign-package-picker-status", error.message); } }
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
  return { show() {
    if (loaded) return;
    loaded = true;
    const id = new URLSearchParams(location.search).get("campaign_execution");
    if (id) open(id); else listRuns();
  } };
}
