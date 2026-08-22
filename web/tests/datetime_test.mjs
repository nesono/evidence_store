// Unit tests for web/static/datetime.js, run with `node --test`.
//
// The picker's arithmetic — which days a month grid holds, what a click does to
// a half-open range, how a typed string becomes a UTC instant — is the part that
// is easy to get subtly wrong and impossible to eyeball in a browser. It lives in
// a module with no DOM in it so it can be tested here; the widget that draws it
// stays in datepicker.js.

import test from "node:test";
import assert from "node:assert/strict";

import {
  DEFAULT_END_TIME,
  DEFAULT_START_TIME,
  dayInRange,
  joinUTC,
  monthLabel,
  monthMatrix,
  monthName,
  nextSelection,
  parseUserDateTime,
  readRange,
  shiftMonth,
  splitUTC,
  todayUTC,
  visibleMonth,
  writeRange,
} from "../static/datetime.js";

// --- parseUserDateTime ---

test("parses a zoneless date and time as UTC", () => {
  const d = parseUserDateTime("2026-03-30 14:00");
  assert.equal(d.toISOString(), "2026-03-30T14:00:00.000Z");
});

test("parses a bare date as midnight UTC", () => {
  const d = parseUserDateTime("2026-03-30");
  assert.equal(d.toISOString(), "2026-03-30T00:00:00.000Z");
});

test("accepts the ISO T separator", () => {
  const d = parseUserDateTime("2026-03-30T14:00:00");
  assert.equal(d.toISOString(), "2026-03-30T14:00:00.000Z");
});

test("honours an explicit zone offset", () => {
  const d = parseUserDateTime("2026-03-30T14:00:00+02:00");
  assert.equal(d.toISOString(), "2026-03-30T12:00:00.000Z");
});

test("honours a trailing Z", () => {
  const d = parseUserDateTime("2026-03-30T14:00:00Z");
  assert.equal(d.toISOString(), "2026-03-30T14:00:00.000Z");
});

test("rejects what is not a datetime", () => {
  assert.equal(parseUserDateTime("last tuesday"), null);
  assert.equal(parseUserDateTime("2026-13-40"), null);
  assert.equal(parseUserDateTime(""), null);
  assert.equal(parseUserDateTime("   "), null);
  assert.equal(parseUserDateTime(null), null);
});

// --- splitUTC / joinUTC ---

test("splits an instant into the day and time the picker edits", () => {
  assert.deepEqual(splitUTC(new Date("2026-03-30T14:05:00Z")), {
    day: "2026-03-30",
    time: "14:05",
  });
});

test("splits and rejoins without drifting", () => {
  const iso = "2026-12-31T23:59:00.000Z";
  const { day, time } = splitUTC(new Date(iso));
  assert.equal(joinUTC(day, time).toISOString(), iso);
});

test("joins a day with no time at midnight UTC", () => {
  assert.equal(joinUTC("2026-03-30", "").toISOString(), "2026-03-30T00:00:00.000Z");
});

test("rejects a join with no day", () => {
  assert.equal(joinUTC("", "14:00"), null);
  assert.equal(joinUTC("not-a-day", "14:00"), null);
});

// --- monthMatrix ---

test("lays out a month as six Monday-first weeks", () => {
  const weeks = monthMatrix(2026, 2); // March 2026, months are 0-based
  assert.equal(weeks.length, 6);
  for (const week of weeks) assert.equal(week.length, 7);
});

test("starts the grid on the Monday on or before the first of the month", () => {
  // 1 March 2026 is a Sunday, so the grid opens on 23 February.
  const first = monthMatrix(2026, 2)[0][0];
  assert.deepEqual(first, { day: "2026-02-23", inMonth: false });
});

test("marks the days that belong to the month", () => {
  const days = monthMatrix(2026, 2).flat();
  const inMonth = days.filter(c => c.inMonth).map(c => c.day);
  assert.equal(inMonth.length, 31);
  assert.equal(inMonth[0], "2026-03-01");
  assert.equal(inMonth[30], "2026-03-31");
});

