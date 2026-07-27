package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/analytics"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
)

// ---------------------------------------------------------------------------
// Fixture
//
// Nineteen runs. Two tests always fail together (a shared root cause), one
// fails independently, one never fails at all, and eight of the runs are
// infrastructure outages that error every test at once.
//
// The outages deliberately outnumber the genuine failing runs, because that is
// what turns "counting errors as failures" from mild noise into a collapse of
// the whole suite into one cluster.
// ---------------------------------------------------------------------------

type clusterFixture struct {
	repo string
}

func seedClusterFixture(t *testing.T) clusterFixture {
	t.Helper()

	repo := fmt.Sprintf("clusters/%d", time.Now().UnixNano())
	var records []model.EvidenceCreate

	minute := 0
	add := func(procedure, invocation string, result model.EvidenceResult) {
		metadata, err := json.Marshal(map[string]any{"invocation_id": invocation})
		require.NoError(t, err)
		records = append(records, model.EvidenceCreate{
			Repo:         repo,
			Branch:       "main",
			RCSRef:       "commit-" + invocation,
			ProcedureRef: procedure,
			EvidenceType: "bazel",
			Source:       "ci",
			Result:       result,
			FinishedAt:   model.FlexibleTime{Time: fixtureBase.Add(time.Duration(minute) * time.Minute)},
			Metadata:     metadata,
		})
		minute++
	}

	// Runs 1-4: //net:a and //net:b fail together, nothing else does.
	for i := 1; i <= 4; i++ {
		run := fmt.Sprintf("run%02d", i)
		add("//net:a", run, model.ResultFail)
		add("//net:b", run, model.ResultFail)
		add("//db:c", run, model.ResultPass)
		add("//clean:d", run, model.ResultPass)
	}

	// Runs 5-7: //db:c fails alone.
	for i := 5; i <= 7; i++ {
		run := fmt.Sprintf("run%02d", i)
		add("//net:a", run, model.ResultPass)
		add("//net:b", run, model.ResultPass)
		add("//db:c", run, model.ResultFail)
		add("//clean:d", run, model.ResultPass)
	}

	// Runs 8-11: everything passes.
	for i := 8; i <= 11; i++ {
		run := fmt.Sprintf("run%02d", i)
		for _, p := range []string{"//net:a", "//net:b", "//db:c", "//clean:d"} {
			add(p, run, model.ResultPass)
		}
	}

	// Runs 12-19: infrastructure outages. Every test errors in the same run, so
	// counting errors as failures makes every pair look correlated. There are
	// more outage runs than genuine failing runs, which is what turns the
	// distortion from noise into a suite-wide collapse.
	for i := 12; i <= 19; i++ {
		run := fmt.Sprintf("run%02d", i)
		for _, p := range []string{"//net:a", "//net:b", "//db:c", "//clean:d"} {
			add(p, run, model.ResultError)
		}
	}

	_, err := testEvidenceStore.InsertBatch(context.Background(), records)
	require.NoError(t, err)

	return clusterFixture{repo: repo}
}

type clustersResponse struct {
	RunKey        string                `json:"run_key"`
	IncludeErrors bool                  `json:"include_errors"`
	Threshold     float64               `json:"threshold"`
	Tests         int                   `json:"tests"`
	FailingRuns   int                   `json:"failing_runs"`
	Clusters      []analytics.Cluster   `json:"clusters"`
	MinimalSet    []analytics.CoverStep `json:"minimal_set"`
}

func (f clusterFixture) clusters(t *testing.T, extra url.Values) clustersResponse {
	t.Helper()
	q := url.Values{"repo": {f.repo}}
	for k, vs := range extra {
		q[k] = vs
	}
	resp := getJSON(t, "/api/v1/analytics/clusters?"+q.Encode())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSON[clustersResponse](t, resp)
}

func memberNames(c analytics.Cluster) []string {
	out := make([]string, len(c.Members))
	for i, m := range c.Members {
		out[i] = m.ProcedureRef
	}
	return out
}

