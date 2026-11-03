package tests

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
)

func executionFixture(t *testing.T) (*store.CampaignStore, *store.CampaignExecution) {
	t.Helper()
	archive, _ := campaignArchive(t)
	p := decodeJSON[store.CampaignPackage](t, importCampaign(t, testServer.URL, "", archive))
	cleanupCampaign(t, p.ID)
	s := store.NewCampaignStore(testPool)
	input := store.CampaignExecution{ID: uuid.New(), PackageID: p.ID, Name: "Shared patrol " + uuid.NewString(), Repo: "campaign-test/" + uuid.NewString(), Branch: "main", RCSRef: "tested-revision", CreatedBy: "alice"}
	e, fresh, err := s.CreateExecution(context.Background(), input)
	require.NoError(t, err)
	require.True(t, fresh)
	again, fresh, err := s.CreateExecution(context.Background(), input)
	require.NoError(t, err)
	require.False(t, fresh)
	assert.Equal(t, e.ID, again.ID)
	input.RCSRef = "changed"
	_, _, err = s.CreateExecution(context.Background(), input)
	require.ErrorIs(t, err, store.ErrCampaignConflict)
	t.Cleanup(func() {
		ctx := context.Background()
		_, err := testPool.Exec(ctx, `DELETE FROM evidence WHERE repo=$1`, e.Repo)
		assert.NoError(t, err)
		_, err = testPool.Exec(ctx, `DELETE FROM campaign_attempt WHERE execution_id=$1`, e.ID)
		assert.NoError(t, err)
		_, err = testPool.Exec(ctx, `DELETE FROM campaign_execution_event WHERE execution_id=$1`, e.ID)
		assert.NoError(t, err)
		_, err = testPool.Exec(ctx, `DELETE FROM campaign_execution WHERE id=$1`, e.ID)
		assert.NoError(t, err)
	})
	return s, e
}

