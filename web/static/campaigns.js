// Online campaign package management. Definitions remain immutable; execution
// capture will use a separate draft lifecycle rather than this preview screen.
import { API_BASE, apiFetch, mayDo } from "./common.js";
import { hydrateImages, releaseImages } from "./images.js";
import { PROCEDURE_PAGE_SIZE, procedurePage, procedureListHTML, packageInfoHTML, packageListHTML, procedureHTML, uploadProblem } from "./campaignview.js";

export function createCampaignAPI(request = apiFetch) {
  async function checked(path, options) {
    const response = await request(`${API_BASE}${path}`, options);
    if (!response.ok) {
      let message = `HTTP ${response.status}`;
      try { message = (await response.json()).error || message; } catch { /* no JSON error */ }
      throw new Error(message);
    }
    return response;
  }
  return {
    async list(offset) { return (await checked(`/campaigns?limit=25&offset=${offset}`)).json(); },
    async get(id) { return (await checked(`/campaigns/${encodeURIComponent(id)}`)).json(); },
    async upload(file) {
      const problem = uploadProblem(file);
      if (problem) throw new Error(problem);
      const response = await checked("/campaigns/import", { method: "POST", headers: { "Content-Type": "application/gzip" }, body: file });
      return { row: await response.json(), duplicate: response.status === 200 };
    },
    async download(id) { return (await checked(`/campaigns/${encodeURIComponent(id)}/package`)).blob(); },
  };
}

let view = null;
export function mountCampaigns(me) {
  view = createCampaignView(document.getElementById("tab-campaigns"), me);
}
export function showCampaigns() { view?.show(); }

