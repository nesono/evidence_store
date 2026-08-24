package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/server"
)

// ---------------------------------------------------------------------------
// Tests: Weather conditions
// ---------------------------------------------------------------------------

// What the weather was doing while a manual test ran lives in
// `metadata.weather_conditions` (DESIGN.md §2.2) as text, and
// `metadata.weather_observed_at` says which hour a fetched reading was for.
// Both are the record's own account: the store never reads them, because a
// reading that came back reworded would be a claim about the weather that
// neither the tester nor the service made.

func postWeathered(t *testing.T, prefix string, metadata map[string]any) model.Evidence {
	t.Helper()

	body := map[string]any{
		"repo":          "org/" + prefix + "_" + uuid.New().String()[:8],
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "manual/brake-check",
		"evidence_type": "manual_test",
		"source":        "j.tester",
		"result":        "PASS",
		"finished_at":   "2026-03-30 14:00",
		"metadata":      metadata,
	}

	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	return decodeJSON[model.Evidence](t, resp)
}

func TestWeatherRoundTripsVerbatim(t *testing.T) {
	const conditions = "Partly cloudy, 18.7 °C, wind 15 km/h, humidity 52%, no precipitation"
	created := postWeathered(t, "wx", map[string]any{
		"weather_conditions":  conditions,
		"weather_observed_at": "2026-03-30T14:00:00Z",
	})

	resp := getJSON(t, "/api/v1/evidence/"+created.ID.String())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	fetched := decodeJSON[model.Evidence](t, resp)

	var meta struct {
		Conditions string `json:"weather_conditions"`
		ObservedAt string `json:"weather_observed_at"`
	}
	require.NoError(t, json.Unmarshal(fetched.Metadata, &meta))
	assert.Equal(t, conditions, meta.Conditions)
	assert.Equal(t, "2026-03-30T14:00:00Z", meta.ObservedAt)
}

// The tester who was standing there outranks a model that put the hailstorm two
// valleys away, so a line they wrote themselves is stored as they wrote it —
// and carries no hour, because there is no reading behind it to date.
func TestWeatherKeepsWhatTheTesterWrote(t *testing.T) {
	const written = "Hagelschauer, Sicht unter 50 m, Fahrbahn nass"
	created := postWeathered(t, "wx_typed", map[string]any{"weather_conditions": written})

	var meta struct {
		Conditions string  `json:"weather_conditions"`
		ObservedAt *string `json:"weather_observed_at"`
	}
	require.NoError(t, json.Unmarshal(created.Metadata, &meta))
	assert.Equal(t, written, meta.Conditions)
	assert.Nil(t, meta.ObservedAt)
}

// Weather sits beside where the test was run and what the tester saw, so a
// record can have any of the three, and adding one does not disturb the others.
func TestWeatherLocationAndTestLogAreIndependent(t *testing.T) {
	created := postWeathered(t, "wx_loc", map[string]any{
		"weather_conditions": "Overcast, 8 °C, precipitation 1.2 mm",
		"location":           "52.51631, 13.37771",
		"observations":       testLog,
	})

	var meta struct {
		Conditions   string `json:"weather_conditions"`
		Location     string `json:"location"`
		Observations string `json:"observations"`
	}
	require.NoError(t, json.Unmarshal(created.Metadata, &meta))
	assert.Equal(t, "Overcast, 8 °C, precipitation 1.2 mm", meta.Conditions)
	assert.Equal(t, "52.51631, 13.37771", meta.Location)
	assert.Equal(t, testLog, meta.Observations)
}

// ---------------------------------------------------------------------------
// Tests: the weather lookup endpoint
// ---------------------------------------------------------------------------

// The upstream is a fake. A suite that asked the real service what the sky was
// doing would fail when it rained, and would tell a third party the coordinates
// of every machine that runs the tests.
func setupWeatherServer(t *testing.T, upstream string) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL:     "unused",
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		Blob:            testBlobConfig,
		Weather:         config.Weather{Endpoint: upstream, Timeout: 5 * time.Second},
	}
	ts := httptest.NewServer(server.New(cfg, testPool, testBlobStore, server.SSO{}).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func fakeWeatherUpstream(t *testing.T) string {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"hourly": {
          "time": ["2026-03-30T14:00"],
          "temperature_2m": [8.4],
          "relative_humidity_2m": [81],
          "precipitation": [1.2],
          "weather_code": [3],
          "wind_speed_10m": [22.0]
        }}`))
	}))
	t.Cleanup(upstream.Close)
	return upstream.URL
}

// The line the endpoint answers with is the line that goes in the field, and
// the same line any other client would write into `weather_conditions`.
func TestWeatherEndpointAnswersWithALineForTheField(t *testing.T) {
	ts := setupWeatherServer(t, fakeWeatherUpstream(t))

	resp := doRequest(t, http.MethodGet,
		ts.URL+"/api/v1/weather?lat=52.51631&lon=13.37771&at=2026-03-30T14:05:00Z", "", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Summary    string `json:"summary"`
		ObservedAt string `json:"observed_at"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "Overcast, 8.4 °C, wind 22 km/h, humidity 81%, precipitation 1.2 mm", body.Summary)
	assert.Contains(t, body.ObservedAt, "2026-03-30T14:00")
}

// A lookup writes nothing, so a read-only key may make one: the same key that
// may read a record may look up the weather that would go on one.
func TestWeatherEndpointIsAReadNotAWrite(t *testing.T) {
	upstream := fakeWeatherUpstream(t)
	cfg := &config.Config{
		DatabaseURL:     "unused",
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		APIKeys:         []config.APIKey{{Key: "reader", ReadOnly: true}},
		Blob:            testBlobConfig,
		Weather:         config.Weather{Endpoint: upstream, Timeout: 5 * time.Second},
	}
	ts := httptest.NewServer(server.New(cfg, testPool, testBlobStore, server.SSO{}).Handler())
	defer ts.Close()

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/weather?lat=52.5&lon=13.4", "Bearer reader", nil)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// And it is behind the key like everything else under /api/v1.
	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/weather?lat=52.5&lon=13.4", "", nil)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A deployment with no way out to the internet configures the lookup away. The
// route still answers, because a form whose button silently does nothing is
// worse than one that says the button is off here.
func TestWeatherEndpointCanBeSwitchedOff(t *testing.T) {
	ts := setupWeatherServer(t, "")

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/weather?lat=52.5&lon=13.4", "", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body.Error, "disabled")
}
