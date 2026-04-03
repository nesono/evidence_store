package retention

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

func buildEvaluator(t *testing.T, rules []Rule) *Evaluator {
	t.Helper()
	cfg := &Config{Rules: rules}
	ev, err := NewEvaluator(cfg)
	require.NoError(t, err)
	return ev
}

func TestEvaluator_FirstMatchWins(t *testing.T) {
	eval := buildEvaluator(t, []Rule{
		{Name: "keep-releases", Match: map[string]string{"branch": "^main$"}, MaxAge: 0},
		{Name: "default", Match: map[string]string{}, MaxAge: 2160 * time.Hour},
	})

	ev := &model.Evidence{Branch: "main", Result: model.ResultPass}
	assert.Equal(t, time.Duration(0), eval.MaxAge(ev))

	ev2 := &model.Evidence{Branch: "feature/foo", Result: model.ResultPass}
	assert.Equal(t, 2160*time.Hour, eval.MaxAge(ev2))
}

func TestEvaluator_RegexMatching(t *testing.T) {
	eval := buildEvaluator(t, []Rule{
		{Name: "release-branches", Match: map[string]string{"branch": "^(main|release/.*)$"}, MaxAge: 0},
		{Name: "default", Match: map[string]string{}, MaxAge: 720 * time.Hour},
	})

	tests := []struct {
		branch string
		want   time.Duration
	}{
		{"main", 0},
		{"release/v1.0", 0},
		{"release/v2.3.4", 0},
		{"feature/login", 720 * time.Hour},
		{"pr/123", 720 * time.Hour},
	}
	for _, tt := range tests {
		ev := &model.Evidence{Branch: tt.branch, Result: model.ResultPass}
		assert.Equal(t, tt.want, eval.MaxAge(ev), "branch=%q", tt.branch)
	}
}

func TestEvaluator_MultiFieldAND(t *testing.T) {
	eval := buildEvaluator(t, []Rule{
		{
			Name:   "manual-on-main",
			Match:  map[string]string{"branch": "^main$", "evidence_type": "^manual$"},
			MaxAge: 8760 * time.Hour,
		},
		{Name: "default", Match: map[string]string{}, MaxAge: 720 * time.Hour},
	})

	// Both fields match.
	ev := &model.Evidence{Branch: "main", EvidenceType: "manual", Result: model.ResultPass}
	assert.Equal(t, 8760*time.Hour, eval.MaxAge(ev))

	// Only one field matches — falls through to default.
	ev2 := &model.Evidence{Branch: "main", EvidenceType: "bazel", Result: model.ResultPass}
	assert.Equal(t, 720*time.Hour, eval.MaxAge(ev2))

	ev3 := &model.Evidence{Branch: "feature/x", EvidenceType: "manual", Result: model.ResultPass}
	assert.Equal(t, 720*time.Hour, eval.MaxAge(ev3))
}

func TestEvaluator_NoMatchReturnsNegative(t *testing.T) {
	eval := buildEvaluator(t, []Rule{
		{Name: "only-main", Match: map[string]string{"branch": "^main$"}, MaxAge: 720 * time.Hour},
	})

	ev := &model.Evidence{Branch: "develop", Result: model.ResultPass}
	assert.Equal(t, time.Duration(-1), eval.MaxAge(ev))
}

func TestEvaluator_EmptyMatchMatchesAll(t *testing.T) {
	eval := buildEvaluator(t, []Rule{
		{Name: "catch-all", Match: map[string]string{}, MaxAge: 720 * time.Hour},
	})

	ev := &model.Evidence{Branch: "anything", Repo: "whatever", Result: model.ResultFail}
	assert.Equal(t, 720*time.Hour, eval.MaxAge(ev))
}

func TestEvaluator_ResultFieldMatching(t *testing.T) {
	eval := buildEvaluator(t, []Rule{
		{Name: "keep-failures", Match: map[string]string{"result": "^FAIL$"}, MaxAge: 8760 * time.Hour},
		{Name: "default", Match: map[string]string{}, MaxAge: 720 * time.Hour},
	})

	assert.Equal(t, 8760*time.Hour, eval.MaxAge(&model.Evidence{Result: model.ResultFail}))
	assert.Equal(t, 720*time.Hour, eval.MaxAge(&model.Evidence{Result: model.ResultPass}))
}
