// Where a test was run.
//
// A manual result is made at a place — a proving ground, a vehicle, one of four
// benches that are not interchangeable — and the place is part of what the
// record proves. One field takes it, because a tester at a bench types "Lab 2,
// bay 4" and a tester in a field has coordinates, and making them choose a
// format first is how the field ends up empty.
//
// So the stored value is the text as written. What lives here is the reading of
// it: recognising a coordinate pair when there is one, so the record can offer a
// map, and turning a device's fix into text in the first place. Nothing here
// touches the DOM, and the geolocation source is a parameter, so both are
// testable without a browser.

// A decimal pair, separated by a comma or by space, each half optionally
// carrying a degree sign and a hemisphere letter.
const COORD_PAIR =
  /^([+-]?\d{1,3}(?:\.\d+)?)\s*°?\s*([NS])?(?:\s*[,;]\s*|\s+)([+-]?\d{1,3}(?:\.\d+)?)\s*°?\s*([EW])?$/i;

// parseCoordinates reads a latitude/longitude pair, or returns null for
// anything else — which includes every address, since the field takes those
// too. Null is the ordinary case, not an error: it means the text is a place
// name and stays exactly as the tester wrote it.
export function parseCoordinates(text) {
  if (typeof text !== "string") return null;

  const m = COORD_PAIR.exec(text.trim());
  if (!m) return null;

  const [, rawLat, ns, rawLon, ew] = m;

  // A hemisphere letter and a sign both say which side of the line the point is
  // on. When a value carries each, one of them is redundant and the other may
  // be a typo, and there is no way to tell which — so this is not a pair.
  if ((ns && /^[+-]/.test(rawLat)) || (ew && /^[+-]/.test(rawLon))) return null;

  let lat = Number(rawLat);
  let lon = Number(rawLon);
  if (ns && ns.toUpperCase() === "S") lat = -lat;
  if (ew && ew.toUpperCase() === "W") lon = -lon;

  if (Math.abs(lat) > 90 || Math.abs(lon) > 180) return null;
  return { lat, lon };
}

// formatCoordinates writes a fix as text for the field.
//
// Five decimals is a little over a metre — better than any phone knows where it
// is, and short enough that two readings of the same spot look like one place
// rather than two.
export function formatCoordinates(lat, lon) {
  return `${lat.toFixed(5)}, ${lon.toFixed(5)}`;
}

// formatAccuracy says how much of a place a fix actually names. A reading good
// to three kilometres is not evidence that the test happened at the bench, and
// the tester is the only one who can see that in time to correct it.
export function formatAccuracy(meters) {
  if (typeof meters !== "number" || !Number.isFinite(meters)) return "";
  if (meters < 1000) return `±${Math.round(meters)} m`;
  return `±${(meters / 1000).toFixed(1)} km`;
}

// mapURL points at coordinates on a map. It is a link and never a fetch: a
// record renders without asking anyone else where its point is, and the reader
// decides whether to tell a map service what they are reading.
export function mapURL({ lat, lon }) {
  return `https://www.openstreetmap.org/?mlat=${lat}&mlon=${lon}#map=16/${lat}/${lon}`;
}

// requestPosition asks the device where it is. The geolocation source is a
// parameter so this can be driven by a fake; in the browser it defaults to the
// real one.
export function requestPosition(geolocation = globalThis.navigator?.geolocation) {
  if (!geolocation) {
    return Promise.reject(
      new Error("This browser cannot report a position — type the location instead."),
    );
  }

  return new Promise((resolve, reject) => {
    geolocation.getCurrentPosition(
      pos => resolve({
        lat: pos.coords.latitude,
        lon: pos.coords.longitude,
        accuracy: pos.coords.accuracy,
      }),
      err => reject(new Error(positionErrorMessage(err))),
      // High accuracy because a fix to the nearest cell tower does not
      // distinguish two benches in one building; a timeout because indoors the
      // request can otherwise never come back at all.
      { enableHighAccuracy: true, timeout: 15000, maximumAge: 0 },
    );
  });
}

// positionErrorMessage turns a GeolocationPositionError into something that
// says what to do next. Every one of them is recoverable by typing the place in,
// so none of them is phrased as a failure of the record.
export function positionErrorMessage(err) {
  switch (err && err.code) {
    case 1:
      return "Location permission was denied — allow it in the browser, or type the location in.";
    case 2:
      return "Position is unavailable here — type the location in instead.";
    case 3:
      return "Locating timed out — try again outdoors, or type the location in.";
    default:
      return `Could not get a position: ${(err && err.message) || "unknown error"}`;
  }
}