func TestSharedCampaignCloseReopenPreservesWork(t *testing.T) {
	ctx := context.Background()
	s, e := executionFixture(t)
	a, fresh, err := s.StartExecutionAttempt(ctx, e.ID, uuid.New(), "patrol/display", "alice")
	require.NoError(t, err)
	require.True(t, fresh)
	shared, err := s.GetAttempt(ctx, a.ID, "bob")
	require.NoError(t, err)
	assert.Equal(t, a.ID, shared.ID)
	resumed, fresh, err := s.StartExecutionAttempt(ctx, e.ID, uuid.New(), a.ProcedureRef, "bob")
	require.NoError(t, err)
	require.False(t, fresh)
	assert.Equal(t, a.ID, resumed.ID)
	data := a.Data
	data.Notes = "Partly exercised on the track"
	data.FinishedAt = "2026-10-24T09:15:23.123456Z"
	data.Answers["visible"] = json.RawMessage(`{"outcome":"PASS"}`)
	saved, err := s.SaveAttempt(ctx, a.ID, "bob", a.Revision, uuid.New(), data, testBlobStore)
	require.NoError(t, err)
	require.NotNil(t, saved.UpdatedBy)
	assert.Equal(t, "bob", *saved.UpdatedBy)
	assert.Equal(t, data.FinishedAt, saved.Data.FinishedAt)
	_, err = s.SaveAttempt(ctx, a.ID, "alice", a.Revision, uuid.New(), a.Data, testBlobStore)
	require.ErrorIs(t, err, store.ErrAttemptConflict)
	_, err = s.SetExecutionClosed(ctx, e.ID, e.Revision, true, "alice")
	require.ErrorIs(t, err, store.ErrCampaignConflict)
	e, err = s.GetExecution(ctx, e.ID)
	require.NoError(t, err)
	closed, err := s.SetExecutionClosed(ctx, e.ID, e.Revision, true, "bob")
	require.NoError(t, err)
	require.NotNil(t, closed.ClosedAt)
	a, err = s.GetAttempt(ctx, a.ID, "alice")
	require.NoError(t, err)
	require.NotNil(t, a.EvidenceID)
	assert.Equal(t, "SKIPPED", a.Data.Result)
	assert.Contains(t, a.Data.Notes, "Partly exercised")
	assert.JSONEq(t, `{"outcome":"PASS"}`, string(a.Data.Answers["visible"]))
	ev := decodeJSON[model.Evidence](t, getJSON(t, "/api/v1/evidence/"+a.EvidenceID.String()))
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(ev.Metadata, &metadata))
	snapshot := metadata["campaign"].(map[string]any)
	assert.Equal(t, e.ID.String(), snapshot["execution_id"])
	assert.Equal(t, "bob", snapshot["closure"].(map[string]any)["closed_by"])
	assert.Equal(t, "bob", snapshot["updated_by"])
	assert.Equal(t, "bob", snapshot["finalized_by"])
	assert.Equal(t, "SKIPPED", closed.Procedures[0].Result)
	_, _, err = s.StartExecutionAttempt(ctx, e.ID, uuid.New(), a.ProcedureRef, "alice")
	require.ErrorIs(t, err, store.ErrCampaignConflict)
	reopened, err := s.SetExecutionClosed(ctx, e.ID, closed.Revision, false, "alice")
	require.NoError(t, err)
	assert.Nil(t, reopened.ClosedAt)
	retry, fresh, err := s.StartExecutionAttempt(ctx, e.ID, uuid.New(), a.ProcedureRef, "alice")
	require.NoError(t, err)
	require.True(t, fresh)
	assert.NotEqual(t, a.ID, retry.ID)
	assert.Empty(t, retry.Data.Notes)
	assert.Equal(t, e.RCSRef, retry.Data.RCSRef)
	data = retry.Data
	data.Result = "PASS"
	data.Answers["visible"] = json.RawMessage(`{"outcome":"PASS"}`)
	retry, err = s.SaveAttempt(ctx, retry.ID, "alice", retry.Revision, uuid.New(), data, testBlobStore)
	require.NoError(t, err)
	final, _, err := s.FinalizeAttempt(ctx, retry.ID, "bob", retry.Revision, time.Now().UTC().Truncate(time.Microsecond))
	require.NoError(t, err)
	assert.Equal(t, "bob", *final.FinalizedBy)
	e, err = s.GetExecution(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, "PASS", e.Procedures[0].Result)
	assert.Equal(t, 2, e.Procedures[0].Attempts)
	// An unfinished retry supersedes the earlier PASS with a closure-generated skip.
	_, _, err = s.StartExecutionAttempt(ctx, e.ID, uuid.New(), a.ProcedureRef, "bob")
	require.NoError(t, err)
	e, err = s.GetExecution(ctx, e.ID)
	require.NoError(t, err)
	e, err = s.SetExecutionClosed(ctx, e.ID, e.Revision, true, "alice")
	require.NoError(t, err)
	assert.Equal(t, "SKIPPED", e.Procedures[0].Result)
	assert.Equal(t, 3, e.Procedures[0].Attempts)
}

func TestSharedCampaignUnrunSkipAndSearch(t *testing.T) {
	ctx := context.Background()
	s, e := executionFixture(t)
	rows, err := s.ListExecutions(ctx, "open", "Shrdptrl", 100, 0)
	require.NoError(t, err)
	found := false
	for _, row := range rows {
		if row.ID == e.ID {
			found = true
		}
	}
	assert.True(t, found)
	closed, err := s.SetExecutionClosed(ctx, e.ID, e.Revision, true, "alice")
	require.NoError(t, err)
	require.Len(t, closed.Procedures, 1)
	require.NotNil(t, closed.Procedures[0].LatestID)
	a, err := s.GetAttempt(ctx, *closed.Procedures[0].LatestID, "bob")
	require.NoError(t, err)
	assert.Equal(t, "SKIPPED", a.Data.Result)
	assert.Contains(t, string(a.Data.Answers["visible"]), "NOT_EXECUTED")
	rows, err = s.ListExecutions(ctx, "open", e.Repo, 100, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = s.ListExecutions(ctx, "completed", e.Repo, 100, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows, err = s.ListExecutions(ctx, "all", "%does_not_match%", 100, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestSharedCampaignConcurrentStartAndClosure(t *testing.T) {
	ctx := context.Background()
	s, e := executionFixture(t)
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			a, _, err := s.StartExecutionAttempt(ctx, e.ID, uuid.New(), "patrol/display", "tester")
			errs <- err
			if err == nil {
				ids <- a.ID
			}
		})
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var first uuid.UUID
	for id := range ids {
		if first == uuid.Nil {
			first = id
		}
		assert.Equal(t, first, id)
	}
	e, err := s.GetExecution(ctx, e.ID)
	require.NoError(t, err)
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.SetExecutionClosed(ctx, e.ID, e.Revision, true, "tester"); results <- err })
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, store.ErrCampaignConflict)
			conflict++
		}
	}
	assert.Equal(t, 1, success)
	assert.Equal(t, 1, conflict)
	e, err = s.GetExecution(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, e.Procedures[0].Attempts)
}

