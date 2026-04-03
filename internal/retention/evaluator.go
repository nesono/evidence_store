package retention

import (
	"regexp"
	"time"

	"github.com/nesono/evidence-store/internal/model"
)

// compiledRule is a retention rule with pre-compiled regexes.
type compiledRule struct {
	name     string
	matchers map[string]*regexp.Regexp
	maxAge   time.Duration
}

// Evaluator matches evidence records against compiled retention rules.
type Evaluator struct {
	rules []compiledRule
}

// NewEvaluator compiles the rules from a Config into an Evaluator.
// Rules must already be sorted by priority (descending) — LoadConfig does this.
func NewEvaluator(cfg *Config) (*Evaluator, error) {
	rules := make([]compiledRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		cr := compiledRule{
			name:     r.Name,
			matchers: make(map[string]*regexp.Regexp, len(r.Match)),
			maxAge:   r.MaxAge,
		}
		for field, pattern := range r.Match {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err // should not happen — already validated by ParseConfig
			}
			cr.matchers[field] = re
		}
		rules = append(rules, cr)
	}
	return &Evaluator{rules: rules}, nil
}

// fieldValue extracts a matchable field value from an evidence record.
func fieldValue(ev *model.Evidence, field string) string {
	switch field {
	case "repo":
		return ev.Repo
	case "branch":
		return ev.Branch
	case "rcs_ref":
		return ev.RCSRef
	case "procedure_ref":
		return ev.ProcedureRef
	case "evidence_type":
		return ev.EvidenceType
	case "source":
		return ev.Source
	case "result":
		return string(ev.Result)
	default:
		return ""
	}
}

// MaxAge returns the max_age of the first matching rule for the given evidence record.
// Returns -1 if no rule matches (record should not be deleted).
func (e *Evaluator) MaxAge(ev *model.Evidence) time.Duration {
	for _, rule := range e.rules {
		if matchesRule(ev, rule) {
			return rule.maxAge
		}
	}
	return -1
}

func matchesRule(ev *model.Evidence, rule compiledRule) bool {
	for field, re := range rule.matchers {
		val := fieldValue(ev, field)
		if !re.MatchString(val) {
			return false
		}
	}
	return true
}
