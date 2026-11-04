// Prepared campaigns and a write-ahead journal. Nothing is called saved until
// its IndexedDB transaction commits; HTTP retries replay the persisted payload.
import { API_BASE, apiFetchNoRedirect } from "./common.js";
import { digestOf, digestsInRecord } from "./blobref.js";

export function campaignStore(scope, indexedDB = globalThis.indexedDB) {
  let opening;
  const open = () => opening ||= new Promise((resolve, reject) => {
    if (!indexedDB) return reject(new Error("Offline campaigns require IndexedDB."));
    const request = indexedDB.open("evidence-campaigns", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("items", { keyPath: "key" });
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
  });
  async function run(mode, fn) {
    const db = await open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction("items", mode);
      const request = fn(tx.objectStore("items"));
      tx.oncomplete = () => resolve(request.result);
      tx.onerror = tx.onabort = () => reject(tx.error || new Error("Local storage transaction failed."));
    });
  }
  return {
    async get(kind, id) { return (await run("readonly", s => s.get([scope, kind, id])))?.value; },
    async all(kind) { return (await run("readonly", s => s.getAll())).filter(r => r.key[0] === scope && r.key[1] === kind).map(r => r.value); },
    async put(kind, id, value) { await run("readwrite", s => s.put({ key: [scope, kind, id], value })); },
    async remove(kind, id) { await run("readwrite", s => s.delete([scope, kind, id])); },
  };
}

export async function campaignRequest(path, method = "GET", data) {
  const response = await apiFetchNoRedirect(API_BASE + path, {
    method, cache: "no-store",
    ...(data ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(data) } : {}),
  });
  if (!response.ok) {
    let message = `HTTP ${response.status}`;
    try { message = (await response.json()).error || message; } catch { /* proxy response */ }
    throw Object.assign(new Error(message), { status: response.status });
  }
  return response.json();
}

export function localAttempt(entry) {
  const row = structuredClone(entry.row);
  if (entry.operations.length) row.data = structuredClone(entry.operations.at(-1).data);
  return { ...row, local_version: entry.version, local_pending: !!(entry.create || entry.operations.length || entry.completion),
    local_complete: !!entry.completion, local_error: entry.error || "", local_conflict: entry.conflict || null };
}

