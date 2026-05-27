package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/nesono/evidence-store/internal/config"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func request(method, key string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/evidence", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	return r
}

func TestDisabledIsNoOp(t *testing.T) {
	mw := Middleware(config.RateLimit{})
	handler := mw(okHandler())

	for range 1000 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodGet, "key1"))
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestReadLimitEnforced(t *testing.T) {
	mw := Middleware(config.RateLimit{ReadRPS: 1, ReadBurst: 3})
	handler := mw(okHandler())

	// First 3 reads consume the burst.
	for i := range 3 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodGet, "key1"))
		assert.Equal(t, http.StatusOK, rec.Code, "request %d should pass", i+1)
	}

	// The next read should be denied.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, "key1"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, ra, 1)
}

func TestWriteLimitEnforced(t *testing.T) {
	mw := Middleware(config.RateLimit{WriteRPS: 1, WriteBurst: 2})
	handler := mw(okHandler())

	for range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodPost, "key1"))
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "key1"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestReadAndWriteBucketsAreIndependent(t *testing.T) {
	mw := Middleware(config.RateLimit{
		ReadRPS: 1, ReadBurst: 2,
		WriteRPS: 1, WriteBurst: 2,
	})
	handler := mw(okHandler())

	// Drain reads.
	for range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodGet, "key1"))
		assert.Equal(t, http.StatusOK, rec.Code)
	}
	// Reads exhausted.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, "key1"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Writes should still be available.
	for i := range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodPost, "key1"))
		assert.Equal(t, http.StatusOK, rec.Code, "write %d should pass while reads are exhausted", i+1)
	}
}

func TestPerKeyIsolation(t *testing.T) {
	mw := Middleware(config.RateLimit{ReadRPS: 1, ReadBurst: 2})
	handler := mw(okHandler())

	for range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodGet, "key1"))
		assert.Equal(t, http.StatusOK, rec.Code)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, "key1"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "key1 should be exhausted")

	// A different key should be unaffected.
	for range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodGet, "key2"))
		assert.Equal(t, http.StatusOK, rec.Code, "key2 should have its own bucket")
	}
}

func TestUnauthenticatedFallsBackToIP(t *testing.T) {
	mw := Middleware(config.RateLimit{ReadRPS: 1, ReadBurst: 1})
	handler := mw(okHandler())

	r1 := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	r1.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r1)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Same IP, second request — denied.
	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	r2.RemoteAddr = "10.0.0.1:9999"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, r2)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Different IP — allowed.
	r3 := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	r3.RemoteAddr = "10.0.0.2:1234"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, r3)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestOnlyReadConfiguredAllowsWrites(t *testing.T) {
	mw := Middleware(config.RateLimit{ReadRPS: 1, ReadBurst: 1})
	handler := mw(okHandler())

	// Write bucket disabled — unlimited writes.
	for range 50 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodPost, "key1"))
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestXForwardedForRespected(t *testing.T) {
	mw := Middleware(config.RateLimit{ReadRPS: 1, ReadBurst: 1})
	handler := mw(okHandler())

	r1 := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	r1.RemoteAddr = "10.0.0.99:1234"
	r1.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.99")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r1)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Same forwarded IP, different RemoteAddr — should still be the same caller.
	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	r2.RemoteAddr = "10.0.0.42:5555"
	r2.Header.Set("X-Forwarded-For", "203.0.113.5")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, r2)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}
