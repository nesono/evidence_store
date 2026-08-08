package tests

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/analytics"
)

// ---------------------------------------------------------------------------
// Aggregation cache
//
// Sorting and paging are applied after the query, so they re-ask for an
// aggregation whose inputs did not change. The cache serves those — and must
// not let one request's thresholds or ordering reach the next one's answer.
// ---------------------------------------------------------------------------

func TestAnalyticsCacheServesRepeatedQueries(t *testing.T) {
	f := seedAnalyticsFixture(t)

	first := f.get(t, url.Values{"sort": {"fail_rate"}, "order": {"desc"}})
	second := f.get(t, url.Values{"sort": {"fail_rate"}, "order": {"desc"}})

	require.Equal(t, first.Total, second.Total)
	require.Len(t, second.Tests, len(first.Tests))
	for i := range first.Tests {
		assert.Equal(t, first.Tests[i].ProcedureRef, second.Tests[i].ProcedureRef)
		assert.Equal(t, first.Tests[i].Counts, second.Tests[i].Counts)
	}
}

// Re-sorting is the case the cache exists for: same filter, different order.
// The second answer must be genuinely re-sorted, not the first one replayed.
func TestAnalyticsCacheDoesNotFreezeOrdering(t *testing.T) {
	f := seedAnalyticsFixture(t)

	desc := f.get(t, url.Values{"sort": {"fail_rate"}, "order": {"desc"}})
	asc := f.get(t, url.Values{"sort": {"fail_rate"}, "order": {"asc"}})

	require.NotEmpty(t, desc.Tests)
	require.NotEmpty(t, asc.Tests)
	assert.Equal(t, "//broken:test", desc.Tests[0].ProcedureRef)
	assert.NotEqual(t, desc.Tests[0].ProcedureRef, asc.Tests[0].ProcedureRef)

	// Ascending is the exact reverse walk of the same set.
	assert.Equal(t, desc.Total, asc.Total)
}

// Thresholds are applied to a copy after the cache hands the aggregation back,
// so two requests differing only in threshold must disagree about labels.
func TestAnalyticsCacheKeepsThresholdsPerRequest(t *testing.T) {
	f := seedAnalyticsFixture(t)

	lenient := f.get(t, url.Values{"min_runs": {"1"}})
	strict := f.get(t, url.Values{"min_runs": {"25"}})

	assert.Equal(t, []string{analytics.LabelStable},
		byProcedure(t, lenient, "//stable:test").Labels)
	assert.Equal(t, []string{analytics.LabelSparse},
		byProcedure(t, strict, "//stable:test").Labels,
		"a cached aggregation must not carry the previous request's labels")
}

// Paging is applied after the cache too, so a later window must be a different
// slice of the same set rather than a repeat of the first.
func TestAnalyticsCacheKeepsPagingPerRequest(t *testing.T) {
	f := seedAnalyticsFixture(t)

	all := f.get(t, url.Values{"sort": {"procedure_ref"}})
	page := f.get(t, url.Values{"sort": {"procedure_ref"}, "limit": {"2"}, "offset": {"2"}})

	require.Len(t, page.Tests, 2)
	assert.Equal(t, all.Tests[2].ProcedureRef, page.Tests[0].ProcedureRef)
	assert.Equal(t, all.Total, page.Total)
}

// A different filter is a different question and must not be answered from
// another filter's entry.
func TestAnalyticsCacheKeyedByFilter(t *testing.T) {
	f := seedAnalyticsFixture(t)

	all := f.get(t, nil)
	narrowed := f.get(t, url.Values{"procedure_ref": {"//stable:test"}})

	assert.Equal(t, 6, all.Total)
	assert.Equal(t, 1, narrowed.Total)
}

// Narrowing the window is also a different key, so the counts must move.
func TestAnalyticsCacheKeyedByTimeWindow(t *testing.T) {
	f := seedAnalyticsFixture(t)

	wide := f.get(t, nil)
	narrow := f.get(t, url.Values{
		"finished_before": {fixtureBase.Add(10 * time.Minute).Format(time.RFC3339)},
	})

	assert.Equal(t, 6, wide.Total)
	assert.Equal(t, 1, narrow.Total)
}
