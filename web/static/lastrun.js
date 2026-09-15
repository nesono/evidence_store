// What a tester was last working on, remembered on this device (#162).
//
// At a rig the next record is nearly always about the same repository, branch,
// build and procedure as the last one, and retyping a commit hash on a phone is
// where mistakes come from. So those four are kept after a record is filed and
// put back into the form's empty fields the next time it opens. Never over
// something already typed, and never the result, the log or anything else that
// belongs to a single run.

export const LAST_RUN_KEY = "evidence.addform.lastRun";
export const REMEMBERED = ["repo", "branch", "rcs_ref", "procedure_ref"];

// pickLastRun keeps the remembered fields of a record, and only the ones with
// something in them.
export function pickLastRun(record) {
  const out = {};
  for (const name of REMEMBERED) {
    const value = record?.[name];
    if (typeof value === "string" && value.trim()) out[name] = value.trim();
  }
  return out;
}

// fieldsToRestore says which remembered values go back into the form: those
// whose field is empty now.
export function fieldsToRestore(saved, current) {
  const out = {};
  for (const name of REMEMBERED) {
    const value = saved?.[name];
    if (typeof value === "string" && value && !String(current?.[name] ?? "").trim()) out[name] = value;
  }
  return out;
}

// Storage can refuse (a private window, a browser set to block site data) or
// hold something that is not ours; neither may stop a record being filed.
export function rememberLastRun(record, storage = globalThis.localStorage) {
  try {
    storage.setItem(LAST_RUN_KEY, JSON.stringify(pickLastRun(record)));
  } catch { /* nothing to remember with */ }
}

export function loadLastRun(storage = globalThis.localStorage) {
  try {
    const saved = JSON.parse(storage.getItem(LAST_RUN_KEY));
    return saved && typeof saved === "object" ? saved : {};
  } catch {
    return {};
  }
}
