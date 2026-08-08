package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/analytics"
	"github.com/nesono/evidence-store/internal/model"
)

func sample(procedure string) []analytics.TestStats {
	return []analytics.TestStats{{
		Repo:         "org/repo",
		ProcedureRef: procedure,
		Counts:       analytics.Counts{Pass: 10, Fail: 2},
	}}
}

func strPtr(s string) *string { return &s }

func TestStatsCacheDisabledNeverStores(t *testing.T) {
	c := newStatsCache(0, 10)

	c.put("k", sample("//a"))
	_, ok := c.get("k")
	assert.False(t, ok, "a zero TTL disables the cache entirely")
}

func TestStatsCacheRoundTrip(t *testing.T) {
	c := newStatsCache(time.Minute, 10)

	c.put("k", sample("//a"))
	got, ok := c.get("k")
	require.True(t, ok)
	require.Len(t, got, 1)
	assert.Equal(t, "//a", got[0].ProcedureRef)
}

func TestStatsCacheMissesOnUnknownKey(t *testing.T) {
	c := newStatsCache(time.Minute, 10)
	c.put("k", sample("//a"))

	_, ok := c.get("other")
	assert.False(t, ok)
}

func TestStatsCacheExpires(t *testing.T) {
	c := newStatsCache(20*time.Millisecond, 10)
	c.put("k", sample("//a"))

	_, ok := c.get("k")
	require.True(t, ok)

	time.Sleep(40 * time.Millisecond)
	_, ok = c.get("k")
	assert.False(t, ok, "an entry past its TTL is a miss")
}

// The caller runs Finalize and Sort over what it gets back, and both mutate in
// place. Handing out the stored slice would let one request's thresholds and
// ordering leak into the next one's answer.
func TestStatsCacheHandsOutCopies(t *testing.T) {
	c := newStatsCache(time.Minute, 10)
	c.put("k", sample("//a"))

	first, ok := c.get("k")
	require.True(t, ok)
	first[0].ProcedureRef = "//mutated"
	first[0].Labels = []string{"flaky"}
	first[0].FailRate = 0.99

	second, ok := c.get("k")
	require.True(t, ok)
	assert.Equal(t, "//a", second[0].ProcedureRef)
	assert.Nil(t, second[0].Labels)
	assert.Zero(t, second[0].FailRate)
}

// Storing the caller's slice would let the caller mutate the cache afterwards.
func TestStatsCacheCopiesOnPut(t *testing.T) {
	c := newStatsCache(time.Minute, 10)

	stored := sample("//a")
	c.put("k", stored)
	stored[0].ProcedureRef = "//mutated"

	got, ok := c.get("k")
	require.True(t, ok)
	assert.Equal(t, "//a", got[0].ProcedureRef)
}

func TestStatsCacheEvictsWhenFull(t *testing.T) {
	c := newStatsCache(time.Minute, 2)

	c.put("a", sample("//a"))
	c.put("b", sample("//b"))
	c.put("c", sample("//c"))

	assert.LessOrEqual(t, c.len(), 2, "the cache is a bounded buffer, not a leak")
}

// --- Keys ---

func TestStatsCacheKeySameFilterSameKey(t *testing.T) {
	p := TestStatsParams{Filter: model.EvidenceFilter{Repo: strPtr("org/repo")}}
	assert.Equal(t, statsCacheKey(p), statsCacheKey(p))
}

func TestStatsCacheKeyDistinguishesFilters(t *testing.T) {
	a := TestStatsParams{Filter: model.EvidenceFilter{Repo: strPtr("org/one")}}
	b := TestStatsParams{Filter: model.EvidenceFilter{Repo: strPtr("org/two")}}
	assert.NotEqual(t, statsCacheKey(a), statsCacheKey(b))
}

func TestStatsCacheKeyDistinguishesGrouping(t *testing.T) {
	base := model.EvidenceFilter{Repo: strPtr("org/repo")}
	a := TestStatsParams{Filter: base}
	b := TestStatsParams{Filter: base, GroupByEvidenceType: true}
	assert.NotEqual(t, statsCacheKey(a), statsCacheKey(b))
}

func TestStatsCacheKeyDistinguishesTimeWindows(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	a := TestStatsParams{Filter: model.EvidenceFilter{FinishedAfter: &t1}}
	b := TestStatsParams{Filter: model.EvidenceFilter{FinishedAfter: &t2}}
	assert.NotEqual(t, statsCacheKey(a), statsCacheKey(b))
}

// Two filters differing only in result order are different queries as far as
// the key is concerned; that is safe (a needless miss), never wrong.
func TestStatsCacheKeyIsStableForIdenticalResultSets(t *testing.T) {
	a := TestStatsParams{Filter: model.EvidenceFilter{
		Result: []model.EvidenceResult{model.ResultPass, model.ResultFail}}}
	b := TestStatsParams{Filter: model.EvidenceFilter{
		Result: []model.EvidenceResult{model.ResultPass, model.ResultFail}}}
	assert.Equal(t, statsCacheKey(a), statsCacheKey(b))
}
