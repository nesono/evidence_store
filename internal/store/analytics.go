package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nesono/evidence-store/internal/analytics"
	"github.com/nesono/evidence-store/internal/model"
)

// DefaultMaxAnalyticsGroups caps how many distinct tests one aggregation may
// return. Aggregation collapses millions of rows into thousands of groups, and
// the caller then sorts and pages that set in memory — but only if it fits.
// Exceeding the cap is reported rather than silently truncated, because a
// truncated set produces confident-looking numbers computed from part of the
// data.
const DefaultMaxAnalyticsGroups = 50000

// ErrTooManyGroups is returned when an aggregation matches more distinct tests
// than the cap allows. The caller should narrow the filter or the time window.
type ErrTooManyGroups struct {
	Max int
}

func (e *ErrTooManyGroups) Error() string {
	return fmt.Sprintf("query matches more than %d distinct tests; narrow the filter or the time window", e.Max)
}

type TestStatsParams struct {
	Filter model.EvidenceFilter
	// GroupByEvidenceType splits a procedure into one row per evidence type,
	// for when the same procedure runs under several harnesses and their
	// reliability differs.
	GroupByEvidenceType bool
	// MaxGroups overrides DefaultMaxAnalyticsGroups when positive.
	MaxGroups int
}

// TestStats aggregates the filtered evidence into one row per test.
//
// Only the raw counts and timestamps are filled in; the rates and labels are
// derived by analytics.Finalize, so SQL never computes a metric that Go also
// knows how to compute.
func (s *EvidenceStore) TestStats(ctx context.Context, params TestStatsParams) ([]analytics.TestStats, error) {
	maxGroups := params.MaxGroups
	if maxGroups <= 0 {
		maxGroups = DefaultMaxAnalyticsGroups
	}

	// Fixed identifiers chosen by a bool, never interpolated from user input.
	groupCols := []string{"repo", "procedure_ref"}
	if params.GroupByEvidenceType {
		groupCols = append(groupCols, "evidence_type")
	}
	cols := strings.Join(groupCols, ", ")
	prefixed := func(alias string) string {
		out := make([]string, len(groupCols))
		for i, c := range groupCols {
			out[i] = alias + "." + c
		}
		return strings.Join(out, ", ")
	}

	f := buildFilter(params.Filter)
	where := f.whereClause()
	limit := f.arg(maxGroups + 1)

	// `filtered` is referenced by several branches below, so Postgres materialises
	// it and the filter scan happens exactly once.
	//
	// It projects the group columns and nothing more, so that the common grouping
	// stays within idx_evidence_analytics and the scan is index-only. Selecting
	// evidence_type unconditionally would force a heap fetch per row for every
	// caller, including the ones not grouping by it.
	query := fmt.Sprintf(`
WITH filtered AS (
    SELECT %[1]s, rcs_ref, result, finished_at, id
    FROM evidence%[2]s
),
agg AS (
    SELECT %[1]s,
        count(*) FILTER (WHERE result = 'PASS')::int    AS pass_count,
        count(*) FILTER (WHERE result = 'FAIL')::int    AS fail_count,
        count(*) FILTER (WHERE result = 'ERROR')::int   AS error_count,
        count(*) FILTER (WHERE result = 'SKIPPED')::int AS skipped_count,
        min(finished_at)                                AS first_seen,
        max(finished_at)                                AS last_seen,
        max(finished_at) FILTER (WHERE result = 'PASS') AS last_pass_at,
        max(finished_at) FILTER (WHERE result = 'FAIL') AS last_fail_at
    FROM filtered
    GROUP BY %[1]s
),
-- Consecutive verdict changes. ERROR and SKIPPED are dropped first so an
-- infrastructure blip between two passes does not read as two flips.
sequenced AS (
    SELECT %[1]s, result,
        lag(result) OVER (PARTITION BY %[1]s ORDER BY finished_at, id) AS prev
    FROM filtered
    WHERE result IN ('PASS', 'FAIL')
),
flips AS (
    SELECT %[1]s,
        count(*) FILTER (WHERE prev IS NOT NULL AND result <> prev)::int AS flips,
        count(*) FILTER (WHERE prev IS NOT NULL)::int                    AS transitions
    FROM sequenced
    GROUP BY %[1]s
),
-- Commits where the same test both passed and failed: same code, different
-- outcome, which is flakiness no matter what the flip rate says.
disagreements AS (
    SELECT %[1]s, count(*)::int AS flaky_commits
    FROM (
        SELECT %[1]s, rcs_ref
        FROM filtered
        GROUP BY %[1]s, rcs_ref
        HAVING count(*) FILTER (WHERE result = 'PASS') > 0
           AND count(*) FILTER (WHERE result = 'FAIL') > 0
    ) per_commit
    GROUP BY %[1]s
),
latest AS (
    SELECT DISTINCT ON (%[1]s) %[1]s, result AS last_result
    FROM filtered
    ORDER BY %[1]s, finished_at DESC, id DESC
)
SELECT %[3]s,
    a.pass_count, a.fail_count, a.error_count, a.skipped_count,
    COALESCE(f.flips, 0), COALESCE(f.transitions, 0), COALESCE(d.flaky_commits, 0),
    a.first_seen, a.last_seen, a.last_pass_at, a.last_fail_at, l.last_result
FROM agg a
LEFT JOIN flips f USING (%[1]s)
LEFT JOIN disagreements d USING (%[1]s)
LEFT JOIN latest l USING (%[1]s)
LIMIT %[4]s`, cols, where, prefixed("a"), limit)

	rows, err := s.pool.Query(ctx, query, f.args...)
	if err != nil {
		return nil, fmt.Errorf("query test stats: %w", err)
	}
	defer rows.Close()

	stats := make([]analytics.TestStats, 0)
	for rows.Next() {
		var (
			st                     analytics.TestStats
			lastPassAt, lastFailAt *time.Time
		)

		dest := []any{&st.Repo, &st.ProcedureRef}
		if params.GroupByEvidenceType {
			dest = append(dest, &st.EvidenceType)
		}
		dest = append(dest,
			&st.Counts.Pass, &st.Counts.Fail, &st.Counts.Error, &st.Counts.Skipped,
			&st.Counts.Flips, &st.Counts.Transitions, &st.Counts.FlakyCommits,
			&st.FirstSeen, &st.LastSeen, &lastPassAt, &lastFailAt, &st.LastResult,
		)

		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan test stats: %w", err)
		}

		st.FirstSeen = st.FirstSeen.UTC()
		st.LastSeen = st.LastSeen.UTC()
		st.LastPassAt = utcPtr(lastPassAt)
		st.LastFailAt = utcPtr(lastFailAt)

		stats = append(stats, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate test stats: %w", err)
	}

	if len(stats) > maxGroups {
		return nil, &ErrTooManyGroups{Max: maxGroups}
	}

	return stats, nil
}