func TestSharedCampaignAPIAndFixedRevision(t *testing.T) {
	ctx := context.Background()
	s, e := executionFixture(t)
	a, _, err := s.StartExecutionAttempt(ctx, e.ID, uuid.New(), "patrol/display", "alice")
	require.NoError(t, err)
	ts := setupRBACServer(t, nil)
	token := issueKey(t, rbacSubject(t, "campaign-colleague"), "admin")
	resp := draftRequest(t, ts.URL, token, "GET", "/api/v1/campaign-attempts/"+a.ID.String(), nil)
	require.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
	data := a.Data
	data.RCSRef = "other"
	resp = draftRequest(t, ts.URL, token, "PUT", "/api/v1/campaign-attempts/"+a.ID.String(), map[string]any{"revision": a.Revision, "operation_id": uuid.New(), "data": data})
	assert.Equal(t, 422, resp.StatusCode)
	resp.Body.Close()
	resp = draftRequest(t, ts.URL, token, "GET", "/api/v1/campaign-executions/"+e.ID.String(), nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
	viewer := issueKey(t, rbacSubject(t, "campaign-viewer"), "viewer")
	resp = draftRequest(t, ts.URL, viewer, "GET", "/api/v1/campaign-attempts/"+a.ID.String(), nil)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
	resp = draftRequest(t, ts.URL, viewer, "PUT", "/api/v1/campaign-executions/"+e.ID.String()+"/state", map[string]any{"revision": e.Revision, "closed": true})
	assert.Equal(t, 403, resp.StatusCode)
	resp.Body.Close()
}

func TestSharedCampaignClosurePrefersUnfinishedAttempt(t *testing.T) {
	ctx := context.Background()
	s, e := executionFixture(t)
	a, _, err := s.StartExecutionAttempt(ctx, e.ID, uuid.New(), "patrol/display", "alice")
	require.NoError(t, err)
	data := a.Data
	data.Result = "PASS"
	data.Answers["visible"] = json.RawMessage(`{"outcome":"PASS"}`)
	a, err = s.SaveAttempt(ctx, a.ID, "alice", a.Revision, uuid.New(), data, testBlobStore)
	require.NoError(t, err)
	_, _, err = s.FinalizeAttempt(ctx, a.ID, "alice", a.Revision, time.Now().UTC().Truncate(time.Microsecond))
	require.NoError(t, err)
	retry, _, err := s.StartExecutionAttempt(ctx, e.ID, uuid.New(), a.ProcedureRef, "bob")
	require.NoError(t, err)
	// Timestamps may be restored or affected by a clock correction. A live
	// draft must still be closed even when it sorts before historical evidence.
	_, err = testPool.Exec(ctx, `UPDATE campaign_attempt SET created_at=created_at-interval '1 day' WHERE id=$1`, retry.ID)
	require.NoError(t, err)
	e, err = s.GetExecution(ctx, e.ID)
	require.NoError(t, err)
	e, err = s.SetExecutionClosed(ctx, e.ID, e.Revision, true, "alice")
	require.NoError(t, err)
	assert.Nil(t, e.Procedures[0].DraftID)
	retry, err = s.GetAttempt(ctx, retry.ID, "bob")
	require.NoError(t, err)
	assert.Equal(t, "SKIPPED", retry.Data.Result)
	assert.NotNil(t, retry.FinalizedAt)
}