export function createCampaignOffline({ me, store = campaignStore(me?.subject || "anonymous"), request = campaignRequest,
  fetchBlob = digest => apiFetchNoRedirect(`${API_BASE}/blobs/${digest}`),
  fetchArchive = id => apiFetchNoRedirect(`${API_BASE}/campaigns/${id}/package`),
  uploadBlob = blob => apiFetchNoRedirect(`${API_BASE}/blobs`, { method: "POST", headers: { "Content-Type": blob.contentType }, body: blob.bytes }),
  fallbackBlob = async () => null,
  lock = fn => globalThis.navigator?.locks ? navigator.locks.request(`campaigns:${me?.subject || "anonymous"}`, fn) : Promise.reject(new Error("Offline campaigns require browser Web Locks support.")),
} = {}) {
  const subject = me?.subject || "anonymous";
  async function identity() {
    const current = await request("/me");
    if ((current.subject || "anonymous") !== subject) throw Object.assign(new Error(`Sign in as ${subject} to synchronize this work.`), { status: 401 });
  }
  const getEntry = id => store.get("attempt", id);
  async function verified(response, digest) {
    if (!response.ok) throw Object.assign(new Error(`Asset download failed (HTTP ${response.status}).`), { status: response.status });
    const bytes = await response.arrayBuffer();
    if (await digestOf(bytes) !== digest) throw new Error(`Asset digest mismatch: ${digest}`);
    return { digest, bytes, contentType: response.headers.get("Content-Type") || "application/octet-stream" };
  }
  async function retain(data, download = false) {
    for (const digest of digestsInRecord(data)) {
      if (await store.get("blob", digest)) continue;
      let blob = await fallbackBlob(digest);
      if (!blob && download) blob = await verified(await fetchBlob(digest), digest);
      if (!blob) throw new Error("Attachment is not stored on this device. Prepare the campaign again while connected.");
      await store.put("blob", digest, blob);
    }
  }
  async function remember(row) {
    const existing = await getEntry(row.id);
    if (existing && localAttempt(existing).local_pending) return localAttempt(existing);
    await retain(row.data, true);
    const entry = { row, operations: [], version: (existing?.version || 0) + (existing && existing.row.revision !== row.revision ? 1 : 0) };
    await store.put("attempt", row.id, entry);
    return localAttempt(entry);
  }
  async function saveEntry(row, data) {
    const entry = await getEntry(row.id);
    if (!entry || entry.version !== row.local_version) throw new Error("This local draft changed in another tab. Reopen it before editing.");
    if (entry.completion || entry.row.finalized_at) throw new Error("This attempt is already completed. Synchronize or resolve its conflict first.");
    await retain(data);
    const operation = { operation_id: crypto.randomUUID(), data: structuredClone(data) };
    // An operation with a revision may already have reached the server. Keep it
    // byte-for-byte until its response has been acknowledged on disk.
    if (entry.operations.length && !entry.operations.at(-1).revision) entry.operations.pop();
    entry.operations.push(operation);
    entry.version++;
    await store.put("attempt", row.id, entry);
    return localAttempt(entry);
  }
  async function synchronize(entry) {
    if (entry.error) return;
    try {
      if (entry.create) {
        const row = await request("/campaign-attempts", "POST", entry.create);
        if (row.id !== entry.row.id) {
          entry.conflict = row;
          throw Object.assign(new Error("A colleague started this procedure while you were offline. Review both attempts."), { status: 409 });
        }
        entry.row = row; delete entry.create;
        await store.put("attempt", row.id, entry);
      }
      while (entry.operations.length) {
        const operation = entry.operations[0];
        operation.revision ||= entry.row.revision;
        await store.put("attempt", entry.row.id, entry);
        for (const digest of digestsInRecord(operation.data)) {
          const blob = await store.get("blob", digest);
          if (!blob) throw new Error("A local attachment is missing. Restore it before synchronizing.");
          // Re-upload even after a previous success: an unreferenced server blob
          // could have expired while the device was disconnected.
          const response = await uploadBlob(blob);
          if (!response.ok) throw Object.assign(new Error(`Attachment upload failed (HTTP ${response.status}).`), { status: response.status });
        }
        entry.row = await request(`/campaign-attempts/${entry.row.id}`, "PUT", operation);
        entry.operations.shift();
        await store.put("attempt", entry.row.id, entry);
      }
      if (entry.completion) {
        entry.completion.revision ||= entry.row.revision;
        await store.put("attempt", entry.row.id, entry);
        entry.row = await request(`/campaign-attempts/${entry.row.id}/finalize`, "POST", entry.completion);
        delete entry.completion;
        await store.put("attempt", entry.row.id, entry);
      }
    } catch (error) {
      if (error.status >= 400 && error.status < 500 && ![401, 408, 429].includes(error.status)) {
        entry.error = error.message;
        if (error.status === 409 && !entry.conflict) {
          try { entry.conflict = await request(`/campaign-attempts/${entry.row.id}`); } catch { /* retain local copy regardless */ }
        }
        await store.put("attempt", entry.row.id, entry);
      } else throw error;
    }
  }
  const api = {
    async prepared(id) { return store.get("campaign", id); },
    async campaigns() { return store.all("campaign"); },
    getBlob: digest => store.get("blob", digest),
    async stashBlob(blob) { await store.put("blob", blob.digest, blob); },
    async uploadAttachments(data) {
      await identity();
      for (const digest of digestsInRecord(data)) {
        const blob = await store.get("blob", digest) || await fallbackBlob(digest);
        if (!blob) continue; // An online-only attachment is already on the server.
        const response = await uploadBlob(blob);
        if (!response.ok) throw new Error(`Attachment upload failed (HTTP ${response.status}).`);
      }
    },
    async updateExecution(execution) { return lock(async () => {
      const prepared = await api.prepared(execution.id);
      if (prepared) await store.put("campaign", execution.id, { ...prepared, execution });
    }); },
    async prepare(id) { return lock(async () => {
      await identity();
      const execution = await request(`/campaign-executions/${id}`);
      const pkg = await request(`/campaigns/${execution.package_id}`);
      const archive = await verified(await fetchArchive(pkg.id), pkg.digest);
      const digests = new Set(pkg.definition.scenarios.flatMap(s => Object.values(s.assets || {})));
      for (const digest of digests) await store.put("blob", digest, await verified(await fetchBlob(digest), digest));
      for (const p of execution.procedures) {
        for (const attemptID of new Set([p.draft_id, p.latest_id].filter(Boolean))) {
          const row = await request(`/campaign-attempts/${attemptID}`);
          await retain(row.data, true);
          await remember(row);
        }
      }
      // The readiness marker is written last. Failed preparation is never ready.
      const prepared = { execution, pkg, archive, prepared_at: new Date().toISOString() };
      await store.put("campaign", id, prepared);
      return prepared;
    }); },
    async remember(row) { return lock(() => remember(row)); },
    async get(id) { const entry = await getEntry(id); return entry && localAttempt(entry); },
    async attempts(executionID, ref) { return (await store.all("attempt")).filter(e => e.row.execution_id === executionID && e.row.procedure_ref === ref).map(localAttempt).sort((a,b) => b.created_at.localeCompare(a.created_at)); },
    async start(executionID, ref) { return lock(async () => {
      const prepared = await api.prepared(executionID);
      if (!prepared) throw new Error("Prepare this campaign while connected before working offline.");
      const { execution, pkg } = prepared;
      if (execution.closed_at) throw new Error("This campaign is closed.");
      const prior = (await api.attempts(executionID, ref)).find(a => !a.finalized_at && !a.local_complete);
      if (prior) return prior;
      if ((await api.attempts(executionID, ref)).some(a => a.local_complete)) throw new Error("Synchronize the completed attempt before starting a retry.");
      const scenario = pkg.definition.scenarios.find(s => s.definition.procedure_ref === ref);
      if (!scenario) throw new Error("Procedure is not in the prepared package.");
      const defaults = scenario.definition.defaults;
      const data = { repo: execution.repo, branch: execution.branch, rcs_ref: execution.rcs_ref, source: subject,
        evidence_type: defaults.evidence_type, metadata: { ...defaults.metadata, tags: defaults.tags || [] }, answers: {}, result: "", notes: "" };
      for (const { field } of scenario.procedure.blocks) if (field && !field.bind && Object.hasOwn(field, "default")) data.answers[field.id] = structuredClone(field.default);
      const id = crypto.randomUUID(), now = new Date().toISOString();
      const row = { id, execution_id: executionID, campaign_id: pkg.id, procedure_ref: ref, owner: subject, revision: 0, created_at: now, updated_at: now, data };
      const entry = { row, create: { id, execution_id: executionID, procedure_ref: ref }, operations: [], version: 0 };
      await store.put("attempt", id, entry);
      return localAttempt(entry);
    }); },
    async save(row, data) { return lock(() => saveEntry(row, data)); },
    async complete(row, finished_at) { return lock(async () => {
      const entry = await getEntry(row.id);
      if (!entry || entry.version !== row.local_version) throw new Error("Local draft changed. Reopen it before completing.");
      entry.completion ||= { finished_at };
      entry.version++;
      await store.put("attempt", row.id, entry);
      return localAttempt(entry);
    }); },
    async sync() { return lock(async () => {
      const pending = (await store.all("attempt")).filter(e => localAttempt(e).local_pending);
      if (!pending.length) return;
      await identity();
      for (const entry of pending) await synchronize(entry);
    }); },
    // Resolution is explicit and revision checked too. Preserve the local copy
    // in an archive before accepting remote data or replacing remote answers.
    async resolve(id, choice) { return lock(async () => {
      await identity();
      const entry = await getEntry(id);
      if (!entry?.error) throw new Error("There is no conflict to resolve.");
      const remote = await request(`/campaign-attempts/${entry.conflict?.id || id}`);
      if (choice !== "remote" && choice !== "local") throw new Error("Choose local or server answers.");
      if (choice === "local" && remote.finalized_at) throw new Error("Server attempt is finalized. Keep the server version, then start a new attempt; export your local answers first.");
      if (choice === "local" && entry.conflict && remote.revision !== entry.conflict.revision) throw new Error("Server draft changed again. Reload the conflict before resolving it.");
      await store.put("archive", `${id}:${crypto.randomUUID()}`, entry);
      await retain(remote.data, true);
      const replacement = { row: remote, version: entry.version + 1, operations: choice === "local" ? [{ operation_id: crypto.randomUUID(), revision: remote.revision, data: localAttempt(entry).data }] : [] };
      await store.put("attempt", remote.id, replacement);
      if (remote.id !== id) await store.remove("attempt", id);
      return localAttempt(replacement);
    }); },
    async export(id) { return getEntry(id); },
    async reloadConflict(id) { return lock(async () => {
      await identity();
      const entry = await getEntry(id);
      if (!entry?.error) return entry && localAttempt(entry);
      entry.conflict = await request(`/campaign-attempts/${entry.conflict?.id || id}`);
      await store.put("attempt", id, entry);
      return localAttempt(entry);
    }); },
  };
  return api;
}