// DefaultMaxFailureRows caps the failing rows one clustering request may pull
// into memory.
const DefaultMaxFailureRows = 200000

// ErrTooManyRows is returned when a clustering query matches more failing rows
// than the cap allows.
type ErrTooManyRows struct {
	Max int
}

func (e *ErrTooManyRows) Error() string {
	return fmt.Sprintf("query matches more than %d failing rows; narrow the filter or the time window", e.Max)
}

// RunKey selects how rows are grouped into "runs" for co-failure analysis.
// The schema has no run column, so the grouping is derived — and clustering
// quality depends entirely on getting it right, which is why it is explicit
// rather than assumed.
type RunKey string

const (
	// RunKeyAuto prefers the adapter's invocation id and falls back to the
	// commit for rows that lack one.
	RunKeyAuto RunKey = "auto"
	// RunKeyInvocation groups strictly by invocation id, ignoring rows without
	// one. Correct when every source sets it.
	RunKeyInvocation RunKey = "invocation"
	// RunKeyCommit groups by commit. Correct when a commit is tested once.
	RunKeyCommit RunKey = "commit"
)

// Valid reports whether k names a supported grouping.
func (k RunKey) Valid() bool {
	switch k {
	case RunKeyAuto, RunKeyInvocation, RunKeyCommit:
		return true
	}
	return false
}

// expr renders the run key as SQL. Fixed strings chosen by a validated enum,
// never interpolated from caller input.
func (k RunKey) expr() string {
	switch k {
	case RunKeyInvocation:
		return "metadata->>'invocation_id'"
	case RunKeyCommit:
		return "rcs_ref"
	default:
		return "COALESCE(metadata->>'invocation_id', rcs_ref)"
	}
}

type FailureOccurrenceParams struct {
	Filter model.EvidenceFilter
	RunKey RunKey
	// IncludeErrors folds ERROR in with FAIL.
	//
	// Off by default, and that default matters: an infrastructure outage fails
	// every test in the same run at once, which makes every pair of them look
	// perfectly correlated. Including errors would collapse the whole suite into
	// one meaningless cluster and bury the real co-failure signal.
	IncludeErrors bool
	// MaxRows overrides DefaultMaxFailureRows when positive.
	MaxRows int
}

