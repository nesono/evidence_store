// A from–to date and time picker over a pair of text filter fields.
//
// The fields stay the source of truth and stay editable: typing or pasting a
// datetime is often faster than clicking to it, and a link's `finished_after`
// has to land somewhere the user can see and edit. The picker is the other way
// in — it reads the fields when it opens and writes them when the user picks, so
// neither side has to know the other exists beyond that.
//
// All arithmetic lives in datetime.js, which has no DOM in it and is unit
// tested; what is here is the widget.

import {
  DEFAULT_END_TIME,
  DEFAULT_START_TIME,
  WEEKDAY_NAMES,
  dayInRange,
  monthLabel,
  monthMatrix,
  monthName,
  nextSelection,
  readRange,
  shiftMonth,
  todayUTC,
  visibleMonth,
  writeRange,
} from "./datetime.js";

// attachRangePicker wires the trigger button and popup inside `root` to the two
// filter fields.
//
// `onApply` is called after the popup closes with the fields already written,
// for callers that want picking a range to run the search.
export function attachRangePicker({ root, fromInput, toInput, onApply }) {
  const trigger = root.querySelector("[data-dt-open]");
  const popup = root.querySelector("[data-dt-popup]");

  let selection = { from: "", to: "", fromTime: DEFAULT_START_TIME, toTime: DEFAULT_END_TIME };
  let month = visibleMonth(selection);
  // The day the arrow keys are on. Roving tabindex: exactly one day in the grid
  // is tabbable, so Tab leaves the calendar instead of walking 42 buttons.
  let focusDay = todayUTC();

  buildSkeleton(popup);
  const yearLabelEl = popup.querySelector(".dt-year");
  const monthLabelEl = popup.querySelector(".dt-month");
  const gridEl = popup.querySelector(".dt-grid");
  const gridBody = gridEl.querySelector("tbody");

  // --- State plumbing ---

  // Push the selection out to the fields. The `input` event is what the UTC
  // previews and the "More filters" badge listen to, so a picked range shows up
  // there the same as a typed one.
  function syncFields() {
    const text = writeRange(selection);
    setFieldValue(fromInput, text.from);
    setFieldValue(toInput, text.to);
  }

  function setFieldValue(input, value) {
    if (input.value === value) return;
    input.value = value;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function selectDay(day) {
    selection = { ...selection, ...nextSelection(selection, day) };
    focusDay = day;
    syncFields();
    renderCalendar();
    renderEnds();
    focusGridDay();
  }

  // Clearing one end drops that limit and leaves the other alone — the point of
  // the button is to go back to "no start" or "no end" without touching the
  // half of the range the user still wants.
  function clearEnd(end) {
    selection = end === "from"
      ? { ...selection, from: "", fromTime: DEFAULT_START_TIME }
      : { ...selection, to: "", toTime: DEFAULT_END_TIME };
    syncFields();
    renderCalendar();
    renderEnds();
  }

  // --- Rendering ---

  function renderCalendar() {
    yearLabelEl.textContent = String(month.year);
    monthLabelEl.textContent = monthName(month);
    // The two rows read as one thing to anyone looking at them; a screen reader
    // walking into the grid gets the whole of it in one go.
    gridEl.setAttribute("aria-label", monthLabel(month));
    const today = todayUTC();

    gridBody.innerHTML = monthMatrix(month.year, month.month).map(week => {
      const cells = week.map(({ day, inMonth }) => {
        const isFrom = day === selection.from;
        const isTo = day === selection.to;
        const classes = ["dt-day"];
        if (!inMonth) classes.push("dt-day-outside");
        if (isFrom || isTo) classes.push("dt-day-end");
        if (dayInRange(day, selection)) classes.push("dt-day-in-range");
        if (day === today) classes.push("dt-day-today");
        const label = isFrom && isTo ? "start and end of range"
          : isFrom ? "start of range"
          : isTo ? "end of range"
          : "";
        return `<td><button type="button" class="${classes.join(" ")}" data-day="${day}"
            tabindex="${day === focusDay ? "0" : "-1"}"
            aria-pressed="${isFrom || isTo}"
            aria-label="${dayLabel(day)}${label ? `, ${label}` : ""}"
          >${Number(day.slice(8, 10))}</button></td>`;
      });
      return `<tr>${cells.join("")}</tr>`;
    }).join("");

    // The grid never holds a day outside the month on show, so a focus day that
    // paged out of view has to move onto the visible month before the roving
    // tabindex points at nothing.
    if (!gridBody.querySelector(`[data-day="${focusDay}"]`)) {
      focusDay = firstDayOf(month);
      const el = gridBody.querySelector(`[data-day="${focusDay}"]`);
      if (el) el.tabIndex = 0;
    }
  }

  function renderEnds() {
    for (const end of ["from", "to"]) {
      const row = popup.querySelector(`.dt-end[data-end="${end}"]`);
      const day = selection[end];
      row.querySelector(".dt-end-day").textContent = day || "no limit";
      row.classList.toggle("dt-end-unset", !day);
      row.querySelector(".dt-time").value = end === "from" ? selection.fromTime : selection.toTime;
      // Nothing to clear when the end is already open.
      row.querySelector(".dt-end-clear").disabled = !day;
    }
  }

  // --- Open / close ---

  function open() {
    selection = readRange(fromInput.value, toInput.value);
    month = visibleMonth(selection);
    focusDay = selection.from || selection.to || todayUTC();
    renderCalendar();
    renderEnds();
    popup.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
    focusGridDay();
  }

  function close({ restoreFocus = true } = {}) {
    if (popup.hidden) return;
    popup.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    if (restoreFocus) trigger.focus();
  }

  function focusGridDay() {
    const el = gridBody.querySelector(`[data-day="${focusDay}"]`) || gridBody.querySelector(".dt-day");
    if (el) el.focus();
  }

  // --- Events ---

  trigger.addEventListener("click", () => {
    if (popup.hidden) open(); else close();
  });

  popup.addEventListener("click", (e) => {
    const day = e.target.closest("[data-day]");
    if (day) {
      selectDay(day.dataset.day);
      return;
    }
    const step = e.target.closest("[data-dt-month]");
    if (step) {
      month = shiftMonth(month, Number(step.dataset.dtMonth));
      renderCalendar();
      return;
    }
    if (e.target.closest("[data-dt-today]")) {
      // Only the view moves. Focus follows it into the grid, so today is one
      // Enter away for anyone who did want to pick it — but nothing is picked
      // until they say so.
      focusDay = todayUTC();
      month = visibleMonth({ from: focusDay, to: "" });
      renderCalendar();
      focusGridDay();
      return;
    }
    const clear = e.target.closest("[data-dt-clear]");
    if (clear) {
      clearEnd(clear.dataset.dtClear);
      return;
    }
    if (e.target.closest("[data-dt-clear-all]")) {
      clearEnd("from");
      clearEnd("to");
      return;
    }
    if (e.target.closest("[data-dt-apply]")) {
      close();
      if (onApply) onApply();
    }
  });

  popup.addEventListener("change", (e) => {
    const time = e.target.closest(".dt-time");
    if (!time) return;
    const end = time.closest(".dt-end").dataset.end;
    // A time with no day would be a limit at an unnamed date; the day the user
    // is most likely to mean is the one they are looking at, but guessing is
    // worse than waiting for them to pick, so the field stays as it is.
    selection = end === "from"
      ? { ...selection, fromTime: time.value || DEFAULT_START_TIME }
      : { ...selection, toTime: time.value || DEFAULT_END_TIME };
    syncFields();
  });

  // Arrow keys walk the calendar the way a spreadsheet walks cells, and paging
  // crosses months — without this the grid is 42 tab stops and no shortcuts.
  popup.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      close();
      e.stopPropagation();
      return;
    }
    const day = e.target.closest("[data-day]");
    if (!day) return;

    const moves = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 };
    let target = null;
    if (e.key in moves) {
      target = addDays(day.dataset.day, moves[e.key]);
    } else if (e.key === "PageUp" || e.key === "PageDown") {
      month = shiftMonth(month, e.key === "PageUp" ? -1 : 1);
      target = clampToMonth(day.dataset.day, month);
    } else if (e.key === "Home") {
      target = firstDayOf(month);
    } else if (e.key === "End") {
      target = lastDayOf(month);
    } else {
      return;
    }
    e.preventDefault();
    // Keep the walked-to day on screen: stepping off either edge pages the
    // calendar rather than leaving focus on a day nobody can see.
    focusDay = target;
    month = monthOf(target);
    renderCalendar();
    focusGridDay();
  });

  // A popup that outlives the click that dismissed it would sit over whatever
  // the user reached for next.
  //
  // The path is read from the event rather than from the DOM: picking a day
  // redraws the calendar, so by the time this runs the button that was clicked
  // is detached and `root.contains(e.target)` says false — which would close the
  // popup on every pick. composedPath() is fixed when the event is dispatched
  // and still names the popup.
  document.addEventListener("click", (e) => {
    if (!popup.hidden && !e.composedPath().includes(root)) close({ restoreFocus: false });
  });

  return { close, isOpen: () => !popup.hidden };
}