func coverProcedures(steps []analytics.CoverStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Test.ProcedureRef
	}
	return out
}

// ---------------------------------------------------------------------------

func TestClustersGroupsTestsThatFailTogether(t *testing.T) {
	f := seedClusterFixture(t)
	body := f.clusters(t, nil)

	assert.Equal(t, "auto", body.RunKey)
	assert.False(t, body.IncludeErrors)
	assert.Equal(t, 3, body.Tests, "only tests that failed at least once")
	assert.Equal(t, 7, body.FailingRuns, "runs 1-7")

	require.Len(t, body.Clusters, 1)
	assert.ElementsMatch(t, []string{"//net:a", "//net:b"}, memberNames(body.Clusters[0]))
	assert.Equal(t, 4, body.Clusters[0].CoversRuns)
	assert.InDelta(t, 1.0, body.Clusters[0].Cohesion, 1e-9)
}

// The question the issue actually asks: how few tests still catch the failures.
func TestClustersMinimalSetCollapsesRedundancy(t *testing.T) {
	f := seedClusterFixture(t)
	body := f.clusters(t, nil)

	// //net:a and //net:b are redundant with each other, so the cover needs one
	// of them plus //db:c — two tests for all seven failing runs.
	require.Len(t, body.MinimalSet, 2)
	assert.InDelta(t, 1.0, body.MinimalSet[1].Coverage, 1e-9)
	assert.Equal(t, 7, body.MinimalSet[1].Cumulative)

	assert.Contains(t, coverProcedures(body.MinimalSet), "//db:c")
	assert.Subset(t, []string{"//net:a", "//net:b"}, coverProcedures(body.MinimalSet)[:1])

	// The bigger contributor leads.
	assert.Equal(t, 4, body.MinimalSet[0].NewRuns)
	assert.Equal(t, 3, body.MinimalSet[1].NewRuns)
}

// An outage errors every test in one run at once. Folding that in would make
// every pair look perfectly correlated and collapse the suite into one
// meaningless cluster, so ERROR is excluded unless asked for.
func TestClustersExcludeErrorsByDefault(t *testing.T) {
	f := seedClusterFixture(t)

	byDefault := f.clusters(t, nil)
	require.Len(t, byDefault.Clusters, 1)
	assert.Len(t, byDefault.Clusters[0].Members, 2)

	withErrors := f.clusters(t, url.Values{"include_errors": {"true"}})
	assert.True(t, withErrors.IncludeErrors)
	assert.Equal(t, 15, withErrors.FailingRuns, "the eight outage runs now count")

	// The outages fuse the whole suite into a single cluster, including
	// //clean:d, which never actually failed. That collapse is the reason
	// errors are excluded by default.
	require.Len(t, withErrors.Clusters, 1)
	assert.ElementsMatch(t,
		[]string{"//net:a", "//net:b", "//db:c", "//clean:d"},
		memberNames(withErrors.Clusters[0]),
		"counting infrastructure errors as failures makes every test look correlated with every other")
}

func TestClustersThresholdControlsGrouping(t *testing.T) {
	f := seedClusterFixture(t)

	assert.Len(t, f.clusters(t, url.Values{"threshold": {"1"}}).Clusters, 1,
		"//net:a and //net:b are perfectly correlated, so even 1.0 groups them")

	// //db:c never fails in the same run as the net pair, so nothing can join it.
	loose := f.clusters(t, url.Values{"threshold": {"0.01"}})
	require.Len(t, loose.Clusters, 1)
	assert.Len(t, loose.Clusters[0].Members, 2)
}

func TestClustersRunKeySelectsGrouping(t *testing.T) {
	f := seedClusterFixture(t)

	for _, key := range []string{"auto", "invocation", "commit"} {
		body := f.clusters(t, url.Values{"run_key": {key}})
		assert.Equal(t, key, body.RunKey)
		assert.Equal(t, 7, body.FailingRuns,
			"the fixture sets one invocation per commit, so all three agree (%s)", key)
	}
}

