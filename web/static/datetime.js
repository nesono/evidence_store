// Date arithmetic for the filter fields and the range picker built on them.
//
// Everything here is a pure function over strings and Dates, with no DOM: the
// calendar's month grids and range selection are the parts worth testing, and
// they are testable only if drawing them lives elsewhere (datepicker.js).
//
// Every instant in the store is UTC, and so is every value these functions
// produce. A user in Berlin picking "30 March" means the store's 30 March, not
// their local one shifted an hour or two — so days are handled as UTC day keys
// throughout and the browser's zone never enters into it.

// Times a bare day means at each end of a range. Filters read as "finished
// after" and "finished before", so a day picked as the start includes all of
// itself, and one picked as the end does too.
export const DEFAULT_START_TIME = "00:00";
export const DEFAULT_END_TIME = "23:59";

const DAY_MS = 86400000;
const DAY_KEY = /^\d{4}-\d{2}-\d{2}$/;

const MONTH_NAMES = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];

// Monday-first, matching the ISO week the rest of the world's calendars use.
export const WEEKDAY_NAMES = ["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"];

// Parse a user-entered datetime. Zoneless values are read as UTC, since that is
// what the field is labelled and what the store holds.
export function parseUserDateTime(str) {
  if (typeof str !== "string") return null;
  str = str.trim();
  if (!str) return null;
  // With an explicit zone (Z or ±HH:MM) the string already says which instant it
  // means, so it is parsed as written.
  if (/Z$|[+-]\d{2}:?\d{2}$/.test(str)) {
    const d = new Date(str);
    return isNaN(d.getTime()) ? null : d;
  }
  // Zoneless: normalize the separator and pin it to UTC. A bare date parses as
  // UTC midnight under this rule too, which is what `new Date("2026-03-30")`
  // already does — spelling it out keeps both forms on the same path.
  const d = new Date(str.replace(" ", "T") + "Z");
  return isNaN(d.getTime()) ? null : d;
}

// Split an instant into the two halves the picker edits.
export function splitUTC(date) {
  return { day: dayKey(date), time: `${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}` };
}

// Rejoin them. An empty time means the start of the day; callers that want a day
// to mean its end pass DEFAULT_END_TIME.
export function joinUTC(day, time) {
  if (!DAY_KEY.test(day || "")) return null;
  const d = new Date(`${day}T${time || "00:00"}:00Z`);
  return isNaN(d.getTime()) ? null : d;
}

// The UTC day an instant falls in, as the key used everywhere else here.
export function todayUTC(now = new Date()) {
  return dayKey(now);
}

// Six weeks of seven days covering `month` (0-based) plus the days either side
// that share its weeks. Always six rows: a grid that changed height as the user
// paged through months would make the popup jump under the pointer.
export function monthMatrix(year, month) {
  const first = Date.UTC(year, month, 1);
  // getUTCDay() is Sunday-first (0); shift so Monday is 0.
  const lead = (new Date(first).getUTCDay() + 6) % 7;
  const start = first - lead * DAY_MS;

  const weeks = [];
  for (let w = 0; w < 6; w++) {
    const week = [];
    for (let d = 0; d < 7; d++) {
      const date = new Date(start + (w * 7 + d) * DAY_MS);
      week.push({ day: dayKey(date), inMonth: date.getUTCMonth() === month });
    }
    weeks.push(week);
  }
  return weeks;
}

export function shiftMonth({ year, month }, delta) {
  const d = new Date(Date.UTC(year, month + delta, 1));
  return { year: d.getUTCFullYear(), month: d.getUTCMonth() };
}

export function monthLabel({ year, month }) {
  return `${MONTH_NAMES[month]} ${year}`;
}

// What clicking `day` does to the current selection.
//
// With one end already set the click completes the range, but only if it lands
// on the right side of it; a click the wrong way round would otherwise produce a
// range matching nothing. Rather than silently swapping the ends — which moves
// the end the user was not pointing at — that click starts a fresh range from
// where they clicked, so a second click always lands somewhere predictable.
export function nextSelection({ from, to }, day) {
  if (from && !to) {
    return day >= from ? { from, to: day } : { from: day, to: "" };
  }
  if (!from && to) {
    return day <= to ? { from: day, to } : { from: day, to: "" };
  }
  return { from: day, to: "" };
}

// Read the two filter fields into a selection the calendar can draw.
//
// A field the parser cannot make sense of reads as unset: the picker has nothing
// to show for it, and guessing at half a value would be worse than showing none.
// The text itself is left alone until the user picks something, so a typo is
// still there to fix rather than silently erased.
export function readRange(fromText, toText) {
  const from = parseUserDateTime(fromText);
  const to = parseUserDateTime(toText);
  return {
    from: from ? splitUTC(from).day : "",
    to: to ? splitUTC(to).day : "",
    fromTime: from ? splitUTC(from).time : DEFAULT_START_TIME,
    toTime: to ? splitUTC(to).time : DEFAULT_END_TIME,
  };
}

// The inverse: what the two filter fields should say for a selection. An end
// with no day is an end with no limit, and writes as an empty field.
export function writeRange({ from, to, fromTime, toTime }) {
  return {
    from: from ? `${from} ${fromTime || DEFAULT_START_TIME}` : "",
    to: to ? `${to} ${toTime || DEFAULT_END_TIME}` : "",
  };
}

// The month the calendar should open on: where the selection is, or where the
// user is in time if there is no selection yet.
export function visibleMonth({ from, to }, now = new Date()) {
  const anchor = joinUTC(from || to, "") || now;
  return { year: anchor.getUTCFullYear(), month: anchor.getUTCMonth() };
}

// Whether a day sits inside a closed range. Day keys are fixed-width and
// zero-padded, so lexical comparison is chronological.
export function dayInRange(day, { from, to }) {
  if (!from || !to) return false;
  return day >= from && day <= to;
}

function dayKey(date) {
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
}

function pad(n) {
  return String(n).padStart(2, "0");
}