function buildSkeleton(popup) {
  // The year gets its own pair of arrows above the month's. Reaching last
  // December from August is one click that way; paging month by month it is
  // eight, and a range a year back is nobody's idea of a scroll.
  //
  // Both rows step the same value — a year is twelve months — so the two sit on
  // one handler and there is no second notion of "where the calendar is".
  popup.innerHTML = `
    <div class="dt-head" aria-live="polite">
      <button type="button" class="dt-nav" data-dt-month="-12" aria-label="Previous year">&laquo;</button>
      <span class="dt-year"></span>
      <button type="button" class="dt-nav" data-dt-month="12" aria-label="Next year">&raquo;</button>
      <button type="button" class="dt-nav" data-dt-month="-1" aria-label="Previous month">&lsaquo;</button>
      <span class="dt-month"></span>
      <button type="button" class="dt-nav" data-dt-month="1" aria-label="Next month">&rsaquo;</button>
      <!-- Moves the calendar, not the filter. Losing your place after paging
           back through a few years should not cost a selection to get out of. -->
      <button type="button" class="dt-today" data-dt-today
              title="Show the current month. Does not change the selection.">Go to today</button>
    </div>
    <table class="dt-grid">
      <thead><tr>${WEEKDAY_NAMES.map(d => `<th scope="col">${d}</th>`).join("")}</tr></thead>
      <tbody></tbody>
    </table>
    <div class="dt-ends">
      ${endRow("from", "After")}
      ${endRow("to", "Before")}
    </div>
    <div class="dt-actions">
      <button type="button" class="secondary outline" data-dt-clear-all>Clear both</button>
      <button type="button" data-dt-apply>Apply</button>
    </div>
    <small class="dt-hint">Times are UTC. Both ends are optional.</small>
  `;
}

