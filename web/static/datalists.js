// The suggestion lists behind the repo and source boxes.
//
// Its own module because both forms use them: the filter bar suggests what is
// already in the store, and the Add Result form suggests the same values so a
// tester spells a repo the way every other record spells it. Neither view owns
// them, so neither holds them.

import { API_BASE, apiFetch, esc } from "./common.js";

const DATALIST_FIELDS = [
  { field: "repo",   listId: "repos-list" },
  { field: "source", listId: "sources-list" },
];

export async function refreshDatalists() {
  await Promise.all(DATALIST_FIELDS.map(async ({ field, listId }) => {
    const resp = await apiFetch(`${API_BASE}/evidence/distinct?field=${field}&limit=500`);
    if (!resp.ok) return;
    const { values } = await resp.json();
    const list = document.getElementById(listId);
    if (!list) return;
    list.innerHTML = (values || []).map(v => `<option value="${esc(v)}"></option>`).join("");
  }));
}
