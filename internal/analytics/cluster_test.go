package analytics

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func test(name string) TestID { return TestID{Repo: "org/repo", ProcedureRef: name} }

// fail builds the occurrence list from a compact "test: runs" description.
func fail(spec map[string][]string) []Occurrence {
	var out []Occurrence
	for procedure, runs := range spec {
		for _, run := range runs {
			out = append(out, Occurrence{Test: test(procedure), Run: run})
		}
	}
	return out
}

func coverNames(steps []CoverStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Test.ProcedureRef
	}
	return out
}

func clusterNames(c Cluster) []string {
	out := make([]string, len(c.Members))
	for i, m := range c.Members {
		out[i] = m.ProcedureRef
	}
	return out
}

// ---------------------------------------------------------------------------
// Matrix and similarity
// ---------------------------------------------------------------------------

func TestMatrixCountsDistinctTestsAndRuns(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2"},
		"//b": {"r2", "r3"},
	}))

	assert.Equal(t, 2, m.Tests())
	assert.Equal(t, 3, m.Runs())
}

// The same test failing twice in one run is one failing run, not two.
func TestMatrixDeduplicatesOccurrences(t *testing.T) {
	m := NewMatrix([]Occurrence{
		{Test: test("//a"), Run: "r1"},
		{Test: test("//a"), Run: "r1"},
	})

	assert.Equal(t, 1, m.Tests())
	assert.Equal(t, 1, m.Runs())
	assert.Equal(t, 1, m.FailingRuns())
}

func TestJaccardIdenticalSetsIsOne(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2", "r3"},
		"//b": {"r1", "r2", "r3"},
	}))
	assertClose(t, 1.0, m.Jaccard(0, 1))
}

func TestJaccardDisjointSetsIsZero(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2"},
		"//b": {"r3", "r4"},
	}))
	assertClose(t, 0, m.Jaccard(0, 1))
}

func TestJaccardPartialOverlap(t *testing.T) {
	// intersection {r2}, union {r1,r2,r3} -> 1/3
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2"},
		"//b": {"r2", "r3"},
	}))
	assertClose(t, 1.0/3.0, m.Jaccard(0, 1))
}

func TestJaccardIsSymmetric(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2", "r5"},
		"//b": {"r2", "r3", "r4"},
	}))
	assertClose(t, m.Jaccard(0, 1), m.Jaccard(1, 0))
}

// Bitsets are chunked into 64-bit words, so a set spanning several words is
// where an off-by-one in the word arithmetic would show up.
func TestJaccardAcrossWordBoundaries(t *testing.T) {
	var aRuns, bRuns []string
	for i := range 200 {
		aRuns = append(aRuns, fmt.Sprintf("r%03d", i))
		if i >= 100 {
			bRuns = append(bRuns, fmt.Sprintf("r%03d", i))
		}
	}
	m := NewMatrix(fail(map[string][]string{"//a": aRuns, "//b": bRuns}))

	// intersection 100, union 200
	assertClose(t, 0.5, m.Jaccard(0, 1))
}

// ---------------------------------------------------------------------------
// Clustering
// ---------------------------------------------------------------------------

func TestClusterGroupsTestsThatFailTogether(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		// Two tests that always fail together.
		"//net:a": {"r1", "r2", "r3"},
		"//net:b": {"r1", "r2", "r3"},
		// One that fails on its own.
		"//db:c": {"r4", "r5"},
	}))

	clusters := m.Cluster(0.6)
	require.Len(t, clusters, 1, "only the co-failing pair forms a cluster")
	assert.ElementsMatch(t, []string{"//net:a", "//net:b"}, clusterNames(clusters[0]))
	assert.Equal(t, 3, clusters[0].CoversRuns)
	assertClose(t, 1.0, clusters[0].Cohesion)
}

// Single-link: a-b and b-c are each above threshold while a-c is not, and the
// chain still forms one cluster.
func TestClusterIsSingleLink(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2", "r3", "r4"},
		"//b": {"r3", "r4", "r5", "r6"},
		"//c": {"r5", "r6", "r7", "r8"},
	}))

	assert.Less(t, m.Jaccard(0, 2), 0.3, "the ends are not similar to each other")

	clusters := m.Cluster(0.3)
	require.Len(t, clusters, 1)
	assert.Len(t, clusters[0].Members, 3)
}

func TestClusterThresholdSeparatesGroups(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2", "r3", "r4"},
		"//b": {"r3", "r4", "r5", "r6"},
	}))

	// intersection 2, union 6 -> 1/3
	assertClose(t, 1.0/3.0, m.Jaccard(0, 1))

	assert.Len(t, m.Cluster(0.3), 1, "below the similarity, they group")
	assert.Empty(t, m.Cluster(0.4), "above it, neither has a partner")
}

func TestClusterIgnoresSolitaryTests(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1"},
		"//b": {"r2"},
		"//c": {"r3"},
	}))
	assert.Empty(t, m.Cluster(0.5), "a cluster of one is not a cluster")
}

func TestClusterOrderingIsDeterministic(t *testing.T) {
	spec := map[string][]string{
		"//x:1": {"r1", "r2"}, "//x:2": {"r1", "r2"},
		"//y:1": {"r3", "r4", "r5"}, "//y:2": {"r3", "r4", "r5"},
	}

	first := NewMatrix(fail(spec)).Cluster(0.6)
	for range 5 {
		again := NewMatrix(fail(spec)).Cluster(0.6)
		require.Len(t, again, len(first))
		for i := range first {
			assert.Equal(t, clusterNames(first[i]), clusterNames(again[i]))
		}
	}

	// Biggest coverage first, so the most valuable cluster leads.
	assert.Equal(t, 3, first[0].CoversRuns)
}

