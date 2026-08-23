package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/weather"
)

// The provider is a fake here. What this handler does is read a request, decide
// which of three answers a failure deserves, and hand back a line for the
// field — none of which needs a weather service, and all of which would be
// untestable against one.
type fakeProvider struct {
	reading weather.Reading
	err     error

	lat, lon float64
	when     time.Time
	calls    int
}

func (f *fakeProvider) At(_ context.Context, lat, lon float64, when time.Time) (weather.Reading, error) {
	f.calls++
	f.lat, f.lon, f.when = lat, lon, when
	return f.reading, f.err
}

func num(v float64) *float64 { return &v }

func weatherRequest(t *testing.T, provider weather.Provider, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/weather"+query, nil)
	NewWeatherHandler(provider).Get(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestWeatherAnswersWithTheLineForTheField(t *testing.T) {
	observed := time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC)
	provider := &fakeProvider{reading: weather.Reading{
		ObservedAt:   observed,
		Description:  "Partly cloudy",
		TemperatureC: num(18.7),
	}}

	rec := weatherRequest(t, provider, "?lat=52.51631&lon=13.37771&at=2026-08-23T13:20:00Z")
	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeBody[struct {
		Summary      string    `json:"summary"`
		Description  string    `json:"description"`
		ObservedAt   time.Time `json:"observed_at"`
		TemperatureC *float64  `json:"temperature_c"`
	}](t, rec)

	assert.Equal(t, "Partly cloudy, 18.7 °C", body.Summary)
	assert.Equal(t, "Partly cloudy", body.Description)
	// The hour the reading is for, not the minute the test finished: a tester
	// deciding whether to keep the line needs to see which one they are being
	// offered.
	assert.True(t, observed.Equal(body.ObservedAt))
	require.NotNil(t, body.TemperatureC)
	assert.InDelta(t, 18.7, *body.TemperatureC, 0.001)
}

// The point and the time are passed through untouched. Coarsening the point is
// the provider's business, and doing it twice would put the reading somewhere
// neither layer chose.
func TestWeatherPassesThePointAndTimeThrough(t *testing.T) {
	provider := &fakeProvider{reading: weather.Reading{Description: "Clear sky"}}

	rec := weatherRequest(t, provider, "?lat=-33.86880&lon=151.20930&at=2026-08-23T13:20:00Z")
	require.Equal(t, http.StatusOK, rec.Code)

	assert.InDelta(t, -33.8688, provider.lat, 0.00001)
	assert.InDelta(t, 151.2093, provider.lon, 0.00001)
	assert.True(t, time.Date(2026, 8, 23, 13, 20, 0, 0, time.UTC).Equal(provider.when))
}

// A tester filing yesterday evening's run wants yesterday evening's weather.
// Only when no time is given at all is now the right answer.
func TestWeatherDefaultsToNow(t *testing.T) {
	provider := &fakeProvider{reading: weather.Reading{Description: "Clear sky"}}

	rec := weatherRequest(t, provider, "?lat=52.5&lon=13.4")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.WithinDuration(t, time.Now(), provider.when, time.Minute)
}

func TestWeatherRejectsAPointItCannotRead(t *testing.T) {
	for _, query := range []string{
		"?lon=13.4",           // no latitude
		"?lat=52.5",           // no longitude
		"?lat=&lon=13.4",      // empty is not zero
		"?lat=north&lon=13.4", // not a number
		"?lat=91&lon=13.4",    // off the planet
		"?lat=52.5&lon=181",   // ditto
	} {
		provider := &fakeProvider{}
		rec := weatherRequest(t, provider, query)
		assert.Equal(t, http.StatusBadRequest, rec.Code, query)
		// Nothing is asked of the service for a request that names no place.
		assert.Zero(t, provider.calls, query)
	}
}

func TestWeatherRejectsATimeItCannotRead(t *testing.T) {
	provider := &fakeProvider{}
	rec := weatherRequest(t, provider, "?lat=52.5&lon=13.4&at=yesterday%20evening")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Zero(t, provider.calls)
}

// No reading for that place and hour is an answer, not a fault. 404 says so,
// and the service's own account of why comes with it — "out of allowed range
// from … to …" tells the tester what to do next in a way "unavailable" cannot.
func TestWeatherHasNothingForThatHour(t *testing.T) {
	provider := &fakeProvider{err: errors.New(
		"no weather reading for that place and time: " +
			"Parameter 'start_hour' is out of allowed range from 2026-05-22 to 2026-09-07")}
	provider.err = errors.Join(weather.ErrNoReading, provider.err)

	rec := weatherRequest(t, provider, "?lat=52.5&lon=13.4&at=2027-08-23T13:00:00Z")
	require.Equal(t, http.StatusNotFound, rec.Code)

	body := decodeBody[struct {
		Error string `json:"error"`
	}](t, rec)
	assert.Contains(t, body.Error, "out of allowed range")
}

// The upstream being down is the operator's problem. The tester gets a field
// they can still type into, and a status that does not blame their request.
func TestWeatherUpstreamFailure(t *testing.T) {
	provider := &fakeProvider{err: errors.New("weather service unreachable: connection refused")}

	rec := weatherRequest(t, provider, "?lat=52.5&lon=13.4")
	require.Equal(t, http.StatusBadGateway, rec.Code)

	body := decodeBody[struct {
		Error string `json:"error"`
	}](t, rec)
	// Whatever the upstream said about itself stays in the server log: it is
	// the operator's to read, and a tester can do nothing with a socket error.
	assert.NotContains(t, body.Error, "connection refused")
	assert.Contains(t, body.Error, "type the conditions in")
}

// A deployment with no way out to the internet configures the lookup away. The
// route still answers, because a form that silently does nothing when a button
// is pressed is worse than one that says the button is off here.
func TestWeatherDisabled(t *testing.T) {
	rec := weatherRequest(t, nil, "?lat=52.5&lon=13.4")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	body := decodeBody[struct {
		Error string `json:"error"`
	}](t, rec)
	assert.Contains(t, body.Error, "disabled")
}
