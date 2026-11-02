import test from "node:test";
import assert from "node:assert/strict";
import { readManualMetadata } from "../static/draftmanual.js";

function formFixture() {
  const fields = Object.fromEntries(
    ["tags", "observations", "location", "weather_conditions"].map((name) => [
      name,
      { value: "", dataset: {} },
    ]),
  );
  const rows = [];
  return {
    fields,
    rows,
    form: {
      elements: { namedItem: (name) => fields[name] },
      querySelectorAll: () => rows,
    },
  };
}

test("manual draft fields retain observation provenance only while text matches", () => {
  const { fields, form } = formFixture();
  fields.tags.value = "vehicle, regression";
  fields.observations.value = "A test log";
  fields.location.value = "52.5, 13.4";
  fields.location.dataset = { fromDevice: "52.5, 13.4", accuracyM: "3" };
  fields.weather_conditions.value = "Rain";
  fields.weather_conditions.dataset = {
    fromService: "Rain",
    observedAt: "2026-10-25T10:00:00Z",
  };
  let metadata = readManualMetadata(form);
  assert.equal(metadata.location_accuracy_m, 3);
  assert.equal(metadata.weather_observed_at, "2026-10-25T10:00:00Z");
  assert.deepEqual(metadata.tags, ["vehicle", "regression"]);
  fields.location.value = "Lab";
  fields.weather_conditions.value = "Sunny";
  metadata = readManualMetadata(form);
  assert.ok(!Object.hasOwn(metadata, "location_accuracy_m"));
  assert.ok(!Object.hasOwn(metadata, "weather_observed_at"));
});

test("custom metadata retains JSON defaults and rejects overriding procedure identity", () => {
  const { rows, form } = formFixture();
  const key = { value: "object" },
    value = { value: '{"count":2}' };
  rows.push({
    dataset: { original: '{"count":2}', display: '{"count":2}' },
    querySelector: (selector) =>
      selector === ".draft-custom-key" ? key : value,
  });
  assert.deepEqual(readManualMetadata(form).object, { count: 2 });
  value.value = "changed";
  assert.equal(readManualMetadata(form).object, "changed");
  key.value = "procedure_ref";
  assert.throws(() => readManualMetadata(form), /reserved/);
});
