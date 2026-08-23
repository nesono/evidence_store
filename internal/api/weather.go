package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nesono/evidence-store/internal/weather"
)

// WeatherHandler answers what the weather was at a place and an hour, so a
// tester filling in a manual result does not have to write it from memory.
//
// It is a lookup and not a store: nothing is written here, and the answer is a
// suggestion for a field the tester can overwrite. The record that comes back
// later carries the text they accepted, not this.
//
// The request goes out from the server rather than from the page because the
// browser asking a weather service directly would hand a tester's position to a
// third party on every form fill, and would leave an operator with no way to
// see or sever that. Here there is one host to allow through a firewall, one
// place to point at an internal service, and one setting that turns it off.
type WeatherHandler struct {
	provider weather.Provider
}

func NewWeatherHandler(provider weather.Provider) *WeatherHandler {
	return &WeatherHandler{provider: provider}
}

type weatherResponse struct {
	weather.Reading
	// Summary is the line that goes in the field. It is composed here so that
	// what a tester sees before submitting and what any other client would write
	// into `weather_conditions` are the same sentence.
	Summary string `json:"summary"`
}

// Get reads the weather for one point at one time.
//
// `at` is the moment the test finished, which is not always now: a tester
// filing yesterday evening's run wants yesterday evening's weather, and taking
// the current hour instead would file a plausible-looking untruth.
func (h *WeatherHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.provider == nil {
		writeError(w, http.StatusServiceUnavailable,
			"weather lookups are disabled on this server — type the conditions in instead")
		return
	}

	query := r.URL.Query()

	lat, err := coordinate(query.Get("lat"), 90)
	if err != nil {
		writeError(w, http.StatusBadRequest, "lat: "+err.Error())
		return
	}
	lon, err := coordinate(query.Get("lon"), 180)
	if err != nil {
		writeError(w, http.StatusBadRequest, "lon: "+err.Error())
		return
	}

	when := time.Now().UTC()
	if raw := query.Get("at"); raw != "" {
		when, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "at: expected an RFC 3339 timestamp")
			return
		}
	}

	reading, err := h.provider.At(r.Context(), lat, lon, when)
	if err != nil {
		// Having no reading for a place and an hour is an answer, not a fault:
		// 404 says the tester should write the conditions in themselves, and
		// carries the service's own account of why there is nothing.
		if errors.Is(err, weather.ErrNoReading) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// The upstream being down is the operator's problem and belongs in the
		// log with the detail; the tester gets a field they can still type in.
		slog.Error("weather lookup failed", "error", err, "at", when)
		writeError(w, http.StatusBadGateway,
			"could not reach the weather service — type the conditions in instead")
		return
	}

	writeJSON(w, http.StatusOK, weatherResponse{Reading: reading, Summary: reading.Summary()})
}

// coordinate reads one half of a point. Both halves are required: defaulting a
// missing one to zero would answer confidently for a spot in the Atlantic.
func coordinate(raw string, limit float64) (float64, error) {
	if raw == "" {
		return 0, errors.New("required")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, errors.New("expected a decimal degree")
	}
	if value < -limit || value > limit {
		return 0, errors.New("out of range")
	}
	return value, nil
}