// FailureOccurrences projects the filtered failures down to (test, run) pairs,
// which is all the co-failure analysis needs. This is a small fraction of the
// evidence — only failing rows, only three columns, deduplicated in the database.
func (s *EvidenceStore) FailureOccurrences(ctx context.Context, params FailureOccurrenceParams) ([]analytics.Occurrence, error) {
	if params.RunKey == "" {
		params.RunKey = RunKeyAuto
	}
	if !params.RunKey.Valid() {
		return nil, fmt.Errorf("unknown run key %q", params.RunKey)
	}

	maxRows := params.MaxRows
	if maxRows <= 0 {
		maxRows = DefaultMaxFailureRows
	}

	f := buildFilter(params.Filter)
	results := []any{model.ResultFail}
	if params.IncludeErrors {
		results = append(results, model.ResultError)
	}
	placeholders := make([]string, len(results))
	for i, r := range results {
		placeholders[i] = f.arg(r)
	}
	f.add(fmt.Sprintf("result IN (%s)", strings.Join(placeholders, ",")))

	runExpr := params.RunKey.expr()
	// Rows with no run key cannot be grouped with anything, so they are dropped
	// rather than silently collapsed into a single null bucket.
	f.add(runExpr + " IS NOT NULL")

	query := fmt.Sprintf(
		"SELECT DISTINCT repo, procedure_ref, %s AS run FROM evidence%s LIMIT %s",
		runExpr, f.whereClause(), f.arg(maxRows+1))

	rows, err := s.pool.Query(ctx, query, f.args...)
	if err != nil {
		return nil, fmt.Errorf("query failure occurrences: %w", err)
	}
	defer rows.Close()

	occurrences := make([]analytics.Occurrence, 0)
	for rows.Next() {
		var repo, procedure, run string
		if err := rows.Scan(&repo, &procedure, &run); err != nil {
			return nil, fmt.Errorf("scan failure occurrence: %w", err)
		}
		occurrences = append(occurrences, analytics.Occurrence{
			Test: analytics.TestID{Repo: repo, ProcedureRef: procedure},
			// Runs are namespaced by repo so that two repos sharing a commit
			// hash or invocation id are not treated as one run.
			Run: repo + "\x00" + run,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate failure occurrences: %w", err)
	}

	if len(occurrences) > maxRows {
		return nil, &ErrTooManyRows{Max: maxRows}
	}

	return occurrences, nil
}

// WindowSummary is the headline shape of a query window.
type WindowSummary struct {
	Runs    int64 `json:"runs"`
	Pass    int64 `json:"pass"`
	Fail    int64 `json:"fail"`
	Error   int64 `json:"error"`
	Skipped int64 `json:"skipped"`

	Tests   int64 `json:"tests"`
	Repos   int64 `json:"repos"`
	Commits int64 `json:"commits"`

	FirstSeen *time.Time `json:"first_seen,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
}

// Summary counts the filtered evidence without grouping it.
func (s *EvidenceStore) Summary(ctx context.Context, filter model.EvidenceFilter) (*WindowSummary, error) {
	f := buildFilter(filter)

	query := `
SELECT
    count(*)::bigint,
    count(*) FILTER (WHERE result = 'PASS')::bigint,
    count(*) FILTER (WHERE result = 'FAIL')::bigint,
    count(*) FILTER (WHERE result = 'ERROR')::bigint,
    count(*) FILTER (WHERE result = 'SKIPPED')::bigint,
    count(DISTINCT (repo, procedure_ref))::bigint,
    count(DISTINCT repo)::bigint,
    count(DISTINCT rcs_ref)::bigint,
    min(finished_at),
    max(finished_at)
FROM evidence` + f.whereClause()

	var (
		sum                 WindowSummary
		firstSeen, lastSeen *time.Time
	)
	err := s.pool.QueryRow(ctx, query, f.args...).Scan(
		&sum.Runs, &sum.Pass, &sum.Fail, &sum.Error, &sum.Skipped,
		&sum.Tests, &sum.Repos, &sum.Commits,
		&firstSeen, &lastSeen,
	)
	if err != nil {
		return nil, fmt.Errorf("query summary: %w", err)
	}

	sum.FirstSeen = utcPtr(firstSeen)
	sum.LastSeen = utcPtr(lastSeen)

	return &sum, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}
