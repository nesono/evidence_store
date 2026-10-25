import test from "node:test";
import assert from "node:assert/strict";
import { finishPickerValue, finishTimeFromPicker, mountFinishTime } from "../static/finishtime.js";

test("finish picker represents UTC, preserving seconds and milliseconds", () => {
  assert.equal(finishPickerValue("2026-10-25T14:30:12.345+02:00"), "2026-10-25T12:30:12.345");
  assert.equal(finishTimeFromPicker("2026-10-25T12:30:12.345"), "2026-10-25T12:30:12.345Z");
  assert.equal(finishTimeFromPicker("2026-10-25T12:30"), "2026-10-25T12:30:00.000Z");
  assert.equal(finishPickerValue("not a date"), "");
  assert.equal(finishPickerValue("2026-10-25T12:30"), "", "text input must specify its timezone");
  assert.equal(finishTimeFromPicker(""), "");
  assert.equal(finishTimeFromPicker("2026-10-25T12:30Z"), "");
});

test("shortcuts and picker sync without defaulting or changing original creation time", () => {
  const nodes = Object.fromEntries(["at", "picker", "now", "created"].map(name => [name, {
    value: "", disabled: false, handlers: {},
    addEventListener(event, callback) { this.handlers[event] = callback; },
  }]));
  const root = { querySelector: id => nodes[id.replace("#campaign-finished-", "")] };
  let createdAt = "2026-10-24T09:15:23.123456Z";
  const control = mountFinishTime(root, () => createdAt);
  assert.equal(nodes.at.value, "");
  nodes.created.handlers.click();
  assert.equal(nodes.at.value, createdAt, "original timestamp keeps its full precision in text");
  assert.equal(nodes.picker.value, "2026-10-24T09:15:23.123");
  nodes.at.value = "2026-10-25T14:00:00+02:00";
  nodes.at.handlers.input();
  assert.equal(nodes.picker.value, "2026-10-25T12:00:00.000");
  nodes.picker.value = "2026-10-26T10:20";
  nodes.picker.handlers.input();
  assert.equal(nodes.at.value, "2026-10-26T10:20:00.000Z");
  const before = Date.now();
  nodes.now.handlers.click();
  assert.ok(Date.parse(nodes.at.value) >= before && Date.parse(nodes.at.value) <= Date.now());
  control.lock(true);
  assert.ok(Object.values(nodes).every(node => node.disabled));
  control.lock(false);
  assert.ok(Object.values(nodes).every(node => !node.disabled));
  createdAt = undefined;
  control.lock(false);
  assert.equal(nodes.created.disabled, true);
});
