package analytics

import (
	"cmp"
	"math/bits"
	"slices"
)

// TestID identifies a test across the analytics package.
type TestID struct {
	Repo         string `json:"repo"`
	ProcedureRef string `json:"procedure_ref"`
}

func compareTestID(a, b TestID) int {
	return cmp.Or(
		cmp.Compare(a.ProcedureRef, b.ProcedureRef),
		cmp.Compare(a.Repo, b.Repo),
	)
}

// Occurrence records that a test failed in a particular run. The store produces
// one of these per failing row; duplicates are harmless.
type Occurrence struct {
	Test TestID
	Run  string
}

// Matrix is a co-failure incidence matrix: for each test, the set of runs in
// which it failed, held as a bitset.
//
// Only failing rows ever enter the matrix, so it is far smaller than the
// evidence it was derived from, and every operation below is a bitset scan
// rather than a query.
type Matrix struct {
	tests []TestID
	runs  int
	words int
	// bits[t] is the run set of test t, one bit per run index.
	bits [][]uint64
}

// NewMatrix builds the incidence matrix. Tests and runs are indexed in sorted
// order so that every result derived from the matrix is deterministic.
func NewMatrix(occurrences []Occurrence) *Matrix {
	testIndex := map[TestID]int{}
	runIndex := map[string]int{}
	var tests []TestID
	var runs []string

	for _, o := range occurrences {
		if _, ok := testIndex[o.Test]; !ok {
			testIndex[o.Test] = 0
			tests = append(tests, o.Test)
		}
		if _, ok := runIndex[o.Run]; !ok {
			runIndex[o.Run] = 0
			runs = append(runs, o.Run)
		}
	}

	slices.SortFunc(tests, compareTestID)
	slices.Sort(runs)
	for i, t := range tests {
		testIndex[t] = i
	}
	for i, r := range runs {
		runIndex[r] = i
	}

	m := &Matrix{
		tests: tests,
		runs:  len(runs),
		words: (len(runs) + 63) / 64,
	}
	m.bits = make([][]uint64, len(tests))
	for i := range m.bits {
		m.bits[i] = make([]uint64, m.words)
	}

	for _, o := range occurrences {
		r := runIndex[o.Run]
		m.bits[testIndex[o.Test]][r/64] |= 1 << (r % 64)
	}

	return m
}

// Tests is the number of distinct tests that failed at least once.
func (m *Matrix) Tests() int { return len(m.tests) }

// Runs is the number of distinct runs represented.
func (m *Matrix) Runs() int { return m.runs }

// FailingRuns is the coverage denominator. Every run in the matrix contains at
// least one failure by construction, so this equals Runs; it is named separately
// because that is the meaning it carries in the cover results.
func (m *Matrix) FailingRuns() int { return m.runs }

// TestAt returns the test at index i.
func (m *Matrix) TestAt(i int) TestID { return m.tests[i] }

// Jaccard is the similarity of two tests' failure sets: the share of the runs
// where either failed in which both failed. 1 means they always fail together,
// 0 means they never do.
func (m *Matrix) Jaccard(i, j int) float64 {
	var intersection, union int
	for w := range m.words {
		a, b := m.bits[i][w], m.bits[j][w]
		intersection += bits.OnesCount64(a & b)
		union += bits.OnesCount64(a | b)
	}
	return ratio(intersection, union)
}

// Cluster is a group of tests that tend to fail together.
type Cluster struct {
	ID      int      `json:"id"`
	Size    int      `json:"size"`
	Members []TestID `json:"members"`
	// CoversRuns is how many failing runs the cluster catches between its
	// members — the reason to care about it.
	CoversRuns int `json:"covers_runs"`
	// Cohesion is the mean pairwise similarity inside the cluster. A chain
	// linked end to end scores lower than a group that all fail together.
	Cohesion float64 `json:"cohesion"`
}

