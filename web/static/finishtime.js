// A native picker edits UTC wall-clock components. datetime-local does not
// carry a timezone, so conversions explicitly add/remove Z, never local offsets.
export function finishPickerValue(value) {
  if (!value || !/(Z|[+-]\d{2}:\d{2})$/.test(value.trim())) return "";
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date.toISOString().slice(0, -1) : "";
}

export function finishTimeFromPicker(value) {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,3})?)?$/.test(value)) return "";
  const date = new Date(value + "Z");
  return Number.isFinite(date.getTime()) ? date.toISOString() : "";
}

export function mountFinishTime(root, createdAt, changed = () => {}) {
  const field = root.querySelector("#campaign-finished-at");
  const picker = root.querySelector("#campaign-finished-picker");
  const now = root.querySelector("#campaign-finished-now");
  const created = root.querySelector("#campaign-finished-created");
  function set(value) {
    field.value = value || "";
    picker.value = finishPickerValue(field.value);
    changed();
  }
  field.addEventListener("input", () => { picker.value = finishPickerValue(field.value); changed(); });
  picker.addEventListener("input", () => { field.value = finishTimeFromPicker(picker.value); changed(); });
  now.addEventListener("click", () => set(new Date().toISOString()));
  created.addEventListener("click", () => set(createdAt()));
  return {
    set,
    lock(disabled) {
      field.disabled = picker.disabled = now.disabled = disabled;
      created.disabled = disabled || !finishPickerValue(createdAt() || "");
    },
  };
}
