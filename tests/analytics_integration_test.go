package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/analytics"
	"github.com/nesono/evidence-store/internal/model"
)

// ---------------------------------------------------------------------------
// Fixture
//
// One repo containing one test of each kind we claim to detect. Every count in
// the assertions below is hand-derived from this fixture, so a change in the
// aggregation shows up as a specific wrong number rather than a vague failure.
// ---------------------------------------------------------------------------

type analyticsFixture struct {
	repo string
}

var fixtureBase = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func seedAnalyticsFixture(t *testing.T) analyticsFixture {
	t.Helper()

	repo := fmt.Sprintf("analytics/%d", time.Now().UnixNano())
	var records []model.EvidenceCreate

	// finished_at increases by one minute per record, so the ordered scan that
	// computes flips has an unambiguous sequence to walk.
	minute := 0
	add := func(procedure, commit string, result model.EvidenceResult) {
		records = append(records, model.EvidenceCreate{
			Repo:         repo,
			Branch:       "main",
			RCSRef:       commit,
			ProcedureRef: procedure,
			EvidenceType: "bazel",
			Source:       "ci",
			Result:       result,
			FinishedAt:   model.FlexibleTime{Time: fixtureBase.Add(time.Duration(minute) * time.Minute)},
		})
		minute++
	}

	// Never fails: 20 passes over 20 commits.
	for i := range 20 {
		add("//stable:test", fmt.Sprintf("s%02d", i), model.ResultPass)
	}

	// Always fails: 20 failures, no flips — broken, not flaky.
	for i := range 20 {
		add("//broken:test", fmt.Sprintf("b%02d", i), model.ResultFail)
	}

	// Flaky by alternation: every consecutive pair changes verdict.
	for i := range 20 {
		result := model.ResultPass
		if i%2 == 1 {
			result = model.ResultFail
		}
		add("//flaky:test", fmt.Sprintf("f%02d", i), result)
	}

	// Flaky by disagreement at one commit, and only that: the flip rate stays
	// below the threshold, so this test is detected purely by same-commit
	// contradiction.
	add("//disagree:test", "d00", model.ResultPass)
	add("//disagree:test", "d00", model.ResultFail)
	for i := 1; i < 19; i++ {
		add("//disagree:test", fmt.Sprintf("d%02d", i), model.ResultPass)
	}

	// Infrastructure-heavy: half its runs never produced a verdict.
	for i := range 10 {
		add("//infra:test", fmt.Sprintf("i%02d", i), model.ResultPass)
	}
	for i := 10; i < 20; i++ {
		add("//infra:test", fmt.Sprintf("i%02d", i), model.ResultError)
	}

	// Too little history to judge.
	for i := range 3 {
		add("//sparse:test", fmt.Sprintf("p%02d", i), model.ResultPass)
	}

	_, err := testEvidenceStore.InsertBatch(context.Background(), records)
	require.NoError(t, err)

	return analyticsFixture{repo: repo}
}

type analyticsTestsResponse struct {
	Window struct {
		From  *time.Time `json:"from"`
		To    *time.Time `json:"to"`
		Runs  int        `json:"runs"`
		Tests int        `json:"tests"`
	} `json:"window"`
	Thresholds analytics.Thresholds  `json:"thresholds"`
	Tests      []analytics.TestStats `json:"tests"`
	Total      int                   `json:"total"`
}

func (f analyticsFixture) get(t *testing.T, extra url.Values) analyticsTestsResponse {
	t.Helper()
	q := url.Values{"repo": {f.repo}}
	for k, vs := range extra {
		q[k] = vs
	}
	resp := getJSON(t, "/api/v1/analytics/tests?"+q.Encode())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSON[analyticsTestsResponse](t, resp)
}

func byProcedure(t *testing.T, body analyticsTestsResponse, procedure string) analytics.TestStats {
	t.Helper()
	for _, s := range body.Tests {
		if s.ProcedureRef == procedure {
			return s
		}
	}
	t.Fatalf("no stats returned for %s", procedure)
	return analytics.TestStats{}
}

// ---------------------------------------------------------------------------
// Aggregation
// ---------------------------------------------------------------------------