// Cluster groups tests whose failure sets are at least `threshold` similar.
//
// This is single-link agglomerative clustering, done as a union-find over the
// pairs above the threshold: A and C land in the same cluster if each is similar
// to B, even when they are not similar to each other. That is the right shape
// for "these fail for the same underlying reason", and it is why Cohesion is
// reported — a long chain is a weaker claim than a tight group.
//
// Solitary tests are omitted: a cluster of one says nothing.
func (m *Matrix) Cluster(threshold float64) []Cluster {
	parent := make([]int, len(m.tests))
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}

	for i := range m.tests {
		for j := i + 1; j < len(m.tests); j++ {
			if m.Jaccard(i, j) >= threshold {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			}
		}
	}

	groups := map[int][]int{}
	for i := range m.tests {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	clusters := make([]Cluster, 0, len(groups))
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}

		union := make([]uint64, m.words)
		for _, i := range members {
			for w := range m.words {
				union[w] |= m.bits[i][w]
			}
		}
		covers := 0
		for _, w := range union {
			covers += bits.OnesCount64(w)
		}

		var total float64
		var pairs int
		for a := range members {
			for b := a + 1; b < len(members); b++ {
				total += m.Jaccard(members[a], members[b])
				pairs++
			}
		}

		ids := make([]TestID, len(members))
		for i, idx := range members {
			ids[i] = m.tests[idx]
		}
		slices.SortFunc(ids, compareTestID)

		clusters = append(clusters, Cluster{
			Size:       len(ids),
			Members:    ids,
			CoversRuns: covers,
			Cohesion:   ratioFloat(total, pairs),
		})
	}

	// Most valuable first, with a deterministic tie-break.
	slices.SortFunc(clusters, func(a, b Cluster) int {
		return cmp.Or(
			cmp.Compare(b.CoversRuns, a.CoversRuns),
			compareTestID(a.Members[0], b.Members[0]),
		)
	})
	for i := range clusters {
		clusters[i].ID = i + 1
	}

	return clusters
}

// CoverStep is one entry in the minimal covering set, in the order chosen.
type CoverStep struct {
	Test TestID `json:"test"`
	// NewRuns is how many failing runs this test catches that no earlier test in
	// the list already caught. Always positive.
	NewRuns int `json:"new_runs"`
	// Cumulative and Coverage describe the set up to and including this step,
	// which is what makes the list readable as "these N tests catch X%".
	Cumulative int     `json:"cumulative"`
	Coverage   float64 `json:"coverage"`
}

// GreedyCover selects tests in the order that catches the most failing runs
// soonest: repeatedly take the test covering the most runs not yet covered.
//
// This is the classic greedy set-cover approximation. It is *not* guaranteed to
// find the smallest possible set — set cover is NP-hard, and greedy can be beaten
// on constructed inputs — but it is within a ln(n) factor and it produces the
// ranked list the decision actually needs.
//
// Only steps that contribute are returned, so no entry in the list is redundant.
func (m *Matrix) GreedyCover() []CoverStep {
	steps := []CoverStep{}
	if len(m.tests) == 0 {
		return steps
	}

	covered := make([]uint64, m.words)
	// Once a test contributes nothing it never can again, because the covered set
	// only grows. Dropping it keeps later passes cheap.
	active := make([]int, len(m.tests))
	for i := range active {
		active[i] = i
	}

	cumulative := 0
	for len(active) > 0 {
		best, bestGain := -1, 0
		remaining := active[:0]

		for _, i := range active {
			gain := 0
			for w := range m.words {
				gain += bits.OnesCount64(m.bits[i][w] &^ covered[w])
			}
			if gain == 0 {
				continue
			}
			remaining = append(remaining, i)
			// Tests are indexed in identity order, so ">" keeps the first of any
			// tie and the choice is reproducible.
			if gain > bestGain {
				best, bestGain = i, gain
			}
		}
		active = remaining

		if best < 0 {
			break
		}

		for w := range m.words {
			covered[w] |= m.bits[best][w]
		}
		cumulative += bestGain

		steps = append(steps, CoverStep{
			Test:       m.tests[best],
			NewRuns:    bestGain,
			Cumulative: cumulative,
			Coverage:   ratio(cumulative, m.runs),
		})
	}

	return steps
}

func ratioFloat(numerator float64, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return numerator / float64(denominator)
}
