// The Access tab: who can talk to this store, and what they may do.
//
// Before this existed, issuing a key meant an environment variable and a
// redeploy, and revoking one meant the same. Everything here is one request to
// /api/v1/principals, which is behind principal:admin — so the tab itself is
// only shown to a caller that /me says holds it.

import { API_BASE, apiFetch, esc, formatTime } from "./common.js";

// The roles in the order they widen, which is the order an administrator reads
// them in. Kept in step with internal/auth/role.go by the API refusing anything
// else; the descriptions are what stop an admin having to guess.
//
// provisioner sits outside that widening: it is not a larger version of the
// role above it but a different job, and the only one here that grants no
// reading at all.
const ROLES = [
  ["viewer", "Read evidence, analytics and inheritance"],
  ["contributor", "Viewer, plus writing evidence and images"],
  ["ci", "Contributor, plus writing a source that is not its own name"],
  ["admin", "Contributor, plus inheritance, retention and this page"],
  ["provisioner", "Only SCIM: create and deactivate people. Reads nothing"],
];

let principals = [];

// --- Rendering ---

function roleCheckboxes(container, selected, namePrefix) {
  container.querySelectorAll(".access-role").forEach(el => el.remove());
  for (const [role, description] of ROLES) {
    const label = document.createElement("label");
    label.className = "access-role";
    label.innerHTML = `
      <input type="checkbox" name="${namePrefix}-${role}" value="${role}"
             ${selected.includes(role) ? "checked" : ""}>
      <strong>${role}</strong> <small>${esc(description)}</small>`;
    container.appendChild(label);
  }
}

function checkedRoles(container) {
  return [...container.querySelectorAll("input[type=checkbox]:checked")].map(el => el.value);
}

// A key that has never been used has never been seen, which is worth showing as
// such rather than as a blank: it is how an admin spots a credential that was
// issued and never picked up.
function lastSeen(p) {
  return p.last_seen_at ? formatTime(p.last_seen_at) : "never";
}

function statusBadge(p) {
  return p.disabled_at
    ? `<span class="badge badge-fail" title="Revoked ${esc(formatTime(p.disabled_at))}">revoked</span>`
    : `<span class="badge badge-pass">active</span>`;
}

function renderTable() {
  const body = document.querySelector("#access-table tbody");
  if (principals.length === 0) {
    body.innerHTML = `<tr><td colspan="5"><em>No principals yet. Issue a key to create one.</em></td></tr>`;
    return;
  }

  body.innerHTML = principals.map(p => `
    <tr data-id="${esc(p.id)}">
      <td class="col-subject">
        ${esc(p.subject)}
        ${p.display_name ? `<br><small>${esc(p.display_name)}</small>` : ""}
      </td>
      <td class="col-roles">${
        p.roles.length
          ? p.roles.map(r => `<span class="badge badge-tag">${esc(r)}</span>`).join(" ")
          : `<small><em>none — authenticated, allowed nothing</em></small>`
      }</td>
      <td class="col-status">${statusBadge(p)}</td>
      <td class="col-seen"><small>${esc(lastSeen(p))}</small></td>
      <td class="col-actions">
        <button type="button" class="secondary outline" data-action="roles">Roles</button>
        <button type="button" class="secondary outline" data-action="rotate">Rotate</button>
        <button type="button" class="secondary outline" data-action="${p.disabled_at ? "enable" : "disable"}">
          ${p.disabled_at ? "Restore" : "Revoke"}
        </button>
      </td>
    </tr>
    <tr class="access-role-editor" data-editor-for="${esc(p.id)}" hidden>
      <td colspan="5">
        <fieldset class="access-role-set"><legend>Roles for ${esc(p.subject)}</legend></fieldset>
        <button type="button" data-action="save-roles">Save roles</button>
        <button type="button" class="secondary" data-action="cancel-roles">Cancel</button>
      </td>
    </tr>
  `).join("");
}

function feedback(message, kind) {
  const el = document.getElementById("access-feedback");
  el.innerHTML = `<p class="access-${kind}">${esc(message)}</p>`;
  if (kind === "ok") setTimeout(() => { el.innerHTML = ""; }, 6000);
}

// The server's messages are written for the person reading them — "is the only
// principal that can still administer this store" says more than a status code
// — so they are shown rather than replaced.
async function errorText(resp, fallback) {
  try {
    const body = await resp.json();
    if (body.error) return body.error;
    if (body.errors) return body.errors.join("; ");
  } catch { /* not JSON; fall through */ }
  return fallback;
}

// --- Requests ---

export async function loadPrincipals() {
  const resp = await apiFetch(`${API_BASE}/principals`);
  if (!resp.ok) {
    feedback(await errorText(resp, "Could not load principals."), "error");
    return;
  }
  const body = await resp.json();
  principals = body.principals || [];
  document.getElementById("access-db-warning").hidden = body.auth_db_enabled;
  renderTable();
}

