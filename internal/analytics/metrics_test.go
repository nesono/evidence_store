package analytics

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func assertClose(t *testing.T, want, got float64) {
	t.Helper()
	assert.InDelta(t, want, got, 1e-6)
}

func TestCountsRuns(t *testing.T) {
	c := Counts{Pass: 10, Fail: 3, Error: 2, Skipped: 1}
	assert.Equal(t, 16, c.Runs())
	assert.Equal(t, 13, c.VerdictRuns())
}

// ERROR and SKIPPED are excluded from the fail rate: an infrastructure crash is
// not a statement about whether the test itself is broken.
func TestFailRateExcludesErrorAndSkipped(t *testing.T) {
	c := Counts{Pass: 8, Fail: 2, Error: 90, Skipped: 50}
	assertClose(t, 0.2, c.FailRate())
}

func TestFailRateNoVerdicts(t *testing.T) {
	assertClose(t, 0, Counts{Error: 5, Skipped: 3}.FailRate())
	assertClose(t, 0, Counts{}.FailRate())
}

// SKIPPED is excluded from the error rate too, so a mostly-skipped test does not
// dilute the infrastructure signal.
func TestErrorRate(t *testing.T) {
	c := Counts{Pass: 7, Fail: 1, Error: 2, Skipped: 40}
	assertClose(t, 0.2, c.ErrorRate())
	assertClose(t, 0, Counts{Skipped: 9}.ErrorRate())
}

func TestFlipRate(t *testing.T) {
	// 9 comparable pairs, 3 of which changed verdict.
	assertClose(t, 1.0/3.0, Counts{Flips: 3, Transitions: 9}.FlipRate())
	assertClose(t, 0, Counts{Flips: 0, Transitions: 0}.FlipRate())
}

// The property that makes the Wilson bound worth using: on the raw rate a
// 10-run and a 500-run clean record tie at a perfect score, and the bound
// breaks that tie in favour of the one backed by more evidence.
func TestPassRateLowerRewardsEvidence(t *testing.T) {
	short := Counts{Pass: 10}
	long := Counts{Pass: 500}

	assertClose(t, 0, short.FailRate())
	assertClose(t, 0, long.FailRate())

	assertClose(t, 0.722467, short.PassRateLower())
	assertClose(t, 0.992375, long.PassRateLower())
	assert.Greater(t, long.PassRateLower(), short.PassRateLower())
}

func TestWilsonLower(t *testing.T) {
	for _, tt := range []struct {
		name      string
		successes int
		total     int
		want      float64
	}{
		{"no data", 0, 0, 0},
		{"perfect 10", 10, 10, 0.722467},
		{"perfect 500", 500, 500, 0.992375},
		{"total failure", 0, 10, 0},
		{"half of 100", 50, 100, 0.403832},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertClose(t, tt.want, WilsonLower(tt.successes, tt.total))
		})
	}
}

func TestWilsonLowerIsBoundedToUnitInterval(t *testing.T) {
	for _, n := range []int{1, 2, 5, 100, 10000} {
		for _, s := range []int{0, 1, n / 2, n} {
			if s > n {
				continue
			}
			got := WilsonLower(s, n)
			assert.False(t, math.IsNaN(got))
			assert.GreaterOrEqual(t, got, 0.0)
			assert.LessOrEqual(t, got, 1.0)
		}
	}
}

// A test that never produced a verdict cannot be judged, and asking for a bound
// on nothing must not yield a NaN that then poisons the sort order.
func TestWilsonLowerNeverNaN(t *testing.T) {
	assert.False(t, math.IsNaN(WilsonLower(0, 0)))
	assert.False(t, math.IsNaN(WilsonLower(-1, 0)))
	assert.False(t, math.IsNaN(WilsonLower(5, -3)))
}

func TestLabelsStable(t *testing.T) {
	th := DefaultThresholds()
	assert.Equal(t, []string{LabelStable}, th.Labels(Counts{Pass: 50}))
}

