package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The upstream is a fake in every test here. A suite that asked the real
// service what the sky was doing would fail when it rained, and would tell a
// third party the coordinates of every machine that runs the tests.
func fakeUpstream(t *testing.T, body string, status int) (*OpenMeteo, *url.Values) {
	t.Helper()
	var query url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return NewOpenMeteo(srv.URL, 5*time.Second), &query
}

const oneHour = `{
  "hourly": {
    "time": ["2026-08-23T13:00", "2026-08-23T14:00"],
    "temperature_2m": [18.7, 17.7],
    "relative_humidity_2m": [52, 56],
    "precipitation": [0.0, 0.3],
    "weather_code": [2, 80],
    "wind_speed_10m": [14.8, 17.7]
  }
}`

func at(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return parsed
}

func TestReadsTheHourTheTestRanIn(t *testing.T) {
	provider, _ := fakeUpstream(t, oneHour, http.StatusOK)

	got, err := provider.At(context.Background(), 52.51631, 13.37771, at(t, "2026-08-23T13:20:00Z"))
	require.NoError(t, err)

	assert.Equal(t, at(t, "2026-08-23T13:00:00Z"), got.ObservedAt)
	assert.Equal(t, "Partly cloudy", got.Description)
	require.NotNil(t, got.TemperatureC)
	assert.InDelta(t, 18.7, *got.TemperatureC, 0.001)
	require.NotNil(t, got.WindSpeedKPH)
	assert.InDelta(t, 14.8, *got.WindSpeedKPH, 0.001)
	require.NotNil(t, got.RelativeHumidity)
	assert.InDelta(t, 52, *got.RelativeHumidity, 0.001)
	require.NotNil(t, got.PrecipitationMM)
	assert.InDelta(t, 0, *got.PrecipitationMM, 0.001)
}

// Half past the hour is nearer the next reading than the one just gone, and
// which one is nearer is the only thing that makes one of them right.
func TestTakesTheNearestHour(t *testing.T) {
	provider, _ := fakeUpstream(t, oneHour, http.StatusOK)

	got, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:50:00Z"))
	require.NoError(t, err)

	assert.Equal(t, at(t, "2026-08-23T14:00:00Z"), got.ObservedAt)
	assert.Equal(t, "Slight rain showers", got.Description)
}

// An hour with no numbers in it is not a reading. Falling back to the hour that
// does have them beats reporting a record with nothing in it.
func TestFallsBackToTheHourThatHasAReading(t *testing.T) {
	provider, _ := fakeUpstream(t, `{
      "hourly": {
        "time": ["2026-08-23T13:00", "2026-08-23T14:00"],
        "temperature_2m": [null, 17.7],
        "relative_humidity_2m": [null, 56],
        "precipitation": [null, 0.3],
        "weather_code": [null, 3],
        "wind_speed_10m": [null, 17.7]
      }
    }`, http.StatusOK)

	got, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:05:00Z"))
	require.NoError(t, err)

	assert.Equal(t, at(t, "2026-08-23T14:00:00Z"), got.ObservedAt)
	assert.Equal(t, "Overcast", got.Description)
}

// Every variable missing means the service has no reading for that hour, which
// is a different thing from the service being down: nothing here will fix it,
// and the tester should be told to write the weather themselves.
func TestNoReadingAtAll(t *testing.T) {
	provider, _ := fakeUpstream(t, `{
      "hourly": {
        "time": ["2026-08-23T13:00"],
        "temperature_2m": [null],
        "weather_code": [null]
      }
    }`, http.StatusOK)

	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:05:00Z"))
	assert.ErrorIs(t, err, ErrNoReading)
}

func TestEmptyResponseIsNoReading(t *testing.T) {
	provider, _ := fakeUpstream(t, `{"hourly": {"time": []}}`, http.StatusOK)

	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:05:00Z"))
	assert.ErrorIs(t, err, ErrNoReading)
}

// The window a service will answer for is its business and it moves, so the
// range is not checked here — but what it says about it is worth passing on,
// because "out of allowed range from … to …" tells the tester what to do next.
func TestOutOfRangeCarriesTheReasonBack(t *testing.T) {
	provider, _ := fakeUpstream(t,
		`{"error": true, "reason": "Parameter 'start_hour' is out of allowed range from 2026-05-22 to 2026-09-07"}`,
		http.StatusBadRequest)

	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2027-08-23T13:00:00Z"))
	require.ErrorIs(t, err, ErrNoReading)
	assert.Contains(t, err.Error(), "out of allowed range")
}

func TestUpstreamFailureIsNotNoReading(t *testing.T) {
	provider, _ := fakeUpstream(t, `{"error": true, "reason": "boom"}`, http.StatusInternalServerError)

	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:00:00Z"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoReading)
}

func TestGarbageResponseIsAnError(t *testing.T) {
	provider, _ := fakeUpstream(t, `<html>upstream is a captive portal</html>`, http.StatusOK)

	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:00:00Z"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoReading)
}

func TestUnreachableUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := srv.URL
	srv.Close()

	provider := NewOpenMeteo(endpoint, time.Second)
	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:00:00Z"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoReading)
}

