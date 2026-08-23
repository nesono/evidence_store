// Unit tests for web/static/weather.js, run with `node --test`.
//
// Two things are worth testing without a browser: which point a lookup is made
// about, and what a tester is told when there is no reading to be had. Both are
// decisions, and both are wrong in ways that would file a record about the
// wrong place or leave a field silently empty.

import test from "node:test";
import assert from "node:assert/strict";

import { describeReading, fetchWeather, weatherPoint } from "../static/weather.js";

// --- weatherPoint ---

function fakePosition(fix) {
  return async () => {
    if (fix instanceof Error) throw fix;
    return fix;
  };
}

// The location field is what the record is going to claim, so when it holds a
// pair it wins — including when the tester is filing a run made somewhere they
// are no longer standing.
test("asks about the point in the location field", async () => {
  const locate = fakePosition({ lat: 0, lon: 0 });
  const point = await weatherPoint("52.51631, 13.37771", locate);

  assert.equal(point.lat, 52.51631);
  assert.equal(point.lon, 13.37771);
  assert.equal(point.source, "location");
});

// A place name cannot be turned into a point here without asking a geocoding
// service what the tester meant, which is a guess sent to a third party. The
// device is nearer and honest about being an approximation.
test("falls back to the device for a place name", async () => {
  const locate = fakePosition({ lat: 48.137, lon: 11.575, accuracy: 12 });
  const point = await weatherPoint("Test track, bay 4", locate);

  assert.equal(point.lat, 48.137);
  assert.equal(point.source, "device");
});

test("falls back to the device for an empty location", async () => {
  const locate = fakePosition({ lat: 48.137, lon: 11.575 });
  assert.equal((await weatherPoint("", locate)).source, "device");
});

// Without a point there is no lookup, and the reason the device gave is what
// tells the tester whether to allow permission or type the weather in.
test("passes on why the device could not say where it is", async () => {
  const locate = fakePosition(new Error("Location permission was denied"));
  await assert.rejects(() => weatherPoint("", locate), /permission/i);
});

// --- fetchWeather ---

function fakeFetch(status, body) {
  const calls = [];
  const impl = async (url) => {
    calls.push(url);
    return { ok: status >= 200 && status < 300, status, json: async () => body };
  };
  return { impl, calls };
}

const reading = {
  observed_at: "2026-08-23T13:00:00Z",
  description: "Partly cloudy",
  summary: "Partly cloudy, 18.7 °C, wind 15 km/h, humidity 52%, no precipitation",
};

test("returns the reading the store answered with", async () => {
  const { impl } = fakeFetch(200, reading);
  const got = await fetchWeather({ lat: 52.5, lon: 13.4 }, new Date("2026-08-23T13:20:00Z"), impl);

  assert.equal(got.summary, reading.summary);
  assert.equal(got.observed_at, "2026-08-23T13:00:00Z");
});

test("asks about the point and the moment the test finished", async () => {
  const { impl, calls } = fakeFetch(200, reading);
  await fetchWeather({ lat: 52.5, lon: 13.4 }, new Date("2026-08-23T13:20:00Z"), impl);

  const url = new URL(calls[0], "http://example.test");
  assert.equal(url.pathname, "/api/v1/weather");
  assert.equal(url.searchParams.get("lat"), "52.5");
  assert.equal(url.searchParams.get("lon"), "13.4");
  assert.equal(url.searchParams.get("at"), "2026-08-23T13:20:00.000Z");
});

// A record with no finish time yet is a record about now, and saying so is the
// server's job — not this one's, which would have to guess a timezone.
test("leaves the time out when the record does not say one", async () => {
  const { impl, calls } = fakeFetch(200, reading);
  await fetchWeather({ lat: 52.5, lon: 13.4 }, null, impl);

  const url = new URL(calls[0], "http://example.test");
  assert.equal(url.searchParams.get("at"), null);
});

// The server has already decided which failures a tester can act on and phrased
// them so. Rewording them here would only let the two drift apart.
test("passes the store's own explanation through", async () => {
  const { impl } = fakeFetch(404, {
    error: "no weather reading: Parameter 'start_hour' is out of allowed range from 2026-05-22 to 2026-09-07",
  });
  await assert.rejects(
    () => fetchWeather({ lat: 52.5, lon: 13.4 }, new Date(), impl),
    /out of allowed range/,
  );
});

test("says something even when the store explains nothing", async () => {
  const { impl } = fakeFetch(502, {});
  await assert.rejects(() => fetchWeather({ lat: 52.5, lon: 13.4 }, new Date(), impl), /502/);
});

// A 200 with no line in it would otherwise blank the field, which reads as a
// tester who looked at the sky and saw nothing.
test("treats an empty answer as no reading", async () => {
  const { impl } = fakeFetch(200, { observed_at: "2026-08-23T13:00:00Z" });
  await assert.rejects(
    () => fetchWeather({ lat: 52.5, lon: 13.4 }, new Date(), impl),
    /nothing for that place/,
  );
});

test("reports a network failure as one", async () => {
  const impl = async () => { throw new Error("Failed to fetch"); };
  await assert.rejects(
    () => fetchWeather({ lat: 52.5, lon: 13.4 }, new Date(), impl),
    /Could not reach the weather service/,
  );
});

// --- describeReading ---

// Hourly is the resolution weather models publish at, so the reading is never
// the minute of the test. A tester deciding whether to keep the line has to be
// able to see which hour they are being offered.
test("names the hour the reading is for", () => {
  assert.match(describeReading(reading, "location"), /2026-08-23 13:00 UTC/);
});

// A reading taken at the device is about where the tester is standing now,
// which is not always where the record says the test was run.
test("says when the point came from the device", () => {
  assert.match(describeReading(reading, "device"), /this device's position/);
  assert.match(describeReading(reading, "location"), /the location/);
});

test("still says something for a reading with no hour", () => {
  assert.ok(describeReading({ summary: "Fog" }, "location").length > 0);
});
