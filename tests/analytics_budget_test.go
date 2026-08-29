package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/server"
)

// serverWithBudget builds a server whose analytics queries must finish within
// the given budget. A nanosecond is unmeetable, which is the point: it exercises
// the path a genuinely oversized query takes without needing an oversized
// database to produce one.
func serverWithBudget(t *testing.T, budget time.Duration) *httptest.Server {
	t.Helper()

	cfg := &config.Config{
		DatabaseURL:     testPool.Config().ConnString(),
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		QueryTimeout:    budget,
		Blob:            testBlobConfig,
	}

	srv := httptest.NewServer(server.New(cfg, testPool, testBlobStore, server.SSO{}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func getFrom(t *testing.T, srv *httptest.Server, path string) (*http.Response, map[string]any) {
	t.Helper()

	resp, err := http.Get(srv.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

// A query that cannot finish in budget must come back as a refusal with advice,
// not as a dropped connection after the server's request timeout.
func TestAnalyticsBudgetRefusesInsteadOfHanging(t *testing.T) {
	f := seedAnalyticsFixture(t)
	srv := serverWithBudget(t, time.Nanosecond)

	for _, path := range []string{
		"/api/v1/analytics/tests?repo=" + url.QueryEscape(f.repo),
		"/api/v1/analytics/summary?repo=" + url.QueryEscape(f.repo),
		"/api/v1/analytics/clusters?repo=" + url.QueryEscape(f.repo),
	} {
		resp, body := getFrom(t, srv, path)
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, path)
		assert.Contains(t, body["error"], "narrow the filter or the time window", path)
	}
}

// The advice has to name the budget, or the reader cannot tell whether the
// query was outrageous or the limit was tight.
func TestAnalyticsBudgetMessageNamesTheLimit(t *testing.T) {
	f := seedAnalyticsFixture(t)
	srv := serverWithBudget(t, time.Nanosecond)

	_, body := getFrom(t, srv, "/api/v1/analytics/tests?repo="+url.QueryEscape(f.repo))
	assert.Contains(t, body["error"], "1ns")
}

// A budget generous enough for the query must not interfere with it.
func TestAnalyticsBudgetAllowsQueriesThatFit(t *testing.T) {
	f := seedAnalyticsFixture(t)
	srv := serverWithBudget(t, 30*time.Second)

	resp, body := getFrom(t, srv, "/api/v1/analytics/tests?repo="+url.QueryEscape(f.repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, float64(6), body["total"])
}

// Zero means "no budget of our own", leaving only the server's request timeout.
func TestAnalyticsBudgetZeroDisables(t *testing.T) {
	f := seedAnalyticsFixture(t)
	srv := serverWithBudget(t, 0)

	resp, body := getFrom(t, srv, "/api/v1/analytics/tests?repo="+url.QueryEscape(f.repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, float64(6), body["total"])
}

// --- The same budget on the search path (issue #123) ---
//
// Search accepts a POSIX regex behind a leading `~`, which Postgres evaluates.
// The pattern is bound as an argument so there is nothing to inject; it is the
// cost of evaluating it that is worth bounding, and until now only analytics
// was bounded at all.

func TestSearchBudgetRefusesInsteadOfHanging(t *testing.T) {
	srv := serverWithBudget(t, time.Nanosecond)

	for _, path := range []string{
		"/api/v1/evidence?repo=" + url.QueryEscape("~.*"),
		"/api/v1/evidence",
		"/api/v1/evidence/distinct?field=repo",
	} {
		resp, body := getFrom(t, srv, path)
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, path)
		assert.Contains(t, body["error"], "narrow the filter or the time window", path)
	}
}

// The two paths must refuse in the same words. A caller who has learned what
// the message means on one endpoint should not have to learn it again.
func TestSearchAndAnalyticsRefuseIdentically(t *testing.T) {
	f := seedAnalyticsFixture(t)
	srv := serverWithBudget(t, time.Nanosecond)

	_, search := getFrom(t, srv, "/api/v1/evidence")
	_, analytics := getFrom(t, srv, "/api/v1/analytics/summary?repo="+url.QueryEscape(f.repo))

	assert.Equal(t, analytics["error"], search["error"])
}

// With a budget that can be met, nothing changes: the same query answers.
func TestSearchWithAWorkableBudgetStillAnswers(t *testing.T) {
	srv := serverWithBudget(t, 15*time.Second)

	resp, body := getFrom(t, srv, "/api/v1/evidence?limit=1")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, body, "error")
}

// Zero switches the budget off, as it always has for analytics.
func TestSearchBudgetCanBeDisabled(t *testing.T) {
	srv := serverWithBudget(t, 0)

	resp, _ := getFrom(t, srv, "/api/v1/evidence?limit=1")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