test("runs the grid forward one day at a time across month ends", () => {
  const days = monthMatrix(2026, 2).flat().map(c => c.day);
  for (let i = 1; i < days.length; i++) {
    const prev = Date.parse(days[i - 1] + "T00:00:00Z");
    assert.equal(Date.parse(days[i] + "T00:00:00Z") - prev, 86400000);
  }
});

test("lays out a February that starts on a Monday without a blank first week", () => {
  // 1 February 2027 is a Monday: the grid must open on it, not a week earlier.
  assert.deepEqual(monthMatrix(2027, 1)[0][0], { day: "2027-02-01", inMonth: true });
});

test("covers a leap February", () => {
  const inMonth = monthMatrix(2028, 1).flat().filter(c => c.inMonth);
  assert.equal(inMonth.length, 29);
  assert.equal(inMonth.at(-1).day, "2028-02-29");
});

// --- shiftMonth / monthLabel ---

test("steps a month forward and back across the year boundary", () => {
  assert.deepEqual(shiftMonth({ year: 2026, month: 11 }, 1), { year: 2027, month: 0 });
  assert.deepEqual(shiftMonth({ year: 2026, month: 0 }, -1), { year: 2025, month: 11 });
});

// The year arrows are the month step taken twelve at a time, which has to land
// on the same month of another year rather than drifting.
test("steps a year forward and back without moving the month", () => {
  assert.deepEqual(shiftMonth({ year: 2026, month: 2 }, 12), { year: 2027, month: 2 });
  assert.deepEqual(shiftMonth({ year: 2026, month: 2 }, -12), { year: 2025, month: 2 });
  assert.deepEqual(shiftMonth({ year: 2028, month: 1 }, 12), { year: 2029, month: 1 });
});

test("labels a month for the header", () => {
  assert.equal(monthLabel({ year: 2026, month: 2 }), "March 2026");
});

// The header carries the year on its own row, so the month row names the month
// alone — repeating the year under it would just be noise.
test("names a month without its year", () => {
  assert.equal(monthName({ year: 2026, month: 2 }), "March");
  assert.equal(monthName({ year: 2026, month: 11 }), "December");
});

// --- nextSelection ---

test("first click starts a range and leaves the end open", () => {
  assert.deepEqual(nextSelection({ from: "", to: "" }, "2026-03-10"), {
    from: "2026-03-10",
    to: "",
  });
});

test("second click on a later day closes the range", () => {
  assert.deepEqual(nextSelection({ from: "2026-03-10", to: "" }, "2026-03-20"), {
    from: "2026-03-10",
    to: "2026-03-20",
  });
});

test("clicking the start again makes a single-day range", () => {
  assert.deepEqual(nextSelection({ from: "2026-03-10", to: "" }, "2026-03-10"), {
    from: "2026-03-10",
    to: "2026-03-10",
  });
});

test("clicking before an open start moves the start rather than inverting the range", () => {
  assert.deepEqual(nextSelection({ from: "2026-03-20", to: "" }, "2026-03-10"), {
    from: "2026-03-10",
    to: "",
  });
});

test("clicking with both ends set starts over", () => {
  assert.deepEqual(nextSelection({ from: "2026-03-10", to: "2026-03-20" }, "2026-03-15"), {
    from: "2026-03-15",
    to: "",
  });
});

test("clicking on or before a lone end fills in the start and keeps the end", () => {
  assert.deepEqual(nextSelection({ from: "", to: "2026-03-20" }, "2026-03-10"), {
    from: "2026-03-10",
    to: "2026-03-20",
  });
});

test("clicking after a lone end starts a new range", () => {
  assert.deepEqual(nextSelection({ from: "", to: "2026-03-20" }, "2026-03-25"), {
    from: "2026-03-25",
    to: "",
  });
});

// --- dayInRange ---

test("highlights the days between the ends, inclusive", () => {
  const range = { from: "2026-03-10", to: "2026-03-12" };
  assert.equal(dayInRange("2026-03-09", range), false);
  assert.equal(dayInRange("2026-03-10", range), true);
  assert.equal(dayInRange("2026-03-11", range), true);
  assert.equal(dayInRange("2026-03-12", range), true);
  assert.equal(dayInRange("2026-03-13", range), false);
});

