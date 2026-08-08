// Shared between the search/add view (app.js) and the analytics view
// (analytics.js). Extracted so the two can use the same API client and escaping
// without importing each other, which would make the module graph cyclic.

export const API_BASE = "/api/v1";

// --- Auth ---

const API_KEY_STORAGE = "evidence_api_key";

export function getStoredAPIKey() {
  return localStorage.getItem(API_KEY_STORAGE) || "";
}

export function setStoredAPIKey(key) {
  if (key) {
    localStorage.setItem(API_KEY_STORAGE, key);
  } else {
    localStorage.removeItem(API_KEY_STORAGE);
  }
  updateAuthUI();
}

export function updateAuthUI() {
  const btn = document.getElementById("auth-logout");
  if (btn) btn.hidden = !getStoredAPIKey();
}

export function promptForAPIKey(msg) {
  const key = prompt(msg || "Enter your API key:");
  if (key !== null) {
    setStoredAPIKey(key.trim());
  }
  return getStoredAPIKey();
}

// Wrapper around fetch that attaches Authorization header and handles 401.
export async function apiFetch(url, options = {}) {
  const key = getStoredAPIKey();
  if (key) {
    options.headers = { ...options.headers, Authorization: `Bearer ${key}` };
  }
  const resp = await fetch(url, options);
  if (resp.status === 401) {
    const newKey = promptForAPIKey("Authentication required. Enter your API key:");
    if (newKey) {
      options.headers = { ...options.headers, Authorization: `Bearer ${newKey}` };
      return fetch(url, options);
    }
  }
  return resp;
}

// --- Formatting ---

export function esc(str) {
  const d = document.createElement("div");
  d.textContent = str;
  return d.innerHTML;
}

export function formatTime(iso) {
  const d = new Date(iso);
  const pad = n => String(n).padStart(2, "0");
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
}

export function resultBadge(result) {
  const cls = result ? result.toLowerCase() : "unknown";
  return `<span class="badge badge-${cls}">${result || "?"}</span>`;
}
