package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/nesono/evidence-store/internal/analytics"
)

// statsCache holds recent aggregation results for a short time.
//
// The win it exists for is not repeated page loads — it is sorting and paging.
// Both are done in Go over the whole result set, so every click on a column
// header re-runs an aggregation whose inputs did not change. Caching by filter
// alone makes those instant while still refetching when the question changes.
//
// Entries are copied both in and out. The caller runs analytics.Finalize and
// analytics.Sort over what it gets, and both mutate in place; sharing the stored
// slice would let one request's thresholds and ordering leak into the next.
type statsCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]statsEntry
}

type statsEntry struct {
	stats   []analytics.TestStats
	expires time.Time
}

// newStatsCache returns a cache holding entries for ttl. A ttl of zero or less
// disables it: get always misses and put stores nothing.
func newStatsCache(ttl time.Duration, max int) *statsCache {
	return &statsCache{
		ttl:     ttl,
		max:     max,
		entries: make(map[string]statsEntry),
	}
}

func (c *statsCache) enabled() bool { return c != nil && c.ttl > 0 }

func (c *statsCache) get(key string) ([]analytics.TestStats, bool) {
	if !c.enabled() {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return copyStats(entry.stats), true
}

func (c *statsCache) put(key string, stats []analytics.TestStats) {
	if !c.enabled() {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Bounded rather than clever: analytics filters vary little in practice, and
	// an unbounded map keyed by user input is a slow leak. Dropping expired
	// entries first usually makes room; if it does not, the write is skipped
	// rather than evicting something a live request may be about to reuse.
	if len(c.entries) >= c.max {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.max {
			return
		}
	}

	c.entries[key] = statsEntry{
		stats:   copyStats(stats),
		expires: time.Now().Add(c.ttl),
	}
}

func (c *statsCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func copyStats(src []analytics.TestStats) []analytics.TestStats {
	// A shallow copy is enough: the only reference field is Labels, and Finalize
	// replaces it with a freshly built slice rather than appending to it.
	dst := make([]analytics.TestStats, len(src))
	copy(dst, src)
	return dst
}

// statsCacheKey identifies an aggregation by everything that changes its result
// — the filter and the grouping. Sort key, direction, paging and the labelling
// thresholds are deliberately absent: they are applied after the query, so they
// are exactly what the cache is meant to serve without a round trip.
func statsCacheKey(params TestStatsParams) string {
	payload := struct {
		Filter  any  `json:"f"`
		ByType  bool `json:"t"`
		MaxRows int  `json:"m"`
	}{params.Filter, params.GroupByEvidenceType, params.MaxGroups}

	// Marshalling a struct with no maps is deterministic, and hashing keeps the
	// key short regardless of how long a regex someone filtered with.
	encoded, err := json.Marshal(payload)
	if err != nil {
		// Unreachable for this shape; a unique key just means a guaranteed miss.
		return time.Now().String()
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