function endRow(end, label) {
  return `
    <div class="dt-end" data-end="${end}">
      <span class="dt-end-label">${label}</span>
      <span class="dt-end-day"></span>
      <input type="time" class="dt-time" aria-label="${label} time (UTC)">
      <button type="button" class="secondary outline dt-end-clear" data-dt-clear="${end}"
              title="Remove the ${end === "from" ? "start" : "end"} limit">Clear</button>
    </div>
  `;
}

// --- Day-key arithmetic used only for moving focus ---

const DAY_MS = 86400000;

function addDays(day, delta) {
  return toKey(new Date(Date.parse(`${day}T00:00:00Z`) + delta * DAY_MS));
}

function monthOf(day) {
  return { year: Number(day.slice(0, 4)), month: Number(day.slice(5, 7)) - 1 };
}

function firstDayOf({ year, month }) {
  return toKey(new Date(Date.UTC(year, month, 1)));
}

function lastDayOf({ year, month }) {
  return toKey(new Date(Date.UTC(year, month + 1, 0)));
}

// Paging from the 31st into a shorter month lands on its last day rather than
// spilling into the next one.
function clampToMonth(day, month) {
  const last = Number(lastDayOf(month).slice(8, 10));
  const dayOfMonth = Math.min(Number(day.slice(8, 10)), last);
  return `${firstDayOf(month).slice(0, 8)}${String(dayOfMonth).padStart(2, "0")}`;
}

function toKey(date) {
  return date.toISOString().slice(0, 10);
}

function dayLabel(day) {
  return new Date(`${day}T00:00:00Z`).toLocaleDateString(undefined, {
    timeZone: "UTC", weekday: "long", year: "numeric", month: "long", day: "numeric",
  });
}
