package tests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/retention"
)

// ---------------------------------------------------------------------------
// Tests: Retention
// ---------------------------------------------------------------------------

func TestRetentionDeletesExpiredRecords(t *testing.T) {
	repo := "org/ret_expired_" + uuid.New().String()[:8]

	// Insert an old record (100 days ago) on a feature branch.
	ev := makeEvidence(repo, "feature/old", "ret_old1", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, 201, resp.StatusCode)
	old := decodeJSON[model.Evidence](t, resp)

	// Backdate it via direct update through test server.
	backdateEvidence(t, old.ID, 100*24*time.Hour)

	// Insert a recent record on same branch.
	ev2 := makeEvidence(repo, "feature/new", "ret_new1", "//pkg:test", "ci", model.ResultPass)
	resp = postJSON(t, "/api/v1/evidence", ev2)
	require.Equal(t, 201, resp.StatusCode)
	recent := decodeJSON[model.Evidence](t, resp)

	// Run retention with 90-day default.
	cfg := mustParseRetentionConfig(t, `
interval: 1h
rules:
  - name: default
    match: {}
    max_age: 2160h
    priority: 0
`)
	deleted := runRetention(t, cfg)
	assert.GreaterOrEqual(t, deleted, 1)

	// Old record should be gone.
	resp = getJSON(t, "/api/v1/evidence/"+old.ID.String())
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()

	// Recent record should still exist.
	resp = getJSON(t, "/api/v1/evidence/"+recent.ID.String())
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
}

func TestRetentionKeepsForeverOnMaxAgeZero(t *testing.T) {
	repo := "org/ret_forever_" + uuid.New().String()[:8]

	ev := makeEvidence(repo, "main", "ret_main1", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, 201, resp.StatusCode)
	created := decodeJSON[model.Evidence](t, resp)

	backdateEvidence(t, created.ID, 365*24*time.Hour)

	cfg := mustParseRetentionConfig(t, `
interval: 1h
rules:
  - name: keep-main
    match:
      branch: "^main$"
    max_age: 0s
    priority: 100
  - name: default
    match: {}
    max_age: 720h
    priority: 0
`)
	runRetention(t, cfg)

	resp = getJSON(t, "/api/v1/evidence/"+created.ID.String())
	assert.Equal(t, 200, resp.StatusCode, "main branch record should be kept forever")
	resp.Body.Close()
}

func TestRetentionRespectsPinnedRecords(t *testing.T) {
	repo := "org/ret_pinned_" + uuid.New().String()[:8]

	ev := makeEvidence(repo, "feature/x", "ret_pin1", "//pkg:test", "ci", model.ResultPass)
	ev.Metadata = json.RawMessage(`{"retain": true}`)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, 201, resp.StatusCode)
	pinned := decodeJSON[model.Evidence](t, resp)

	backdateEvidence(t, pinned.ID, 200*24*time.Hour)

	cfg := mustParseRetentionConfig(t, `
interval: 1h
rules:
  - name: default
    match: {}
    max_age: 720h
    priority: 0
`)
	runRetention(t, cfg)

	resp = getJSON(t, "/api/v1/evidence/"+pinned.ID.String())
	assert.Equal(t, 200, resp.StatusCode, "pinned record should survive retention")
	resp.Body.Close()
}

func TestRetentionRespectsInheritance(t *testing.T) {
	repo := "org/ret_inh_" + uuid.New().String()[:8]

	// Insert evidence for a source commit.
	ev := makeEvidence(repo, "feature/z", "inh_src_ref", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, 201, resp.StatusCode)
	srcRecord := decodeJSON[model.Evidence](t, resp)

	backdateEvidence(t, srcRecord.ID, 200*24*time.Hour)

	// Create an inheritance declaration referencing this source.
	resp = postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo:          repo,
		SourceRCSRef:  "inh_src_ref",
		TargetRCSRef:  "inh_tgt_ref",
		Scope:         json.RawMessage(`[]`),
		Justification: "test",
		CreatedBy:     "ci",
	})
	require.Equal(t, 201, resp.StatusCode)
	resp.Body.Close()

	cfg := mustParseRetentionConfig(t, `
interval: 1h
rules:
  - name: default
    match: {}
    max_age: 720h
    priority: 0
`)
	runRetention(t, cfg)

	resp = getJSON(t, "/api/v1/evidence/"+srcRecord.ID.String())
	assert.Equal(t, 200, resp.StatusCode, "inheritance-protected record should survive retention")
	resp.Body.Close()
}

func TestRetentionRegexBranchMatching(t *testing.T) {
	repo := "org/ret_regex_" + uuid.New().String()[:8]

	// Insert a PR branch record (short retention) and a release branch record (long retention).
	evPR := makeEvidence(repo, "pr/123", "ret_pr1", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", evPR)
	require.Equal(t, 201, resp.StatusCode)
	prRecord := decodeJSON[model.Evidence](t, resp)
	backdateEvidence(t, prRecord.ID, 20*24*time.Hour) // 20 days old

	evRelease := makeEvidence(repo, "release/v1.0", "ret_rel1", "//pkg:test", "ci", model.ResultPass)
	resp = postJSON(t, "/api/v1/evidence", evRelease)
	require.Equal(t, 201, resp.StatusCode)
	relRecord := decodeJSON[model.Evidence](t, resp)
	backdateEvidence(t, relRecord.ID, 20*24*time.Hour) // 20 days old

	cfg := mustParseRetentionConfig(t, `
interval: 1h
rules:
  - name: keep-releases
    match:
      branch: "^release/.*$"
    max_age: 0s
    priority: 100
  - name: short-pr
    match:
      branch: "^pr/.*$"
    max_age: 336h
    priority: 50
  - name: default
    match: {}
    max_age: 2160h
    priority: 0
`)
	runRetention(t, cfg)

	// PR record (20 days > 14 days) should be deleted.
	resp = getJSON(t, "/api/v1/evidence/"+prRecord.ID.String())
	assert.Equal(t, 404, resp.StatusCode, "PR branch record should be deleted after 14 days")
	resp.Body.Close()

	// Release record should survive (keep forever).
	resp = getJSON(t, "/api/v1/evidence/"+relRecord.ID.String())
	assert.Equal(t, 200, resp.StatusCode, "release branch record should be kept forever")
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Retention test helpers
// ---------------------------------------------------------------------------

func mustParseRetentionConfig(t *testing.T, yaml string) *retention.Config {
	t.Helper()
	cfg, err := retention.ParseConfig([]byte(yaml))
	require.NoError(t, err)
	return cfg
}

func runRetention(t *testing.T, cfg *retention.Config) int {
	t.Helper()
	worker, err := retention.NewWorker(cfg, testEvidenceStore, testInheritanceStore, testLogger)
	require.NoError(t, err)
	deleted, err := worker.RunOnce(context.Background())
	require.NoError(t, err)
	return deleted
}

func backdateEvidence(t *testing.T, id uuid.UUID, age time.Duration) {
	t.Helper()
	newTime := time.Now().UTC().Add(-age)
	_, err := testPool.Exec(context.Background(),
		"UPDATE evidence SET finished_at = $1 WHERE id = $2", newTime, id)
	require.NoError(t, err)
}
