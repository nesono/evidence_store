// Unit tests for web/static/location.js, run with `node --test`.
//
// Reading a coordinate pair a human typed is the part worth testing: the field
// accepts an address too, so everything that is not a pair has to fall through
// untouched rather than be half-parsed into a point on the equator.

import test from "node:test";
import assert from "node:assert/strict";

import {
  formatAccuracy,
  formatCoordinates,
  mapURL,
  parseCoordinates,
  positionErrorMessage,
  requestPosition,
} from "../static/location.js";

// --- parseCoordinates ---

test("parses a comma-separated decimal pair", () => {
  assert.deepEqual(parseCoordinates("52.5163, 13.3777"), { lat: 52.5163, lon: 13.3777 });
});

test("parses a space-separated pair", () => {
  assert.deepEqual(parseCoordinates("52.5163 13.3777"), { lat: 52.5163, lon: 13.3777 });
});

test("parses negative coordinates", () => {
  assert.deepEqual(parseCoordinates("-33.8688,151.2093"), { lat: -33.8688, lon: 151.2093 });
});

test("parses whole degrees", () => {
  assert.deepEqual(parseCoordinates("52, 13"), { lat: 52, lon: 13 });
});

test("parses hemisphere letters", () => {
  assert.deepEqual(parseCoordinates("33.8688 S, 151.2093 E"), { lat: -33.8688, lon: 151.2093 });
  assert.deepEqual(parseCoordinates("52.5163N 13.3777W"), { lat: 52.5163, lon: -13.3777 });
});

test("parses degree signs", () => {
  assert.deepEqual(parseCoordinates("52.5163° N, 13.3777° E"), { lat: 52.5163, lon: 13.3777 });
});

// A hemisphere letter and a minus sign say the same thing twice, and when they
// disagree there is no reading that is safely the tester's. Falling back to the
// text keeps the record honest about what was written.
test("rejects a sign that contradicts its hemisphere", () => {
  assert.equal(parseCoordinates("-52.5163N, 13.3777E"), null);
});

test("rejects out-of-range values", () => {
  assert.equal(parseCoordinates("91.0, 13.0"), null);
  assert.equal(parseCoordinates("52.0, 181.0"), null);
});

// The same field takes an address, and an address is not a failed coordinate
// pair — it is the other thing the field is for.
test("returns null for an address", () => {
  assert.equal(parseCoordinates("Test track, Munich"), null);
  assert.equal(parseCoordinates("Bay 4, Lab 2"), null);
  assert.equal(parseCoordinates(""), null);
});

test("returns null for a lone number", () => {
  assert.equal(parseCoordinates("52.5163"), null);
});

test("returns null for a third value", () => {
  assert.equal(parseCoordinates("52.5163, 13.3777, 34"), null);
});

test("ignores surrounding whitespace", () => {
  assert.deepEqual(parseCoordinates("  52.5163 , 13.3777  "), { lat: 52.5163, lon: 13.3777 });
});

test("returns null for anything that is not a string", () => {
  assert.equal(parseCoordinates(null), null);
  assert.equal(parseCoordinates(undefined), null);
  assert.equal(parseCoordinates({ lat: 52, lon: 13 }), null);
});

// --- formatCoordinates ---

// Five decimals is about a metre. More is noise no receiver in a phone earns,
// and it makes two readings of the same spot look like two places.
test("formats to five decimals", () => {
  assert.equal(formatCoordinates(52.516312345, 13.377712345), "52.51631, 13.37771");
});

test("keeps trailing zeros so the precision is visible", () => {
  assert.equal(formatCoordinates(52.5, -13), "52.50000, -13.00000");
});

test("what it formats, it can read back", () => {
  const text = formatCoordinates(-33.8688, 151.2093);
  assert.deepEqual(parseCoordinates(text), { lat: -33.8688, lon: 151.2093 });
});

// --- formatAccuracy ---

test("formats metres and kilometres", () => {
  assert.equal(formatAccuracy(12.4), "±12 m");
  assert.equal(formatAccuracy(950), "±950 m");
  assert.equal(formatAccuracy(3200), "±3.2 km");
});

test("formats nothing for an unknown accuracy", () => {
  assert.equal(formatAccuracy(null), "");
  assert.equal(formatAccuracy(undefined), "");
  assert.equal(formatAccuracy(NaN), "");
});

// --- mapURL ---

test("links coordinates to a map", () => {
  const url = mapURL({ lat: 52.5163, lon: 13.3777 });
  assert.match(url, /^https:\/\/www\.openstreetmap\.org\//);
  assert.match(url, /mlat=52\.5163/);
  assert.match(url, /mlon=13\.3777/);
});

// --- requestPosition ---

function fakeGeolocation(impl) {
  return { getCurrentPosition: impl };
}

test("resolves a fix to plain numbers", async () => {
  const geo = fakeGeolocation((ok) => {
    ok({ coords: { latitude: 52.5163, longitude: 13.3777, accuracy: 12.5 } });
  });
  assert.deepEqual(await requestPosition(geo), { lat: 52.5163, lon: 13.3777, accuracy: 12.5 });
});

test("asks for the best fix the device can give", async () => {
  let options;
  const geo = fakeGeolocation((ok, _fail, opts) => {
    options = opts;
    ok({ coords: { latitude: 0, longitude: 0, accuracy: 1 } });
  });
  await requestPosition(geo);
  assert.equal(options.enableHighAccuracy, true);
  // A device that never gets a fix must not leave the button spinning forever.
  assert.ok(options.timeout > 0);
});

test("rejects with a message a tester can act on", async () => {
  const geo = fakeGeolocation((_ok, fail) => fail({ code: 1, message: "User denied Geolocation" }));
  await assert.rejects(() => requestPosition(geo), /permission/i);
});

// Without geolocation there is still a text field, so this is a hint and not a
// failure.
test("rejects when the browser cannot locate at all", async () => {
  await assert.rejects(() => requestPosition(undefined), /type/i);
});

// --- positionErrorMessage ---

test("names each failure by what the tester should do about it", () => {
  assert.match(positionErrorMessage({ code: 1 }), /permission/i);
  assert.match(positionErrorMessage({ code: 2 }), /unavailable/i);
  assert.match(positionErrorMessage({ code: 3 }), /timed out|took too long/i);
  assert.ok(positionErrorMessage({ code: 99, message: "boom" }).length > 0);
});