func TestAnalyticsAggregatesOneRowPerTest(t *testing.T) {
	f := seedAnalyticsFixture(t)
	body := f.get(t, nil)

	assert.Equal(t, 6, body.Total)
	assert.Equal(t, 6, body.Window.Tests)
	assert.Equal(t, 103, body.Window.Runs)

	require.NotNil(t, body.Window.From)
	require.NotNil(t, body.Window.To)
	assert.Equal(t, fixtureBase.UTC(), body.Window.From.UTC())
	assert.Equal(t, fixtureBase.Add(102*time.Minute).UTC(), body.Window.To.UTC())
}

func TestAnalyticsNeverFailingTest(t *testing.T) {
	f := seedAnalyticsFixture(t)
	s := byProcedure(t, f.get(t, nil), "//stable:test")

	assert.Equal(t, 20, s.Counts.Pass)
	assert.Zero(t, s.Counts.Fail)
	assert.Zero(t, s.Counts.Error)
	assert.Equal(t, 20, s.VerdictRuns)
	assert.InDelta(t, 0, s.FailRate, 1e-9)
	assert.InDelta(t, 0, s.FlipRate, 1e-9)
	assert.Equal(t, 19, s.Counts.Transitions)
	assert.Zero(t, s.Counts.Flips)
	assert.InDelta(t, analytics.WilsonLower(20, 20), s.PassRateLower, 1e-9)
	assert.Equal(t, []string{analytics.LabelStable}, s.Labels)
	assert.Equal(t, "PASS", s.LastResult)
	assert.Nil(t, s.LastFailAt)
	require.NotNil(t, s.LastPassAt)
}

func TestAnalyticsAlwaysFailingTest(t *testing.T) {
	f := seedAnalyticsFixture(t)
	s := byProcedure(t, f.get(t, nil), "//broken:test")

	assert.Equal(t, 20, s.Counts.Fail)
	assert.InDelta(t, 1.0, s.FailRate, 1e-9)
	assert.Contains(t, s.Labels, analytics.LabelAlwaysFailing)

	// The distinction the fail rate alone cannot make.
	assert.InDelta(t, 0, s.FlipRate, 1e-9)
	assert.NotContains(t, s.Labels, analytics.LabelFlaky)
	assert.Equal(t, "FAIL", s.LastResult)
	assert.Nil(t, s.LastPassAt)
}

func TestAnalyticsFlakyByAlternation(t *testing.T) {
	f := seedAnalyticsFixture(t)
	s := byProcedure(t, f.get(t, nil), "//flaky:test")

	assert.Equal(t, 10, s.Counts.Pass)
	assert.Equal(t, 10, s.Counts.Fail)
	assert.Equal(t, 19, s.Counts.Transitions)
	assert.Equal(t, 19, s.Counts.Flips, "every consecutive pair alternates")
	assert.InDelta(t, 1.0, s.FlipRate, 1e-9)
	assert.Contains(t, s.Labels, analytics.LabelFlaky)
	assert.NotContains(t, s.Labels, analytics.LabelAlwaysFailing)
}

// Same commit, both outcomes. The flip rate here is 2/19, below the threshold,
// so this test is caught only by the same-commit contradiction.
func TestAnalyticsFlakyBySameCommitDisagreement(t *testing.T) {
	f := seedAnalyticsFixture(t)
	s := byProcedure(t, f.get(t, nil), "//disagree:test")

	assert.Equal(t, 19, s.Counts.Pass)
	assert.Equal(t, 1, s.Counts.Fail)
	assert.Equal(t, 1, s.Counts.FlakyCommits)
	assert.Equal(t, 2, s.Counts.Flips)
	assert.Equal(t, 19, s.Counts.Transitions)
	assert.Less(t, s.FlipRate, analytics.DefaultThresholds().FlipRate)
	assert.Contains(t, s.Labels, analytics.LabelFlaky)
}