func TestClustersRejectsUnknownRunKey(t *testing.T) {
	f := seedClusterFixture(t)
	resp := getJSON(t, "/api/v1/analytics/clusters?repo="+url.QueryEscape(f.repo)+"&run_key=nonsense")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestClustersRejectsInvalidThreshold(t *testing.T) {
	f := seedClusterFixture(t)
	for _, v := range []string{"2", "-1", "abc"} {
		resp := getJSON(t, "/api/v1/analytics/clusters?repo="+url.QueryEscape(f.repo)+"&threshold="+v)
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, v)
	}
}

func TestClustersRespectsEvidenceFilters(t *testing.T) {
	f := seedClusterFixture(t)

	body := f.clusters(t, url.Values{"procedure_ref": {"~^//net:"}})
	assert.Equal(t, 2, body.Tests)
	assert.Equal(t, 4, body.FailingRuns)
	require.Len(t, body.MinimalSet, 1, "one of the pair covers everything they catch")
}

func TestClustersEmptyWindow(t *testing.T) {
	resp := getJSON(t, "/api/v1/analytics/clusters?repo=clusters/does-not-exist")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeJSON[clustersResponse](t, resp)

	assert.Zero(t, body.Tests)
	assert.Zero(t, body.FailingRuns)
	assert.Empty(t, body.Clusters)
	assert.Empty(t, body.MinimalSet)
	assert.NotNil(t, body.MinimalSet, "an empty cover is [] rather than null")
}

// A query too big to analyse is refused rather than truncated: a coverage
// percentage computed from part of the failures looks exactly like an answer.
func TestClustersRefuseOversizedQueries(t *testing.T) {
	f := seedClusterFixture(t)

	_, err := testEvidenceStore.FailureOccurrences(context.Background(), store.FailureOccurrenceParams{
		Filter:  model.EvidenceFilter{Repo: &f.repo},
		MaxRows: 1,
	})
	require.Error(t, err)

	var tooMany *store.ErrTooManyRows
	require.ErrorAs(t, err, &tooMany)
	assert.Equal(t, 1, tooMany.Max)
	assert.Contains(t, err.Error(), "narrow the filter")
}

func TestClustersUnderTheCapSucceed(t *testing.T) {
	f := seedClusterFixture(t)

	occurrences, err := testEvidenceStore.FailureOccurrences(context.Background(), store.FailureOccurrenceParams{
		Filter:  model.EvidenceFilter{Repo: &f.repo},
		MaxRows: 1000,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, analytics.NewMatrix(occurrences).Tests())
}

// Runs are namespaced by repo, so a commit hash reused across repos does not
// fuse two unrelated runs into one.
func TestClustersDoNotFuseRunsAcrossRepos(t *testing.T) {
	shared := fmt.Sprintf("shared-%d", time.Now().UnixNano())
	repoA := shared + "/a"
	repoB := shared + "/b"

	var records []model.EvidenceCreate
	for i, repo := range []string{repoA, repoB} {
		records = append(records, model.EvidenceCreate{
			Repo:         repo,
			Branch:       "main",
			RCSRef:       "same-commit",
			ProcedureRef: fmt.Sprintf("//p%d", i),
			EvidenceType: "bazel",
			Source:       "ci",
			Result:       model.ResultFail,
			FinishedAt:   model.FlexibleTime{Time: fixtureBase},
		})
	}
	_, err := testEvidenceStore.InsertBatch(context.Background(), records)
	require.NoError(t, err)

	resp := getJSON(t, "/api/v1/analytics/clusters?repo=~^"+url.QueryEscape(shared))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeJSON[clustersResponse](t, resp)

	assert.Equal(t, 2, body.Tests)
	assert.Equal(t, 2, body.FailingRuns, "same commit hash, two different repos, two runs")
	assert.Empty(t, body.Clusters, "they never failed in the same run")
}
