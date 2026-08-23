// What the weather was doing while a manual test ran.
//
// Braking distance on a wet surface is a different measurement from braking
// distance on a dry one, and a record that does not say which cannot be
// compared with the next one. The field has always been there — DESIGN.md gives
// manual evidence `weather_conditions` — but filling it meant remembering the
// sky hours later, so it stayed empty.
//
// The reading is asked for from this store's own API rather than from a weather
// service directly: the page must not hand a tester's position to a third party
// on every form fill, and an operator needs one place to point elsewhere or
// switch off. What lives here is deciding which point to ask about and reading
// the answer; nothing here touches the DOM, and fetch is a parameter, so both
// are testable without a browser.

import { API_BASE, apiFetch } from "./common.js";
import { parseCoordinates } from "./location.js";

// weatherPoint decides where to ask about.
//
// The location field is preferred over the device whenever it holds a pair,
// because the tester may be filing a run made somewhere they are not standing
// now — and because it is the place the record is going to claim. A place name
// is not a point, so those fall through to the device: "Lab 2, bay 4" cannot be
// resolved here without asking a geocoding service what the tester meant, which
// is a guess sent to a third party, and the device is both nearer and honest
// about being an approximation.
export async function weatherPoint(locationText, requestPosition) {
  const written = parseCoordinates(locationText);
  if (written) return { ...written, source: "location" };

  const fix = await requestPosition();
  return { lat: fix.lat, lon: fix.lon, source: "device" };
}

// fetchWeather asks this store what the weather was at a point in an hour.
//
// `when` is the moment the record says the test finished, not now: a tester
// filing yesterday evening's run wants yesterday evening's weather, and today's
// would be a plausible-looking untruth.
export async function fetchWeather({ lat, lon }, when, fetchImpl = apiFetch) {
  const query = new URLSearchParams({ lat: String(lat), lon: String(lon) });
  if (when) query.set("at", when.toISOString());

  let resp;
  try {
    resp = await fetchImpl(`${API_BASE}/weather?${query}`);
  } catch (err) {
    throw new Error(`Could not reach the weather service: ${err.message}`);
  }

  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    // The server has already decided which failures are the tester's to act on
    // and which are the operator's, and phrased them accordingly. Repeating
    // that judgement here would only let the two drift apart.
    throw new Error(data.error || `Weather lookup failed (HTTP ${resp.status})`);
  }
  if (!data.summary) {
    throw new Error("The weather service had nothing for that place and time.");
  }
  return data;
}

// describeReading says which hour the line in the field is for, and where the
// point came from.
//
// Both matter to someone deciding whether to keep it: hourly is the resolution
// weather models publish at, so a reading is never the minute of the test, and
// a reading taken at the device is about where the tester is standing now
// rather than where the record says the test was run.
export function describeReading(reading, source) {
  const hour = formatHour(reading.observed_at);
  const place = source === "device" ? "this device's position" : "the location";
  return hour ? `Reading for ${place}, ${hour} UTC` : `Reading for ${place}`;
}

function formatHour(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = n => String(n).padStart(2, "0");
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ` +
    `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
}
