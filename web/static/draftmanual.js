// The familiar manual-result context, shared with Add Result's geolocation,
// weather, Markdown, attachment, and upload helpers. Procedure identity is shown
// read-only and never included in a draft update payload.
import { apiFetchNoRedirect, mayDo } from "./common.js";
import { parseUserDateTime } from "./datetime.js";
import {
  requestPosition,
  formatCoordinates,
  formatAccuracy,
} from "./location.js";
import {
  weatherPoint,
  fetchWeather,
  describeReading,
  composeWeather,
} from "./weather.js";
import { attachImageUploads, embedImages, hydrateImages } from "./images.js";
import { uploadPhotosOf } from "./outboxview.js";
import { mountMarkdownEditor } from "./markdownedit.js";
import { renderMarkdown } from "./markdown.js";
import { setValue } from "./editing.js";

const managed = new Set([
  "tags",
  "notes",
  "observations",
  "location",
  "location_accuracy_m",
  "weather_conditions",
  "weather_observed_at",
]);
const reserved = new Set([
  "campaign",
  "procedure_modified",
  "repo",
  "branch",
  "rcs_ref",
  "procedure_ref",
  "source",
  "result",
  "finished_at",
  ...managed,
]);

export function readManualMetadata(form) {
  const field = (name) => form.elements.namedItem(name);
  const metadata = {};
  metadata.tags = field("tags")
    .value.split(",")
    .map((tag) => tag.trim())
    .filter(Boolean);
  for (const name of ["observations", "location", "weather_conditions"]) {
    if (field(name).value.trim()) metadata[name] = field(name).value.trim();
  }
  if (/!\[attaching…\]\(#pending-\d+\)/.test(metadata.observations || ""))
    throw new Error("Wait for attachments to finish before saving.");
  const location = field("location"),
    weather = field("weather_conditions");
  if (
    metadata.location &&
    location.dataset.fromDevice === metadata.location &&
    Number.isFinite(Number(location.dataset.accuracyM))
  ) {
    metadata.location_accuracy_m = Number(location.dataset.accuracyM);
  }
  if (
    metadata.weather_conditions &&
    weather.dataset.fromService === metadata.weather_conditions &&
    weather.dataset.observedAt
  ) {
    metadata.weather_observed_at = weather.dataset.observedAt;
  }
  for (const row of form.querySelectorAll(".draft-custom-row")) {
    const key = row.querySelector(".draft-custom-key").value.trim();
    const value = row.querySelector(".draft-custom-value").value;
    if (!key) continue;
    if (reserved.has(key))
      throw new Error(
        `Metadata key "${key}" has its own field or is reserved.`,
      );
    if (Object.hasOwn(metadata, key))
      throw new Error(`Duplicate metadata key "${key}".`);
    // Keep package/API JSON values intact when unedited; new values use the same
    // text convention as Add Result's key/value editor.
    const original = row.dataset.original;
    Object.defineProperty(metadata, key, {
      value:
        original && value === row.dataset.display
          ? JSON.parse(original)
          : value,
      enumerable: true,
      writable: true,
      configurable: true,
    });
  }
  return metadata;
}

export function mountDraftManual(
  root,
  me,
  { changed, status, contextID, editable },
) {
  const el = (id) => root.querySelector(`#${id}`);
  const form = el("campaign-draft-form"),
    field = (name) => form.elements.namedItem(name);
  let generation = 0,
    operations = 0;
  const canAttach = mayDo(me, "blob:write");
  const canSource = !me?.authenticated || mayDo(me, "source:any");
  field("source").readOnly = !canSource;
  el("draft-add-photo").hidden = !canAttach;
  mountMarkdownEditor(field("observations"));
  if (canAttach)
    attachImageUploads(field("observations"), (message) => {
      status(message);
      changed();
    });

  function customRow(key = "", value = "") {
    const row = document.createElement("div");
    row.className = "draft-custom-row custom-field-row grid";
    const keyInput = document.createElement("input"),
      valueInput = document.createElement("input"),
      remove = document.createElement("button");
    keyInput.className = "draft-custom-key";
    keyInput.value = key;
    keyInput.placeholder = "key";
    keyInput.setAttribute("aria-label", "Metadata key");
    valueInput.className = "draft-custom-value";
    valueInput.value =
      typeof value === "string" ? value : JSON.stringify(value);
    valueInput.placeholder = "value";
    valueInput.setAttribute("aria-label", "Metadata value");
    row.dataset.original = JSON.stringify(value);
    row.dataset.display = valueInput.value;
    remove.type = "button";
    remove.className = "secondary outline";
    remove.textContent = "×";
    remove.setAttribute("aria-label", "Remove metadata field");
    remove.addEventListener("click", () => {
      row.remove();
      changed();
    });
    row.append(keyInput, valueInput, remove);
    el("draft-custom-fields").append(row);
  }

  function preview() {
    el("draft-log-preview").innerHTML = renderMarkdown(
      field("observations").value,
    );
    if (!el("draft-log-preview").hidden && mayDo(me, "blob:read"))
      hydrateImages(el("draft-log-preview"));
  }
  function write(input, value) {
    setValue(input, value);
    changed();
  }
  el("draft-log-preview-toggle").addEventListener("click", (event) => {
    el("draft-log-preview").hidden = !el("draft-log-preview").hidden;
    event.currentTarget.setAttribute(
      "aria-expanded",
      String(!el("draft-log-preview").hidden),
    );
    preview();
  });
  field("observations").addEventListener("input", preview);
  el("draft-custom-add").addEventListener("click", () => {
    customRow();
    changed();
  });
  el("draft-add-photo").addEventListener("click", () =>
    el("draft-photo-input").click(),
  );
  el("draft-photo-input").addEventListener("change", (event) => {
    if (!editable()) return;
    embedImages(field("observations"), event.target.files, (message) => {
      status(message);
      changed();
    });
    event.target.value = "";
  });

  async function lookup(button, work, apply) {
    if (!editable()) return;
    const token = ++generation,
      id = contextID();
    operations++;
    button.disabled = true;
    try {
      const result = await work();
      if (token === generation && id === contextID() && editable())
        apply(result);
    } catch (error) {
      if (token === generation && id === contextID()) status(error.message);
    } finally {
      operations--;
      button.disabled = false;
    }
  }
  el("draft-locate").addEventListener("click", (event) => {
    const before = field("location").value;
    lookup(event.currentTarget, requestPosition, (fix) => {
      if (field("location").value !== before) return;
      write(field("location"), formatCoordinates(fix.lat, fix.lon));
      field("location").dataset.fromDevice = field("location").value;
      field("location").dataset.accuracyM = fix.accuracy;
      el("draft-location-status").textContent =
        `Device position ${formatAccuracy(fix.accuracy)}`;
    });
  });
  el("draft-weather-lookup").addEventListener("click", (event) => {
    const location = field("location").value,
      weather = field("weather_conditions").value,
      finished = el("campaign-finished-at").value;
    const when = finished ? parseUserDateTime(finished) : new Date();
    if (!when) {
      status("Correct the finish time before looking up weather.");
      return;
    }
    lookup(
      event.currentTarget,
      async () => {
        const point = await weatherPoint(location, requestPosition);
        return {
          point,
          reading: await fetchWeather(point, when, apiFetchNoRedirect),
        };
      },
      ({ point, reading }) => {
        if (
          location !== field("location").value ||
          weather !== field("weather_conditions").value ||
          finished !== el("campaign-finished-at").value
        )
          return;
        write(field("weather_conditions"), reading.summary);
        field("weather_conditions").dataset.fromService = reading.summary;
        if (reading.observed_at)
          field("weather_conditions").dataset.observedAt = reading.observed_at;
        el("draft-weather-status").textContent = describeReading(
          reading,
          point.source,
        );
      },
    );
  });
  field("location").addEventListener("input", () => {
    if (field("location").dataset.fromDevice === field("location").value)
      return;
    delete field("location").dataset.fromDevice;
    delete field("location").dataset.accuracyM;
    el("draft-location-status").textContent = "";
  });
  field("weather_conditions").addEventListener("input", () => {
    if (
      field("weather_conditions").dataset.fromService ===
      field("weather_conditions").value
    )
      return;
    delete field("weather_conditions").dataset.fromService;
    delete field("weather_conditions").dataset.observedAt;
    el("draft-weather-status").textContent = "";
  });
  el("draft-weather-write").addEventListener("click", () => {
    el("draft-weather-compose").hidden = !el("draft-weather-compose").hidden;
  });
  el("draft-weather-apply").addEventListener("click", () => {
    write(
      field("weather_conditions"),
      composeWeather({
        description: el("draft-wc-description").value,
        temperatureC: el("draft-wc-temperature").value,
        windKph: el("draft-wc-wind").value,
        humidity: el("draft-wc-humidity").value,
        precipitationMm: el("draft-wc-precipitation").value,
      }),
    );
    el("draft-weather-compose").hidden = true;
  });
  return {
    restore(row, definition) {
      ++generation;
      const m = row.data.metadata ?? {
        ...definition.defaults.metadata,
        tags: definition.defaults.tags || [],
      };
      field("procedure_ref").value = row.procedure_ref;
      field("evidence_type").value =
        row.data.evidence_type || definition.defaults.evidence_type;
      field("source").value = row.data.source || row.owner;
      field("tags").value = (m.tags || []).join(", ");
      for (const name of ["observations", "location", "weather_conditions"])
        field(name).value = m[name] || "";
      for (const name of ["location", "weather_conditions"])
        for (const key of Object.keys(field(name).dataset))
          delete field(name).dataset[key];
      if (
        m.location_accuracy_m !== undefined &&
        m.location_accuracy_m !== null
      ) {
        field("location").dataset.fromDevice = m.location;
        field("location").dataset.accuracyM = m.location_accuracy_m;
      }
      if (m.weather_observed_at) {
        field("weather_conditions").dataset.fromService = m.weather_conditions;
        field("weather_conditions").dataset.observedAt = m.weather_observed_at;
      }
      el("draft-location-status").textContent = "";
      el("draft-weather-status").textContent = "";
      el("draft-weather-compose").hidden = true;
      for (const id of [
        "description",
        "temperature",
        "wind",
        "humidity",
        "precipitation",
      ])
        el(`draft-wc-${id}`).value = "";
      el("draft-custom-fields").replaceChildren();
      for (const [key, value] of Object.entries(m))
        if (!managed.has(key)) customRow(key, value);
      el("draft-software-context").open =
        !row.data.repo || !row.data.branch || !row.data.rcs_ref;
      el("draft-software-summary").textContent =
        row.data.repo && row.data.rcs_ref
          ? `Software under test: ${row.data.repo} · ${row.data.branch} · ${row.data.rcs_ref} — expand to override`
          : "Software under test — set once for this campaign";
      preview();
    },
    async prepare(data) {
      if (operations)
        throw new Error(
          "Wait for the location/weather lookup to finish before saving.",
        );
      const result = await uploadPhotosOf({ metadata: data.metadata });
      if (!result.ok)
        throw new Error(
          result.error || "Could not upload test-log attachments.",
        );
    },
    pending() {
      return (
        operations > 0 ||
        /!\[attaching…\]\(#pending-\d+\)/.test(field("observations").value)
      );
    },
  };
}
