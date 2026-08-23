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

	"github.com/nesono/evidence-store/internal/auth"
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
		Blob:            testBlobConfig,
	}
	_ = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := server.New(cfg, testPool, testBlobStore)
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

// ---------------------------------------------------------------------------
// Tests: Authorization by permission
// ---------------------------------------------------------------------------

// Reading an inheritance declaration is an ordinary read; declaring one is the
// elevated operation DESIGN.md section 8 specifies.
func TestROKeyBlockedFromInheritanceWrite(t *testing.T) {
	keys := []config.APIKey{{Key: "ro-key", ReadOnly: true}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/inheritance?repo=org/auth_inh&target_rcs_ref=tgt", "Bearer ro-key", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = doRequest(t, http.MethodPost, ts.URL+"/api/v1/inheritance", "Bearer ro-key", model.InheritanceCreate{
		Repo:          "org/auth_inh",
		SourceRCSRef:  "src_ro",
		TargetRCSRef:  "tgt_ro",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "should be refused",
		CreatedBy:     "ro-key",
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// The compatibility mapping's whole job: an rw key that could declare
// inheritance before the split still can.
func TestRWKeyCanWriteInheritance(t *testing.T) {
	keys := []config.APIKey{{Key: "rw-key", ReadOnly: false}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/inheritance", "Bearer rw-key", model.InheritanceCreate{
		Repo:          "org/auth_inh_rw",
		SourceRCSRef:  "src_rw",
		TargetRCSRef:  "tgt_rw",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "rw keys keep working",
		CreatedBy:     "rw-key",
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

// A contributor may write evidence but not rewrite what the store answers
// about untested code. No configured key maps to contributor yet, so the role
// is exercised directly against the server's own middleware.
func TestContributorBlockedFromInheritanceWrite(t *testing.T) {
	handler := auth.Require(auth.PermInheritanceWrite)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inheritance", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(),
		auth.NewPrincipal("user:alice@example.com", auth.KindUser, "Alice", auth.RoleContributor)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// Weather has no permission of its own: whoever may read a record may look up
// the weather that would go on one, and a viewer is the weakest such caller.
func TestROKeyCanReadWeather(t *testing.T) {
	keys := []config.APIKey{{Key: "ro-key", ReadOnly: true}}
	ts := setupAuthServer(t, keys)
	defer ts.Close()

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/weather?lat=48.1&lon=11.6&at=2026-01-01T00:00:00Z", "Bearer ro-key", nil)
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode)
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
