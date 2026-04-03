package retention

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/nesono/evidence-store/internal/model"
)

func TestIsPinned(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     bool
	}{
		{"nil metadata", "", false},
		{"empty object", `{}`, false},
		{"retain true", `{"retain": true}`, true},
		{"retain false", `{"retain": false}`, false},
		{"retain string", `{"retain": "yes"}`, false},
		{"other fields only", `{"tags": ["manual"]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &model.Evidence{}
			if tt.metadata != "" {
				ev.Metadata = json.RawMessage(tt.metadata)
			}
			assert.Equal(t, tt.want, isPinned(ev))
		})
	}
}

func TestIsInheritanceProtected(t *testing.T) {
	refs := map[string]struct{}{
		"org/repo\x00abc123": {},
	}

	ev := &model.Evidence{Repo: "org/repo", RCSRef: "abc123"}
	assert.True(t, isInheritanceProtected(ev, refs))

	ev2 := &model.Evidence{Repo: "org/repo", RCSRef: "def456"}
	assert.False(t, isInheritanceProtected(ev2, refs))

	ev3 := &model.Evidence{Repo: "org/other", RCSRef: "abc123"}
	assert.False(t, isInheritanceProtected(ev3, refs))
}

func TestEvaluator_MaxAgeZeroMeansKeepForever(t *testing.T) {
	// This tests that the worker logic treats max_age=0 as "keep forever".
	// We verify through the evaluator that the rule returns 0.
	eval := buildEvaluator(t, []Rule{
		{Name: "keep-forever", Match: map[string]string{"branch": "^main$"}, MaxAge: 0},
	})

	ev := &model.Evidence{Branch: "main", Result: model.ResultPass}
	assert.Equal(t, time.Duration(0), eval.MaxAge(ev))
}

func TestWorkerLogic_DeleteExpired(t *testing.T) {
	// Simulate worker decision logic without DB.
	eval := buildEvaluator(t, []Rule{
		{Name: "keep-releases", Match: map[string]string{"branch": "^main$"}, MaxAge: 0},
		{Name: "default", Match: map[string]string{}, MaxAge: 720 * time.Hour},
	})
	inheritedRefs := map[string]struct{}{
		"org/repo\x00protected-ref": {},
	}
	now := time.Now()

	records := []model.Evidence{
		{ID: uuid.New(), Branch: "main", Repo: "org/repo", RCSRef: "ref1", FinishedAt: now.Add(-1000 * time.Hour), Result: model.ResultPass},                                                    // main → keep forever
		{ID: uuid.New(), Branch: "feature/x", Repo: "org/repo", RCSRef: "ref2", FinishedAt: now.Add(-1000 * time.Hour), Result: model.ResultPass},                                               // expired, should delete
		{ID: uuid.New(), Branch: "feature/y", Repo: "org/repo", RCSRef: "ref3", FinishedAt: now.Add(-100 * time.Hour), Result: model.ResultPass},                                                // not expired
		{ID: uuid.New(), Branch: "feature/z", Repo: "org/repo", RCSRef: "protected-ref", FinishedAt: now.Add(-1000 * time.Hour), Result: model.ResultPass},                                      // inheritance protected
		{ID: uuid.New(), Branch: "feature/w", Repo: "org/repo", RCSRef: "ref5", FinishedAt: now.Add(-1000 * time.Hour), Result: model.ResultPass, Metadata: json.RawMessage(`{"retain":true}`)}, // pinned
	}

	var toDelete []uuid.UUID
	for i := range records {
		ev := &records[i]
		if isPinned(ev) {
			continue
		}
		if isInheritanceProtected(ev, inheritedRefs) {
			continue
		}
		maxAge := eval.MaxAge(ev)
		if maxAge <= 0 {
			continue
		}
		if now.Sub(ev.FinishedAt) >= maxAge {
			toDelete = append(toDelete, ev.ID)
		}
	}

	assert.Len(t, toDelete, 1, "only the expired feature/x record should be deleted")
	assert.Equal(t, records[1].ID, toDelete[0])
}
