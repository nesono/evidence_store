// Package analytics derives reliability metrics from evidence verdict counts.
//
// The counting and the ordered scan happen in SQL, which is what SQL is good at.
// Everything that turns those counts into a rate, a bound, or a judgement lives
// here as a pure function, so the definitions are unit-testable without a
// database and there is exactly one place where each of them is written down.
package analytics

import "math"

// Counts is the verdict tally for one test over a query window, as returned by
// the store's aggregation.
type Counts struct {
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Error   int `json:"error"`
	Skipped int `json:"skipped"`

	// Flips is the number of consecutive verdict pairs that changed outcome, and
	// Transitions the number of such pairs compared. Both come from an ordered
	// scan in SQL; the ratio is FlipRate.
	Flips       int `json:"flips"`
	Transitions int `json:"transitions"`

	// FlakyCommits is the number of distinct rcs_refs at which this test both
	// passed and failed.
	FlakyCommits int `json:"flaky_commits"`
}

// Runs is every record for the test, whatever the outcome.
func (c Counts) Runs() int { return c.Pass + c.Fail + c.Error + c.Skipped }

// VerdictRuns is the runs that produced an actual statement about the test:
// it either passed or it failed.
func (c Counts) VerdictRuns() int { return c.Pass + c.Fail }

// FailRate is the share of verdicts that were failures. ERROR and SKIPPED are
// excluded deliberately — an infrastructure crash says nothing about whether the
// test is broken, and folding it in makes flaky infrastructure look like a
// failing test.
func (c Counts) FailRate() float64 {
	return ratio(c.Fail, c.VerdictRuns())
}

// ErrorRate is the share of executed runs lost to infrastructure. SKIPPED is
// excluded so that a mostly-skipped test does not dilute the signal.
func (c Counts) ErrorRate() float64 {
	return ratio(c.Error, c.Pass+c.Fail+c.Error)
}

// FlipRate is the share of consecutive verdict pairs that changed outcome. It is
// the separator the fail rate cannot provide: a permanently broken test has a
// fail rate of 1.0 and a flip rate of 0, while a flaky one has a middling fail
// rate and a high flip rate.
func (c Counts) FlipRate() float64 {
	return ratio(c.Flips, c.Transitions)
}

// PassRateLower is the Wilson lower bound on the pass rate, used to rank tests
// by how well evidenced their clean record is. Ranking on the raw pass rate
// ties 8/8 with 500/500; this does not.
func (c Counts) PassRateLower() float64 {
	return WilsonLower(c.Pass, c.VerdictRuns())
}

// z95 is the standard normal quantile for a 95% two-sided interval.
const z95 = 1.959963984540054

// WilsonLower returns the lower bound of the Wilson score interval for a
// proportion of successes out of total, at 95% confidence.
//
// It returns 0 for an empty or nonsensical sample rather than a NaN, because the
// result is used as a sort key and a NaN there silently corrupts the ordering.
func WilsonLower(successes, total int) float64 {
	if total <= 0 || successes < 0 {
		return 0
	}
	n := float64(total)
	p := float64(successes) / n

	denominator := 1 + z95*z95/n
	center := p + z95*z95/(2*n)
	margin := z95 * math.Sqrt(p*(1-p)/n+z95*z95/(4*n*n))

	return clamp01((center - margin) / denominator)
}

// Labels a test can carry. A test gets a list rather than a single category,
// because a test can legitimately be both flaky and infrastructure-heavy and
// picking one to show would hide the other.
const (
	LabelStable        = "stable"
	LabelAlwaysFailing = "always_failing"
	LabelFlaky         = "flaky"
	LabelInfraHeavy    = "infra_heavy"
	LabelSparse        = "sparse"
)

// Thresholds tune the labelling. They are request parameters rather than
// constants — what counts as "almost always failing" is a judgement about a
// particular test suite, not a universal truth.
type Thresholds struct {
	// MinRuns is the number of verdicts below which a test is called sparse and
	// no claim is made about its reliability.
	MinRuns int `json:"min_runs"`
	// AlwaysFailingRate is the fail rate at or above which a test is considered
	// chronically broken.
	AlwaysFailingRate float64 `json:"always_failing_rate"`
	// FlipRate is the flip rate at or above which a test is considered flaky.
	FlipRate float64 `json:"flip_rate"`
	// ErrorRate is the error rate at or above which a test is considered
	// infrastructure-heavy, subject to MinErrors.
	ErrorRate float64 `json:"error_rate"`
	// MinErrors guards ErrorRate against tiny samples, where a single failed
	// container start would otherwise be a 33% error rate.
	MinErrors int `json:"min_errors"`
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		MinRuns:           10,
		AlwaysFailingRate: 0.9,
		FlipRate:          0.2,
		ErrorRate:         0.1,
		MinErrors:         3,
	}
}

// Labels classifies a test. Thresholds are inclusive, and the returned slice is
// always non-nil so it renders as [] rather than null.
func (t Thresholds) Labels(c Counts) []string {
	labels := []string{}
	enough := c.VerdictRuns() >= t.MinRuns

	switch {
	case !enough:
		labels = append(labels, LabelSparse)
	case c.Fail == 0 && c.Error == 0:
		labels = append(labels, LabelStable)
	case c.FailRate() >= t.AlwaysFailingRate:
		labels = append(labels, LabelAlwaysFailing)
	}

	if c.FlakyCommits > 0 || c.FlipRate() >= t.FlipRate {
		labels = append(labels, LabelFlaky)
	}
	if c.Error >= t.MinErrors && c.ErrorRate() >= t.ErrorRate {
		labels = append(labels, LabelInfraHeavy)
	}

	return labels
}

func ratio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}
