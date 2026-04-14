package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/server"
)

// setupAuthServer creates a test server with API keys configured.
func setupAuthServer(t *testing.T, keys []config.APIKey) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL:     "unused",
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		APIKeys:         keys,
	}
	_ = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := server.New(cfg, testPool)
	return httptest.NewServer(srv.Handler())
}

func doRequest(t *testing.T, method, url, authHeader string, body any) *http.Response {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reqBody)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// ---------------------------------------------------------------------------
// Tests: Authentication
// ---------------------------------------------------------------------------

func TestAuthRequiredForAPI(t *testing.T) {
	keys := []config.APIKey{{Key: "test-rw-key", ReadOnly: false}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	// GET without key → 401.
	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// GET with valid key → 200.
	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer test-rw-key", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestAuthInvalidKey(t *testing.T) {
	keys := []config.APIKey{{Key: "real-key", ReadOnly: false}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer wrong-key", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

func TestHealthzBypassesAuth(t *testing.T) {
	keys := []config.APIKey{{Key: "test-key", ReadOnly: false}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	// /healthz without key → 200.
	resp := doRequest(t, http.MethodGet, ts.URL+"/healthz", "", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestROKeyCanGetButNotPost(t *testing.T) {
	keys := []config.APIKey{{Key: "ro-key", ReadOnly: true}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	// GET with RO key → 200.
	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer ro-key", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// POST with RO key → 403.
	ev := makeEvidence("org/auth_ro_test", "main", "ref1", "//pkg:test", "ci", model.ResultPass)
	resp = doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "Bearer ro-key", ev)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

func TestRWKeyCanPost(t *testing.T) {
	keys := []config.APIKey{{Key: "rw-key", ReadOnly: false}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	ev := makeEvidence("org/auth_rw_test", "main", "ref1", "//pkg:test", "ci", model.ResultPass)
	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "Bearer rw-key", ev)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

func TestNoKeysConfiguredAllowsAll(t *testing.T) {
	ts := setupAuthServer(t, nil)
	defer ts.Close()

	// GET without any auth → 200.
	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// POST without any auth → 201.
	ev := makeEvidence("org/auth_nokeys", "main", "ref1", "//pkg:test", "ci", model.ResultPass)
	resp = doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "", ev)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

func TestMultipleKeysWithDifferentRoles(t *testing.T) {
	keys := []config.APIKey{
		{Key: "admin-key", ReadOnly: false},
		{Key: "viewer-key", ReadOnly: true},
	}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	// Admin can POST.
	ev := makeEvidence("org/auth_multi", "main", "ref1", "//pkg:test", "ci", model.ResultPass)
	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "Bearer admin-key", ev)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Viewer can GET.
	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer viewer-key", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Viewer cannot POST.
	resp = doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "Bearer viewer-key", ev)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}
