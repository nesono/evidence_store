package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func statsFor(procedure string, c Counts) TestStats {
	return TestStats{Repo: "org/repo", ProcedureRef: procedure, Counts: c}
}

func procedureOrder(stats []TestStats) []string {
	out := make([]string, len(stats))
	for i, s := range stats {
		out[i] = s.ProcedureRef
	}
	return out
}

func TestFinalizeDerivesRatesAndLabels(t *testing.T) {
	stats := []TestStats{
		statsFor("//a:test", Counts{Pass: 16, Fail: 4, Error: 5, Skipped: 2, Flips: 6, Transitions: 19}),
	}
	Finalize(stats, DefaultThresholds())

	s := stats[0]
	assert.Equal(t, 27, s.Runs)
	assert.Equal(t, 20, s.VerdictRuns)
	assertClose(t, 0.2, s.FailRate)
	assertClose(t, 0.2, s.ErrorRate)
	assertClose(t, 6.0/19.0, s.FlipRate)
	assertClose(t, WilsonLower(16, 20), s.PassRateLower)
	assert.Contains(t, s.Labels, LabelFlaky)
	assert.Contains(t, s.Labels, LabelInfraHeavy)
}

func TestFinalizeRespectsCustomThresholds(t *testing.T) {
	strict := DefaultThresholds()
	strict.MinRuns = 100

	stats := []TestStats{statsFor("//a:test", Counts{Pass: 50})}
	Finalize(stats, strict)

	assert.Equal(t, []string{LabelSparse}, stats[0].Labels)
}

func TestSortByFailRate(t *testing.T) {
	stats := []TestStats{
		statsFor("//low:test", Counts{Pass: 19, Fail: 1}),
		statsFor("//high:test", Counts{Pass: 1, Fail: 19}),
		statsFor("//mid:test", Counts{Pass: 10, Fail: 10}),
	}
	Finalize(stats, DefaultThresholds())

	require.NoError(t, Sort(stats, "fail_rate", true))
	assert.Equal(t, []string{"//high:test", "//mid:test", "//low:test"}, procedureOrder(stats))

	require.NoError(t, Sort(stats, "fail_rate", false))
	assert.Equal(t, []string{"//low:test", "//mid:test", "//high:test"}, procedureOrder(stats))
}

// Ranking "never fails" on the raw pass rate ties every clean test at 1.0; the
// Wilson bound is what puts the well-evidenced ones on top.
func TestSortByPassRateLowerRanksEvidence(t *testing.T) {
	stats := []TestStats{
		statsFor("//thin:test", Counts{Pass: 12}),
		statsFor("//thick:test", Counts{Pass: 400}),
		statsFor("//medium:test", Counts{Pass: 60}),
	}
	Finalize(stats, DefaultThresholds())

	require.NoError(t, Sort(stats, "pass_rate_lower", true))
	assert.Equal(t, []string{"//thick:test", "//medium:test", "//thin:test"}, procedureOrder(stats))
}

func TestSortByErrorRate(t *testing.T) {
	stats := []TestStats{
		statsFor("//clean:test", Counts{Pass: 20}),
		statsFor("//infra:test", Counts{Pass: 10, Error: 10}),
	}
	Finalize(stats, DefaultThresholds())

	require.NoError(t, Sort(stats, "error_rate", true))
	assert.Equal(t, []string{"//infra:test", "//clean:test"}, procedureOrder(stats))
}

// Equal sort values must not leave the order up to chance, or paging through the
// table would repeat and skip rows.
func TestSortTieBreakIsDeterministic(t *testing.T) {
	build := func() []TestStats {
		s := []TestStats{
			statsFor("//c:test", Counts{Pass: 10}),
			statsFor("//a:test", Counts{Pass: 10}),
			statsFor("//b:test", Counts{Pass: 10}),
		}
		Finalize(s, DefaultThresholds())
		return s
	}

	for _, desc := range []bool{false, true} {
		stats := build()
		require.NoError(t, Sort(stats, "fail_rate", desc))
		assert.Equal(t, []string{"//a:test", "//b:test", "//c:test"}, procedureOrder(stats),
			"tie-break stays ascending regardless of direction (desc=%v)", desc)
	}
}

func TestSortRejectsUnknownKey(t *testing.T) {
	err := Sort([]TestStats{}, "; DROP TABLE evidence", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sort")
}

func TestSortEmptyKeyUsesDefault(t *testing.T) {
	stats := []TestStats{
		statsFor("//z:test", Counts{Pass: 5}),
		statsFor("//a:test", Counts{Pass: 5}),
	}
	require.NoError(t, Sort(stats, "", false))
	assert.Equal(t, []string{"//a:test", "//z:test"}, procedureOrder(stats))
}

func TestSortByLastSeen(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	stats := []TestStats{
		{Repo: "org/repo", ProcedureRef: "//old:test", LastSeen: older},
		{Repo: "org/repo", ProcedureRef: "//new:test", LastSeen: newer},
	}
	require.NoError(t, Sort(stats, "last_seen", true))
	assert.Equal(t, []string{"//new:test", "//old:test"}, procedureOrder(stats))
}

func TestIsSortable(t *testing.T) {
	for _, key := range []string{"fail_rate", "error_rate", "flip_rate", "pass_rate_lower", "runs", "procedure_ref", "last_seen"} {
		assert.True(t, IsSortable(key), key)
	}
	assert.False(t, IsSortable("metadata"))
	assert.False(t, IsSortable(""))
}