func TestAnalyticsInfrastructureHeavyTest(t *testing.T) {
	f := seedAnalyticsFixture(t)
	s := byProcedure(t, f.get(t, nil), "//infra:test")

	assert.Equal(t, 10, s.Counts.Error)
	assert.Equal(t, 20, s.Runs)
	assert.Equal(t, 10, s.VerdictRuns)
	assert.InDelta(t, 0.5, s.ErrorRate, 1e-9)
	assert.InDelta(t, 0, s.FailRate, 1e-9, "errors must not count as failures")
	assert.Contains(t, s.Labels, analytics.LabelInfraHeavy)

	// ERROR rows are dropped before the sequence is walked, so an infrastructure
	// blip between two passes is not two flips.
	assert.Zero(t, s.Counts.Flips)
	assert.Equal(t, 9, s.Counts.Transitions)
}

func TestAnalyticsSparseTestIsNotCalledStable(t *testing.T) {
	f := seedAnalyticsFixture(t)
	s := byProcedure(t, f.get(t, nil), "//sparse:test")

	assert.Equal(t, 3, s.Counts.Pass)
	assert.Equal(t, []string{analytics.LabelSparse}, s.Labels)
	assert.NotContains(t, s.Labels, analytics.LabelStable)
}

// ---------------------------------------------------------------------------
// Query surface
// ---------------------------------------------------------------------------

func TestAnalyticsSortByFailRate(t *testing.T) {
	f := seedAnalyticsFixture(t)
	body := f.get(t, url.Values{"sort": {"fail_rate"}, "order": {"desc"}})

	require.NotEmpty(t, body.Tests)
	assert.Equal(t, "//broken:test", body.Tests[0].ProcedureRef)
}

func TestAnalyticsSortByErrorRate(t *testing.T) {
	f := seedAnalyticsFixture(t)
	body := f.get(t, url.Values{"sort": {"error_rate"}, "order": {"desc"}})

	require.NotEmpty(t, body.Tests)
	assert.Equal(t, "//infra:test", body.Tests[0].ProcedureRef)
}

// Ranking the clean tests by Wilson bound puts the well-evidenced one first,
// where the raw pass rate would tie them at 1.0.
func TestAnalyticsSortByPassRateLower(t *testing.T) {
	f := seedAnalyticsFixture(t)
	body := f.get(t, url.Values{"sort": {"pass_rate_lower"}, "order": {"desc"}})

	require.NotEmpty(t, body.Tests)
	assert.Equal(t, "//stable:test", body.Tests[0].ProcedureRef)

	stable := byProcedure(t, body, "//stable:test")
	sparse := byProcedure(t, body, "//sparse:test")
	assert.Greater(t, stable.PassRateLower, sparse.PassRateLower)
}

func TestAnalyticsPagingWindowsTheSortedSet(t *testing.T) {
	f := seedAnalyticsFixture(t)

	all := f.get(t, url.Values{"sort": {"procedure_ref"}})
	require.Len(t, all.Tests, 6)

	page := f.get(t, url.Values{"sort": {"procedure_ref"}, "limit": {"2"}, "offset": {"2"}})
	assert.Equal(t, 6, page.Total, "total describes the whole set, not the page")
	require.Len(t, page.Tests, 2)
	assert.Equal(t, all.Tests[2].ProcedureRef, page.Tests[0].ProcedureRef)
	assert.Equal(t, all.Tests[3].ProcedureRef, page.Tests[1].ProcedureRef)
}

func TestAnalyticsOffsetPastEndReturnsEmptySet(t *testing.T) {
	f := seedAnalyticsFixture(t)
	body := f.get(t, url.Values{"offset": {"500"}})

	assert.Equal(t, 6, body.Total)
	assert.Empty(t, body.Tests)
	assert.NotNil(t, body.Tests, "an empty page is [] rather than null")
}

func TestAnalyticsThresholdsAreRequestParameters(t *testing.T) {
	f := seedAnalyticsFixture(t)

	body := f.get(t, url.Values{"min_runs": {"25"}})
	assert.Equal(t, 25, body.Thresholds.MinRuns)

	// Nothing in the fixture reaches 25 verdicts, so everything is now sparse.
	stable := byProcedure(t, body, "//stable:test")
	assert.Equal(t, []string{analytics.LabelSparse}, stable.Labels)
}