// Scoped DOM and injected API make request ordering testable without importing
// app.js or coupling this tab to the Add Result form.
export function createCampaignView(root, me, api = createCampaignAPI()) {
  const el = id => root.querySelector(`#${id}`);
  let rows = [], offset = 0, hasMore = false, selected = null;
  let procedureOffset = 0, selectedProcedure = null;
  let listVersion = 0, detailVersion = 0, loaded = false, loading = false, importing = false;
  const canImport = mayDo(me, "evidence:write") && mayDo(me, "blob:write");
  const canReadBlobs = mayDo(me, "blob:read");
  el("campaign-import-form").hidden = !canImport;
  el("campaign-import-permission").hidden = canImport;

  function status(id, text, error = false) {
    const target = el(id);
    target.textContent = text;
    target.classList.toggle("feedback-error", error);
  }

  function paintList() {
    el("campaign-list").innerHTML = packageListHTML(rows, selected?.id);
    el("campaign-prev").disabled = loading || offset === 0;
    el("campaign-next").disabled = loading || !hasMore;
    el("campaign-refresh").disabled = loading;
    el("campaign-page").textContent = rows.length ? `Packages ${offset + 1}–${offset + rows.length}` : "No packages on this page";
  }

  async function loadList(nextOffset = offset) {
    const version = ++listVersion;
    loading = true;
    paintList();
    status("campaign-list-status", "Loading campaigns…");
    try {
      const page = await api.list(nextOffset);
      if (version !== listVersion) return;
      rows = page.campaigns;
      offset = nextOffset;
      hasMore = page.has_more;
      loaded = true;
      status("campaign-list-status", "");
    } catch (error) {
      if (version !== listVersion) return;
      status("campaign-list-status", `Could not load campaigns: ${error.message}. A connection is required; retry with Refresh.`, true);
    } finally {
      if (version === listVersion) { loading = false; paintList(); }
    }
  }

  function paintProcedures() {
    if (!selected) return;
    const page = procedurePage(selected.definition.scenarios, el("campaign-procedure-search").value, procedureOffset);
    el("campaign-procedure-list").innerHTML = procedureListHTML(page.rows, selectedProcedure);
    el("campaign-procedure-prev").disabled = procedureOffset === 0;
    el("campaign-procedure-next").disabled = procedureOffset + page.rows.length >= page.total;
    status("campaign-procedure-count", page.total
      ? `Procedures ${procedureOffset + 1}–${procedureOffset + page.rows.length} of ${page.total} matching`
      : "0 matching procedures");
  }

  function closeProcedure() {
    selectedProcedure = null;
    el("campaign-procedure-inspection").hidden = true;
    el("campaign-procedure-preview").replaceChildren();
    releaseImages();
  }

  function paintProcedure() {
    if (!selected) return;
    const output = el("campaign-procedure-preview");
    const scenario = selected.definition.scenarios.find(s => s.definition.procedure_ref === selectedProcedure);
    if (!scenario) { closeProcedure(); return; }
    el("campaign-procedure-inspection").hidden = false;
    const index = selected.definition.scenarios.indexOf(scenario);
    // Manifest order matches parsed scenario order, including pre-migration
    // responses from an older server. Selection itself uses only procedure_ref.
    const entry = selected.definition.manifest.scenarios[index];
    output.innerHTML = procedureHTML(scenario, entry.path);
    releaseImages();
    if (canReadBlobs) hydrateImages(output);
    else for (const img of output.querySelectorAll("img[data-blob]")) img.replaceWith(document.createTextNode("Setup image requires blob:read permission."));
  }

  function displayPackage(row) {
    selected = row;
    el("campaign-detail").hidden = false;
    el("campaign-package-info").innerHTML = packageInfoHTML(row);
    el("campaign-procedure-context").textContent = `Procedures in campaign: ${row.definition.manifest.campaign.name}. Select a procedure to inspect its instructions and fields.`;
    el("campaign-download").hidden = !canReadBlobs;
    el("campaign-procedure-search").value = "";
    procedureOffset = 0;
    closeProcedure();
    status("campaign-detail-status", "");
    paintProcedures();
    paintList();
  }

  async function openPackage(id) {
    const version = ++detailVersion;
    selected = null;
    el("campaign-detail").hidden = true;
    closeProcedure();
    paintList();
    status("campaign-detail-status", "Loading package…");
    try {
      const row = await api.get(id);
      if (version === detailVersion) displayPackage(row);
    } catch (error) {
      if (version === detailVersion) status("campaign-detail-status", `Could not open package: ${error.message}`, true);
    }
  }

  async function importPackage(event) {
    event.preventDefault();
    if (importing || !canImport) return;
    const input = el("campaign-file");
    const problem = uploadProblem(input.files[0]);
    if (problem) { status("campaign-import-status", problem, true); return; }
    importing = true;
    el("campaign-import").disabled = true;
    input.disabled = true;
    status("campaign-import-status", "Uploading and validating package…");
    try {
      const { row, duplicate } = await api.upload(input.files[0]);
      ++detailVersion; // A previous selection response must not replace this import.
      displayPackage(row);
      status("campaign-import-status", duplicate ? "This exact package was already imported. Opened the existing package." : "Package imported. Select a procedure below to preview it.");
      input.value = "";
      await loadList(0);
    } catch (error) {
      status("campaign-import-status", `Import failed: ${error.message}. You can retry the same file safely.`, true);
    } finally {
      importing = false;
      el("campaign-import").disabled = false;
      input.disabled = false;
    }
  }

  async function downloadPackage() {
    if (!selected || !canReadBlobs) return;
    const id = selected.id;
    el("campaign-download").disabled = true;
    try {
      const file = await api.download(id);
      const url = URL.createObjectURL(file);
      const link = document.createElement("a");
      link.href = url; link.download = "campaign.tar.gz";
      document.body.append(link); link.click(); link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      if (selected?.id === id) status("campaign-detail-status", `Download failed: ${error.message}`, true);
    } finally { el("campaign-download").disabled = false; }
  }

  el("campaign-refresh").addEventListener("click", () => loadList());
  el("campaign-prev").addEventListener("click", () => loadList(Math.max(0, offset - 25)));
  el("campaign-next").addEventListener("click", () => loadList(offset + 25));
  el("campaign-list").addEventListener("click", event => {
    const button = event.target.closest("[data-campaign-id]");
    if (button) openPackage(button.dataset.campaignId);
  });
  el("campaign-import-form").addEventListener("submit", importPackage);
  el("campaign-download").addEventListener("click", downloadPackage);
  el("campaign-procedure-search").addEventListener("input", () => {
    procedureOffset = 0;
    closeProcedure();
    paintProcedures();
  });
  function changeProcedurePage(delta) {
    procedureOffset = Math.max(0, procedureOffset + delta);
    closeProcedure();
    paintProcedures();
  }
  el("campaign-procedure-prev").addEventListener("click", () => changeProcedurePage(-PROCEDURE_PAGE_SIZE));
  el("campaign-procedure-next").addEventListener("click", () => changeProcedurePage(PROCEDURE_PAGE_SIZE));
  el("campaign-procedure-list").addEventListener("click", event => {
    const button = event.target.closest("[data-procedure-ref]");
    if (!button) return;
    selectedProcedure = button.dataset.procedureRef;
    for (const item of el("campaign-procedure-list").querySelectorAll("[data-procedure-ref]")) {
      const active = item.dataset.procedureRef === selectedProcedure;
      item.classList.toggle("selected", active);
      item.querySelector("button").setAttribute("aria-pressed", String(active));
    }
    paintProcedure();
    el("campaign-inspection-title").focus();
  });
  el("campaign-procedure-close").addEventListener("click", () => {
    const previous = selectedProcedure;
    closeProcedure();
    paintProcedures();
    const button = [...el("campaign-procedure-list").querySelectorAll("[data-procedure-ref]")].find(item => item.dataset.procedureRef === previous);
    button?.querySelector("button")?.focus();
  });
  return { show() { if (!loaded && !loading && mayDo(me, "evidence:read")) loadList(); } };
}