function showKey(subject, key) {
  document.getElementById("access-key-for").textContent = `Key for ${subject}:`;
  document.getElementById("access-key-value").value = key;
  document.getElementById("access-key-dialog").showModal();
}

async function issueKey(event) {
  event.preventDefault();
  const form = event.target;
  if (!form.checkValidity()) { form.reportValidity(); return; }

  const subject = form.subject.value.trim();
  const resp = await apiFetch(`${API_BASE}/principals`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      subject,
      display_name: form.display_name.value.trim(),
      roles: checkedRoles(document.getElementById("access-new-roles")),
    }),
  });

  if (!resp.ok) {
    feedback(await errorText(resp, "Could not issue the key."), "error");
    return;
  }

  const { api_key: key } = await resp.json();
  form.reset();
  roleCheckboxes(document.getElementById("access-new-roles"), [], "new");
  document.getElementById("access-new").open = false;
  await loadPrincipals();
  showKey(subject, key);
}

async function post(id, action, subject) {
  const resp = await apiFetch(`${API_BASE}/principals/${id}/${action}`, { method: "POST" });
  if (!resp.ok) {
    feedback(await errorText(resp, `Could not ${action} ${subject}.`), "error");
    return null;
  }
  return resp.json();
}

async function saveRoles(id, subject, roles) {
  const resp = await apiFetch(`${API_BASE}/principals/${id}/roles`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ roles }),
  });
  if (!resp.ok) {
    feedback(await errorText(resp, `Could not change the roles for ${subject}.`), "error");
    return;
  }
  await loadPrincipals();
  feedback(`Roles for ${subject} are now ${roles.length ? roles.join(", ") : "none"}.`, "ok");
}

// --- Wiring ---

function principalFor(el) {
  const row = el.closest("tr[data-id]");
  return row && principals.find(p => p.id === row.dataset.id);
}

async function onTableClick(event) {
  const button = event.target.closest("button[data-action]");
  if (!button) return;

  const editorRow = button.closest("tr.access-role-editor");
  const id = editorRow ? editorRow.dataset.editorFor : principalFor(button)?.id;
  const principal = principals.find(p => p.id === id);
  if (!principal) return;

  switch (button.dataset.action) {
    case "roles": {
      const editor = document.querySelector(`tr[data-editor-for="${id}"]`);
      roleCheckboxes(editor.querySelector(".access-role-set"), principal.roles, `edit-${id}`);
      editor.hidden = false;
      break;
    }
    case "cancel-roles":
      editorRow.hidden = true;
      break;
    case "save-roles":
      await saveRoles(id, principal.subject, checkedRoles(editorRow.querySelector(".access-role-set")));
      break;
    case "disable":
      // Revocation bites on the next request, including this browser's own if
      // the admin revokes the key it is holding — worth being sure about.
      if (!confirm(`Revoke ${principal.subject}? Its key stops working immediately.`)) return;
      if (await post(id, "disable", principal.subject)) {
        await loadPrincipals();
        feedback(`${principal.subject} revoked.`, "ok");
      }
      break;
    case "enable":
      if (await post(id, "enable", principal.subject)) {
        await loadPrincipals();
        feedback(`${principal.subject} restored, with the roles it had.`, "ok");
      }
      break;
    case "rotate": {
      if (!confirm(`Rotate the key for ${principal.subject}? The current key stops working immediately.`)) return;
      const issued = await post(id, "rotate", principal.subject);
      if (issued) {
        await loadPrincipals();
        showKey(principal.subject, issued.api_key);
      }
      break;
    }
  }
}

// showAccess is called when the tab is opened, so the list is current rather
// than whatever it was when the page loaded.
export async function showAccess() {
  await loadPrincipals();
}

// mount decides whether the tab exists at all. An open store — nothing
// configured — shows it, because everything is permitted there and hiding the
// page would only puzzle whoever is setting the store up.
export function mount(me) {
  const permitted = !me.authenticated || (me.permissions || []).includes("principal:admin");
  if (!permitted) return false;

  document.getElementById("access-tab-item").hidden = false;
  roleCheckboxes(document.getElementById("access-new-roles"), [], "new");
  document.getElementById("access-form").addEventListener("submit", issueKey);
  document.querySelector("#access-table tbody").addEventListener("click", onTableClick);
  document.getElementById("close-access-key")
    .addEventListener("click", () => document.getElementById("access-key-dialog").close());
  document.getElementById("access-key-copy").addEventListener("click", async () => {
    const value = document.getElementById("access-key-value").value;
    try {
      await navigator.clipboard.writeText(value);
      document.getElementById("access-key-copy").textContent = "Copied";
    } catch {
      // Clipboard access needs a secure context, which a store reached over
      // plain HTTP on a lab network is not. Selecting the text is the fallback
      // that always works.
      document.getElementById("access-key-value").select();
      document.getElementById("access-key-copy").textContent = "Press Ctrl+C";
    }
  });
  return true;
}