test("highlights nothing while only one end is set", () => {
  assert.equal(dayInRange("2026-03-11", { from: "2026-03-10", to: "" }), false);
  assert.equal(dayInRange("2026-03-11", { from: "", to: "2026-03-12" }), false);
});

// --- defaults ---

test("defaults cover the whole day at each end", () => {
  // An "after 2026-03-10" filter should include the 10th, and a "before
  // 2026-03-12" one should include all of the 12th.
  assert.equal(DEFAULT_START_TIME, "00:00");
  assert.equal(DEFAULT_END_TIME, "23:59");
});

test("todayUTC reads as a grid day key", () => {
  assert.match(todayUTC(), /^\d{4}-\d{2}-\d{2}$/);
  assert.equal(todayUTC(new Date("2026-03-30T23:30:00Z")), "2026-03-30");
});

// --- readRange / writeRange ---

test("reads both filter fields into a selection", () => {
  assert.deepEqual(readRange("2026-03-10 08:30", "2026-03-20 17:45"), {
    from: "2026-03-10",
    to: "2026-03-20",
    fromTime: "08:30",
    toTime: "17:45",
  });
});

test("reads empty fields as an empty selection with default times", () => {
  assert.deepEqual(readRange("", ""), {
    from: "",
    to: "",
    fromTime: DEFAULT_START_TIME,
    toTime: DEFAULT_END_TIME,
  });
});

test("reads an unparseable field as unset rather than guessing at it", () => {
  const range = readRange("whenever", "2026-03-20 17:45");
  assert.equal(range.from, "");
  assert.equal(range.fromTime, DEFAULT_START_TIME);
  assert.equal(range.to, "2026-03-20");
});

test("reads a zoned field as the UTC instant it names", () => {
  // 14:00+02:00 is 12:00 UTC, and the picker edits UTC.
  assert.deepEqual(readRange("2026-03-10T14:00:00+02:00", ""), {
    from: "2026-03-10",
    to: "",
    fromTime: "12:00",
    toTime: DEFAULT_END_TIME,
  });
});

test("writes a selection back as the text the fields hold", () => {
  assert.deepEqual(
    writeRange({ from: "2026-03-10", to: "2026-03-20", fromTime: "08:30", toTime: "17:45" }),
    { from: "2026-03-10 08:30", to: "2026-03-20 17:45" },
  );
});

test("writes an unset end as an empty field, so the limit is gone", () => {
  assert.deepEqual(
    writeRange({ from: "2026-03-10", to: "", fromTime: "08:30", toTime: "17:45" }),
    { from: "2026-03-10 08:30", to: "" },
  );
});

test("writes default times for a day picked without one", () => {
  assert.deepEqual(
    writeRange({ from: "2026-03-10", to: "2026-03-20", fromTime: "", toTime: "" }),
    { from: "2026-03-10 00:00", to: "2026-03-20 23:59" },
  );
});

test("round-trips a selection through the fields", () => {
  const text = writeRange({ from: "2026-03-10", to: "2026-03-20", fromTime: "08:30", toTime: "17:45" });
  assert.deepEqual(readRange(text.from, text.to), {
    from: "2026-03-10",
    to: "2026-03-20",
    fromTime: "08:30",
    toTime: "17:45",
  });
});

// --- visibleMonth ---

test("opens on the month of the start of the range", () => {
  assert.deepEqual(
    visibleMonth({ from: "2026-03-10", to: "2026-05-20" }),
    { year: 2026, month: 2 },
  );
});

test("opens on the month of a lone end", () => {
  assert.deepEqual(visibleMonth({ from: "", to: "2026-05-20" }), { year: 2026, month: 4 });
});

test("opens on the current month when nothing is selected", () => {
  assert.deepEqual(
    visibleMonth({ from: "", to: "" }, new Date("2026-08-09T12:00:00Z")),
    { year: 2026, month: 7 },
  );
});