// Zero failures is not enough on its own — three green runs says almost nothing.
func TestLabelsSparseBeatsStable(t *testing.T) {
	th := DefaultThresholds()
	assert.Equal(t, []string{LabelSparse}, th.Labels(Counts{Pass: 3}))
}

func TestLabelsAlwaysFailing(t *testing.T) {
	th := DefaultThresholds()
	labels := th.Labels(Counts{Pass: 1, Fail: 19})
	assert.Contains(t, labels, LabelAlwaysFailing)
	assert.NotContains(t, labels, LabelStable)
}

// The separator the raw fail rate cannot provide: both of these fail often, but
// only one of them is actually flaky.
func TestLabelsDistinguishesBrokenFromFlaky(t *testing.T) {
	th := DefaultThresholds()

	broken := Counts{Fail: 20, Flips: 0, Transitions: 19}
	flaky := Counts{Pass: 10, Fail: 10, Flips: 15, Transitions: 19}

	assert.Contains(t, th.Labels(broken), LabelAlwaysFailing)
	assert.NotContains(t, th.Labels(broken), LabelFlaky)

	assert.Contains(t, th.Labels(flaky), LabelFlaky)
	assert.NotContains(t, th.Labels(flaky), LabelAlwaysFailing)
}

// Passing and failing at the same commit is flakiness regardless of flip rate:
// same code, different outcome.
func TestLabelsFlakyFromSameCommitDisagreement(t *testing.T) {
	th := DefaultThresholds()
	c := Counts{Pass: 18, Fail: 2, Flips: 1, Transitions: 19, FlakyCommits: 1}
	assert.Contains(t, th.Labels(c), LabelFlaky)
}

func TestLabelsInfraHeavy(t *testing.T) {
	th := DefaultThresholds()
	assert.Contains(t, th.Labels(Counts{Pass: 20, Error: 5}), LabelInfraHeavy)
}

// A handful of errors in a huge sample is background noise, and two errors in
// seven runs is not yet a pattern. Both guards have to hold independently.
func TestLabelsInfraHeavyNeedsRateAndCount(t *testing.T) {
	th := DefaultThresholds()
	assert.NotContains(t, th.Labels(Counts{Pass: 1000, Error: 2}), LabelInfraHeavy) // rate too low
	assert.NotContains(t, th.Labels(Counts{Pass: 5, Error: 2}), LabelInfraHeavy)    // count too low
}

func TestLabelsCanBeMultiple(t *testing.T) {
	th := DefaultThresholds()
	c := Counts{Pass: 10, Fail: 10, Error: 6, Flips: 15, Transitions: 19, FlakyCommits: 2}
	labels := th.Labels(c)
	assert.Contains(t, labels, LabelFlaky)
	assert.Contains(t, labels, LabelInfraHeavy)
}

func TestLabelsNeverNil(t *testing.T) {
	th := DefaultThresholds()
	labels := th.Labels(Counts{Pass: 15, Fail: 1})
	assert.NotNil(t, labels)
	assert.Empty(t, labels)
}

// Thresholds are inclusive, so a value sitting exactly on the boundary counts.
func TestLabelsThresholdsAreInclusive(t *testing.T) {
	th := DefaultThresholds()
	assert.Contains(t, th.Labels(Counts{Pass: 1, Fail: 9}), LabelAlwaysFailing) // 0.9 exactly
	assert.Contains(t, th.Labels(Counts{Pass: 20, Fail: 20, Flips: 8, Transitions: 40}), LabelFlaky)
}

func TestDefaultThresholds(t *testing.T) {
	th := DefaultThresholds()
	assert.Equal(t, 10, th.MinRuns)
	assertClose(t, 0.9, th.AlwaysFailingRate)
	assertClose(t, 0.2, th.FlipRate)
	assertClose(t, 0.1, th.ErrorRate)
	assert.Equal(t, 3, th.MinErrors)
}
