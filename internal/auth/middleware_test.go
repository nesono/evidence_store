package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/nesono/evidence-store/internal/config"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := GetRole(r.Context())
		w.Header().Set("X-Auth-Role", string(role))
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddlewareNoKeys(t *testing.T) {
	// With no keys configured, all requests pass through.
	mw := Middleware(nil)
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/evidence", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareMissingHeader(t *testing.T) {
	keys := []config.APIKey{{Key: "secret", ReadOnly: false}}
	handler := Middleware(keys)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddlewareInvalidKey(t *testing.T) {
	keys := []config.APIKey{{Key: "secret", ReadOnly: false}}
	handler := Middleware(keys)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddlewareValidRWKey(t *testing.T) {
	keys := []config.APIKey{{Key: "rw-secret", ReadOnly: false}}
	handler := Middleware(keys)(okHandler())

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "/api/v1/evidence", nil)
		req.Header.Set("Authorization", "Bearer rw-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "method %s should succeed", method)
		assert.Equal(t, "rw", rec.Header().Get("X-Auth-Role"))
	}
}

func TestMiddlewareROKeyAllowsGet(t *testing.T) {
	keys := []config.APIKey{{Key: "ro-secret", ReadOnly: true}}
	handler := Middleware(keys)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Bearer ro-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ro", rec.Header().Get("X-Auth-Role"))
}

func TestMiddlewareROKeyBlocksPost(t *testing.T) {
	keys := []config.APIKey{{Key: "ro-secret", ReadOnly: true}}
	handler := Middleware(keys)(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Bearer ro-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMiddlewareMultipleKeys(t *testing.T) {
	keys := []config.APIKey{
		{Key: "rw-key", ReadOnly: false},
		{Key: "ro-key", ReadOnly: true},
	}
	handler := Middleware(keys)(okHandler())

	// RW key can POST.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Bearer rw-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// RO key cannot POST.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Bearer ro-key")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// RO key can GET.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Bearer ro-key")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareBearerCaseInsensitive(t *testing.T) {
	keys := []config.APIKey{{Key: "secret", ReadOnly: false}}
	handler := Middleware(keys)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareNonBearerScheme(t *testing.T) {
	keys := []config.APIKey{{Key: "secret", ReadOnly: false}}
	handler := Middleware(keys)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