func TestCancelledRequest(t *testing.T) {
	provider, _ := fakeUpstream(t, oneHour, http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.At(ctx, 52.5, 13.4, at(t, "2026-08-23T13:00:00Z"))
	assert.Error(t, err)
}

// --- What is asked of the service ---

func TestAsksForTheHoursAroundTheTest(t *testing.T) {
	provider, query := fakeUpstream(t, oneHour, http.StatusOK)

	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:20:00Z"))
	require.NoError(t, err)

	assert.Equal(t, "2026-08-23T13:00", query.Get("start_hour"))
	assert.Equal(t, "2026-08-23T14:00", query.Get("end_hour"))
	// Asking in UTC and reading the answer as UTC is what makes the reading's
	// hour comparable with the record's finished_at.
	assert.Equal(t, "UTC", query.Get("timezone"))
	assert.Equal(t, "kmh", query.Get("wind_speed_unit"))
	assert.Contains(t, query.Get("hourly"), "temperature_2m")
	assert.Contains(t, query.Get("hourly"), "weather_code")
}

// A test's coordinates say which bench it ran at; the weather does not vary
// between benches, and a service that is told the point to a metre keeps it. A
// kilometre is under the resolution of any weather model and is all that is
// sent.
func TestCoarsensTheCoordinatesItSendsOn(t *testing.T) {
	provider, query := fakeUpstream(t, oneHour, http.StatusOK)

	_, err := provider.At(context.Background(), 52.516312, 13.377712, at(t, "2026-08-23T13:00:00Z"))
	require.NoError(t, err)

	assert.Equal(t, "52.52", query.Get("latitude"))
	assert.Equal(t, "13.38", query.Get("longitude"))
}

func TestKeepsWhateverPathTheEndpointHas(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(oneHour))
	}))
	t.Cleanup(srv.Close)

	provider := NewOpenMeteo(srv.URL+"/v1/forecast", 5*time.Second)
	_, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:00:00Z"))
	require.NoError(t, err)
	assert.Equal(t, "/v1/forecast", path)
}

// --- Describe ---

func TestDescribesTheCodesAServiceSends(t *testing.T) {
	assert.Equal(t, "Clear sky", Describe(0))
	assert.Equal(t, "Fog", Describe(45))
	assert.Equal(t, "Moderate rain", Describe(63))
	assert.Equal(t, "Heavy snow", Describe(75))
	assert.Equal(t, "Thunderstorm with heavy hail", Describe(99))
}

// A code nothing here knows is still a code the service sent, and dropping it
// would leave the record saying less than the service did.
func TestDescribesAnUnknownCodeAsItself(t *testing.T) {
	assert.Equal(t, "weather code 42", Describe(42))
}

// --- Summary ---

func num(v float64) *float64 { return &v }

func TestSummaryReadsAsOneLine(t *testing.T) {
	reading := Reading{
		Description:      "Partly cloudy",
		TemperatureC:     num(18.7),
		WindSpeedKPH:     num(14.8),
		RelativeHumidity: num(52),
		PrecipitationMM:  num(0.3),
	}
	assert.Equal(t,
		"Partly cloudy, 18.7 °C, wind 15 km/h, humidity 52%, precipitation 0.3 mm",
		reading.Summary())
}

// A dry surface is a fact about a braking test, not the absence of one, so a
// reading of zero says so rather than going quiet.
func TestSummarySaysWhenItWasDry(t *testing.T) {
	reading := Reading{Description: "Clear sky", TemperatureC: num(24), PrecipitationMM: num(0)}
	assert.Equal(t, "Clear sky, 24 °C, no precipitation", reading.Summary())
}

// A service with the sky but not the wind should still fill the field with what
// it does have; a zero standing in for a missing wind speed would read as a
// still day the service never reported.
func TestSummaryLeavesOutWhatWasNotReported(t *testing.T) {
	reading := Reading{Description: "Fog", TemperatureC: num(3.5)}
	assert.Equal(t, "Fog, 3.5 °C", reading.Summary())

	assert.Equal(t, "", Reading{}.Summary())
}

// Whole numbers come back whole: "18.0 °C" claims a tenth of a degree the
// service did not report.
func TestSummaryDoesNotInventPrecision(t *testing.T) {
	reading := Reading{TemperatureC: num(18), WindSpeedKPH: num(14.8), RelativeHumidity: num(52.4)}
	assert.Equal(t, "18 °C, wind 15 km/h, humidity 52%", reading.Summary())
}

// The line a tester sees is the line the service's own numbers make, so the
// two are checked together rather than only in isolation.
func TestSummaryOfWhatTheServiceSent(t *testing.T) {
	provider, _ := fakeUpstream(t, oneHour, http.StatusOK)

	got, err := provider.At(context.Background(), 52.5, 13.4, at(t, "2026-08-23T13:00:00Z"))
	require.NoError(t, err)
	assert.Equal(t,
		"Partly cloudy, 18.7 °C, wind 15 km/h, humidity 52%, no precipitation",
		got.Summary())
}
