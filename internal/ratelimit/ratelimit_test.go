package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// --- Bounding the limiter map (issue #122) ---

func TestTokensAreNotKeptAsMapKeys(t *testing.T) {
	// The map used to be keyed on the caller key, which for an authenticated
	// caller contains their bearer token — a copy of every live credential,
	// sitting in the heap of a process that will eventually be dumped.
	s := newStore(config.RateLimit{ReadRPS: 10, ReadBurst: 10})
	const secret = "k:super-secret-api-key"

	require.NotNil(t, s.limiterFor(secret, false))

	for key := range s.readers {
		assert.NotContains(t, key, "super-secret-api-key")
	}
	assert.Contains(t, s.readers, identify(secret), "and it is still found again by the same caller")
}

func TestTheSameCallerKeepsTheSameLimiter(t *testing.T) {
	s := newStore(config.RateLimit{ReadRPS: 10, ReadBurst: 10})

	first := s.limiterFor("ip:10.0.0.1", false)
	second := s.limiterFor("ip:10.0.0.1", false)

	assert.Same(t, first, second)
	assert.Len(t, s.readers, 1)
}

func TestIdleLimitersAreCollectedOnceTheMapGrows(t *testing.T) {
	s := newStore(config.RateLimit{ReadRPS: 1000, ReadBurst: 1000})

	// Enough one-off callers to cross the threshold. None of them spends a
	// token, so every bucket is full and every one is collectable.
	for i := 0; i < sweepThreshold+1; i++ {
		s.limiterFor(fmt.Sprintf("ip:10.0.0.%d", i), false)
	}

	assert.Less(t, len(s.readers), sweepThreshold,
		"a map that only grows is how a limiter becomes a memory leak")
}

func TestACallerWhoStillOwesIsNotCollected(t *testing.T) {
	// The property that makes eviction safe: only full buckets go, and a bucket
	// that is not full belongs to somebody who has spent tokens recently.
	s := newStore(config.RateLimit{ReadRPS: 0.001, ReadBurst: 2})

	spender := s.limiterFor("ip:the-spender", false)
	require.True(t, spender.Allow(), "spend one of its two tokens")

	for i := 0; i < sweepThreshold+1; i++ {
		s.limiterFor(fmt.Sprintf("ip:10.1.0.%d", i), false)
	}

	assert.Same(t, spender, s.limiterFor("ip:the-spender", false),
		"the caller mid-window must keep the bucket that remembers what they spent")
}

func TestCollectionCannotForgiveRateDebt(t *testing.T) {
	// A limiter created fresh starts full, so dropping one that has already
	// refilled and recreating it produces exactly the state the caller would
	// have had anyway. This is that claim, exercised: a caller at their limit
	// stays at their limit across a sweep.
	s := newStore(config.RateLimit{ReadRPS: 0.001, ReadBurst: 1})

	limiter := s.limiterFor("ip:at-the-limit", false)
	require.True(t, limiter.Allow(), "the one token this caller has")
	require.False(t, limiter.Allow(), "and now they are out")

	for i := 0; i < sweepThreshold+1; i++ {
		s.limiterFor(fmt.Sprintf("ip:10.2.0.%d", i), false)
	}

	assert.False(t, s.limiterFor("ip:at-the-limit", false).Allow(),
		"a sweep must not hand back a token the caller had already spent")
}

func TestReadAndWriteBucketsAreSweptIndependently(t *testing.T) {
	s := newStore(config.RateLimit{ReadRPS: 1000, ReadBurst: 1000, WriteRPS: 1000, WriteBurst: 1000})

	writer := s.limiterFor("ip:a-writer", true)
	for i := 0; i < sweepThreshold+1; i++ {
		s.limiterFor(fmt.Sprintf("ip:10.3.0.%d", i), false)
	}

	assert.Same(t, writer, s.limiterFor("ip:a-writer", true),
		"sweeping the read bucket must not disturb the write bucket")
}