func TestAnalyticsRespectsEvidenceFilters(t *testing.T) {
	f := seedAnalyticsFixture(t)

	body := f.get(t, url.Values{"procedure_ref": {"//stable:test"}})
	require.Len(t, body.Tests, 1)
	assert.Equal(t, "//stable:test", body.Tests[0].ProcedureRef)

	// Regex form, shared with the list endpoint.
	body = f.get(t, url.Values{"procedure_ref": {"~^//(stable|broken):"}})
	assert.Len(t, body.Tests, 2)
}

func TestAnalyticsTimeWindowNarrowsTheCounts(t *testing.T) {
	f := seedAnalyticsFixture(t)

	// The first ten records are all //stable:test.
	body := f.get(t, url.Values{
		"finished_before": {fixtureBase.Add(10 * time.Minute).Format(time.RFC3339)},
	})
	require.Len(t, body.Tests, 1)
	assert.Equal(t, "//stable:test", body.Tests[0].ProcedureRef)
	assert.Equal(t, 10, body.Tests[0].Counts.Pass)
}

func TestAnalyticsGroupByEvidenceType(t *testing.T) {
	f := seedAnalyticsFixture(t)

	body := f.get(t, url.Values{"group_by": {"evidence_type"}})
	require.NotEmpty(t, body.Tests)
	for _, s := range body.Tests {
		assert.Equal(t, "bazel", s.EvidenceType)
	}
}

func TestAnalyticsRejectsUnknownSortKey(t *testing.T) {
	f := seedAnalyticsFixture(t)
	resp := getJSON(t, "/api/v1/analytics/tests?repo="+url.QueryEscape(f.repo)+"&sort=metadata")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAnalyticsRejectsInvalidThreshold(t *testing.T) {
	f := seedAnalyticsFixture(t)
	for _, param := range []string{"flip_rate=2", "error_rate=-1", "min_runs=abc"} {
		resp := getJSON(t, "/api/v1/analytics/tests?repo="+url.QueryEscape(f.repo)+"&"+param)
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, param)
	}
}

func TestAnalyticsRejectsInvalidFilter(t *testing.T) {
	resp := getJSON(t, "/api/v1/analytics/tests?result=BOGUS")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Summary
// ---------------------------------------------------------------------------

type analyticsSummaryResponse struct {
	Runs      int64   `json:"runs"`
	Pass      int64   `json:"pass"`
	Fail      int64   `json:"fail"`
	Error     int64   `json:"error"`
	Skipped   int64   `json:"skipped"`
	Tests     int64   `json:"tests"`
	Repos     int64   `json:"repos"`
	Commits   int64   `json:"commits"`
	FailRate  float64 `json:"fail_rate"`
	ErrorRate float64 `json:"error_rate"`
}

func TestAnalyticsSummary(t *testing.T) {
	f := seedAnalyticsFixture(t)

	resp := getJSON(t, "/api/v1/analytics/summary?repo="+url.QueryEscape(f.repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeJSON[analyticsSummaryResponse](t, resp)

	assert.Equal(t, int64(103), body.Runs)
	assert.Equal(t, int64(62), body.Pass)
	assert.Equal(t, int64(31), body.Fail)
	assert.Equal(t, int64(10), body.Error)
	assert.Equal(t, int64(0), body.Skipped)
	assert.Equal(t, int64(6), body.Tests)
	assert.Equal(t, int64(1), body.Repos)
	assert.Equal(t, int64(102), body.Commits)

	assert.InDelta(t, 31.0/93.0, body.FailRate, 1e-9)
	assert.InDelta(t, 10.0/103.0, body.ErrorRate, 1e-9)
}

func TestAnalyticsSummaryEmptyWindow(t *testing.T) {
	resp := getJSON(t, "/api/v1/analytics/summary?repo=analytics/does-not-exist")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeJSON[analyticsSummaryResponse](t, resp)

	assert.Zero(t, body.Runs)
	assert.Zero(t, body.Tests)
	assert.InDelta(t, 0, body.FailRate, 1e-9)
}
