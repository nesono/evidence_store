package analytics

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// TestStats is one row of the analytics table: a single test's record over the
// query window. The store fills in the identity, Counts, and timestamps; the
// derived fields come from Finalize, so every rate has exactly one definition.
type TestStats struct {
	Repo         string `json:"repo"`
	ProcedureRef string `json:"procedure_ref"`
	// EvidenceType is set only when the caller grouped by it.
	EvidenceType string `json:"evidence_type,omitempty"`

	Counts Counts `json:"counts"`

	Runs        int `json:"runs"`
	VerdictRuns int `json:"verdict_runs"`

	FailRate      float64 `json:"fail_rate"`
	ErrorRate     float64 `json:"error_rate"`
	FlipRate      float64 `json:"flip_rate"`
	PassRateLower float64 `json:"pass_rate_lower"`

	FirstSeen  time.Time  `json:"first_seen"`
	LastSeen   time.Time  `json:"last_seen"`
	LastPassAt *time.Time `json:"last_pass_at,omitempty"`
	LastFailAt *time.Time `json:"last_fail_at,omitempty"`
	LastResult string     `json:"last_result"`

	Labels []string `json:"labels"`
}

// Finalize computes the derived fields for every row in place.
func Finalize(stats []TestStats, th Thresholds) {
	for i := range stats {
		c := stats[i].Counts
		stats[i].Runs = c.Runs()
		stats[i].VerdictRuns = c.VerdictRuns()
		stats[i].FailRate = c.FailRate()
		stats[i].ErrorRate = c.ErrorRate()
		stats[i].FlipRate = c.FlipRate()
		stats[i].PassRateLower = c.PassRateLower()
		stats[i].Labels = th.Labels(c)
	}
}

// DefaultSortKey is neutral on purpose: the endpoint answers several different
// questions and none of them deserves to be the implicit one.
const DefaultSortKey = "procedure_ref"

// sortKeys whitelists the orderings the API exposes. Sorting happens here rather
// than in SQL because the rates are defined in Go, and having one of them
// computed two ways would be a bug waiting to happen.
var sortKeys = map[string]func(a, b TestStats) int{
	"procedure_ref":   func(a, b TestStats) int { return cmp.Compare(a.ProcedureRef, b.ProcedureRef) },
	"repo":            func(a, b TestStats) int { return cmp.Compare(a.Repo, b.Repo) },
	"runs":            func(a, b TestStats) int { return cmp.Compare(a.Runs, b.Runs) },
	"verdict_runs":    func(a, b TestStats) int { return cmp.Compare(a.VerdictRuns, b.VerdictRuns) },
	"pass":            func(a, b TestStats) int { return cmp.Compare(a.Counts.Pass, b.Counts.Pass) },
	"fail":            func(a, b TestStats) int { return cmp.Compare(a.Counts.Fail, b.Counts.Fail) },
	"error":           func(a, b TestStats) int { return cmp.Compare(a.Counts.Error, b.Counts.Error) },
	"skipped":         func(a, b TestStats) int { return cmp.Compare(a.Counts.Skipped, b.Counts.Skipped) },
	"flaky_commits":   func(a, b TestStats) int { return cmp.Compare(a.Counts.FlakyCommits, b.Counts.FlakyCommits) },
	"fail_rate":       func(a, b TestStats) int { return cmp.Compare(a.FailRate, b.FailRate) },
	"error_rate":      func(a, b TestStats) int { return cmp.Compare(a.ErrorRate, b.ErrorRate) },
	"flip_rate":       func(a, b TestStats) int { return cmp.Compare(a.FlipRate, b.FlipRate) },
	"pass_rate_lower": func(a, b TestStats) int { return cmp.Compare(a.PassRateLower, b.PassRateLower) },
	"first_seen":      func(a, b TestStats) int { return a.FirstSeen.Compare(b.FirstSeen) },
	"last_seen":       func(a, b TestStats) int { return a.LastSeen.Compare(b.LastSeen) },
}

// IsSortable reports whether key names an exposed ordering.
func IsSortable(key string) bool {
	_, ok := sortKeys[key]
	return ok
}

// Sort orders stats in place. An empty key selects DefaultSortKey.
//
// The tie-break is always ascending by identity, whichever direction the primary
// key runs, so that equal values get a stable order and paging through the table
// neither repeats nor skips a row.
func Sort(stats []TestStats, key string, desc bool) error {
	if key == "" {
		key = DefaultSortKey
	}
	compare, ok := sortKeys[key]
	if !ok {
		return fmt.Errorf("cannot sort by %q", key)
	}

	slices.SortStableFunc(stats, func(a, b TestStats) int {
		c := compare(a, b)
		if desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return cmp.Or(
			cmp.Compare(a.ProcedureRef, b.ProcedureRef),
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(a.EvidenceType, b.EvidenceType),
		)
	})
	return nil
}