// ---------------------------------------------------------------------------
// Greedy cover
// ---------------------------------------------------------------------------

func TestGreedyCoverPicksTheBiggestContributorFirst(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//wide":   {"r1", "r2", "r3", "r4"},
		"//narrow": {"r5"},
	}))

	steps := m.GreedyCover()
	require.Len(t, steps, 2)
	assert.Equal(t, "//wide", steps[0].Test.ProcedureRef)
	assert.Equal(t, 4, steps[0].NewRuns)
	assert.Equal(t, 4, steps[0].Cumulative)
	assertClose(t, 0.8, steps[0].Coverage)

	assert.Equal(t, 1, steps[1].NewRuns)
	assert.Equal(t, 5, steps[1].Cumulative)
	assertClose(t, 1.0, steps[1].Coverage)
}

// The point of the whole exercise: tests that always fail together are
// redundant, and only one of them needs to be in the selected set.
func TestGreedyCoverCollapsesRedundantTests(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//dup:a": {"r1", "r2", "r3"},
		"//dup:b": {"r1", "r2", "r3"},
		"//dup:c": {"r1", "r2", "r3"},
		"//other": {"r4"},
	}))

	steps := m.GreedyCover()
	require.Len(t, steps, 2, "three identical tests contribute once between them")
	assertClose(t, 1.0, steps[len(steps)-1].Coverage)
}

func TestGreedyCoverReachesFullCoverage(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2"},
		"//b": {"r2", "r3"},
		"//c": {"r3", "r4"},
		"//d": {"r9"},
	}))

	steps := m.GreedyCover()
	require.NotEmpty(t, steps)

	last := steps[len(steps)-1]
	assert.Equal(t, m.FailingRuns(), last.Cumulative)
	assertClose(t, 1.0, last.Coverage)
}

// Every step must add something, or the list would suggest running tests that
// catch nothing new.
func TestGreedyCoverStepsAlwaysContribute(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2", "r3"},
		"//b": {"r1", "r2"},
		"//c": {"r3"},
		"//d": {"r1"},
	}))

	for _, s := range m.GreedyCover() {
		assert.Positive(t, s.NewRuns, "%s adds nothing", s.Test.ProcedureRef)
	}
}

func TestGreedyCoverCumulativeIsMonotonic(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//a": {"r1", "r2", "r3", "r4"},
		"//b": {"r4", "r5", "r6"},
		"//c": {"r7"},
		"//d": {"r1", "r7"},
	}))

	steps := m.GreedyCover()
	prev := 0
	for _, s := range steps {
		assert.Greater(t, s.Cumulative, prev)
		prev = s.Cumulative
	}
}

// Greedy is an approximation, not an optimum, and this pins that so nobody
// reads the output as a guaranteed minimum.
//
// half:a and half:b partition all eight runs, so two tests suffice. The bait
// overlaps both and is larger than either, so greedy takes it first and is then
// left with leftovers on both sides that no single test covers — three tests to
// do a two-test job.
func TestGreedyCoverIsApproximateNotOptimal(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{
		"//greedy-bait": {"r1", "r2", "r3", "r5", "r6"},
		"//half:a":      {"r1", "r2", "r3", "r4"},
		"//half:b":      {"r5", "r6", "r7", "r8"},
	}))

	steps := m.GreedyCover()
	assert.Equal(t, "//greedy-bait", steps[0].Test.ProcedureRef)
	assert.Len(t, steps, 3, "greedy needs three; half:a + half:b alone would have covered all eight")
	assertClose(t, 1.0, steps[len(steps)-1].Coverage)
}

func TestGreedyCoverTieBreakIsDeterministic(t *testing.T) {
	spec := map[string][]string{
		"//c": {"r1"},
		"//a": {"r2"},
		"//b": {"r3"},
	}

	first := coverNames(NewMatrix(fail(spec)).GreedyCover())
	assert.Equal(t, []string{"//a", "//b", "//c"}, first, "equal contributions break by identity")

	for range 5 {
		assert.Equal(t, first, coverNames(NewMatrix(fail(spec)).GreedyCover()))
	}
}

// ---------------------------------------------------------------------------
// Degenerate input
// ---------------------------------------------------------------------------

func TestEmptyMatrix(t *testing.T) {
	m := NewMatrix(nil)

	assert.Zero(t, m.Tests())
	assert.Zero(t, m.Runs())
	assert.Zero(t, m.FailingRuns())
	assert.Empty(t, m.Cluster(0.5))
	assert.Empty(t, m.GreedyCover())
	assert.NotNil(t, m.GreedyCover(), "an empty cover is [] rather than null")
}

func TestSingleTestMatrix(t *testing.T) {
	m := NewMatrix(fail(map[string][]string{"//only": {"r1", "r2"}}))

	assert.Empty(t, m.Cluster(0.5))
	steps := m.GreedyCover()
	require.Len(t, steps, 1)
	assertClose(t, 1.0, steps[0].Coverage)
}

func TestMatrixSeparatesIdenticalProceduresInDifferentRepos(t *testing.T) {
	m := NewMatrix([]Occurrence{
		{Test: TestID{Repo: "org/one", ProcedureRef: "//same"}, Run: "r1"},
		{Test: TestID{Repo: "org/two", ProcedureRef: "//same"}, Run: "r1"},
	})
	assert.Equal(t, 2, m.Tests())
}
