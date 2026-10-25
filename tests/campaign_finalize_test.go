package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/campaign"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
)

func finalizableAttempt(t *testing.T) (*store.CampaignAttempt, []byte) {
	t.Helper()
	archive, image := campaignArchive(t)
	p := decodeJSON[store.CampaignPackage](t, importCampaign(t, testServer.URL, "", archive))
	cleanupCampaign(t, p.ID)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM campaign_attempt WHERE campaign_id=$1`, p.ID)
		assert.NoError(t, err)
	})
	s := store.NewCampaignStore(testPool)
	a, _, err := s.CreateAttempt(context.Background(), uuid.New(), p.ID, "patrol/display", "anonymous", campaign.DraftData{
		Repo: "finalization/" + uuid.NewString(), Branch: "main", RCSRef: "actual-deployed-revision", Result: "PASS",
		Answers: map[string]json.RawMessage{"visible": json.RawMessage(`{"outcome":"PASS"}`)},
	})
	require.NoError(t, err)
	return a, image
}

func TestCampaignFinalizationLifecycle(t *testing.T) {
	a, image := finalizableAttempt(t)
	path := "/api/v1/campaign-attempts/" + a.ID.String()
	finished := "2026-10-25T14:30:00.123456789+02:00"
	req := map[string]any{"revision": a.Revision, "finished_at": finished}
	resp := draftRequest(t, testServer.URL, "", http.MethodPost, path+"/finalize", req)
	require.Equal(t, 201, resp.StatusCode)
	final := decodeJSON[store.CampaignAttempt](t, resp)
	require.NotNil(t, final.EvidenceID)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM evidence WHERE id=$1`, final.EvidenceID)
		assert.NoError(t, err)
	})
	assert.Equal(t, int64(2), final.Revision)
	assert.NotNil(t, final.FinalizedAt)
	evidenceResp := getJSON(t, "/api/v1/evidence/"+final.EvidenceID.String())
	require.Equal(t, 200, evidenceResp.StatusCode)
	ev := decodeJSON[model.Evidence](t, evidenceResp)
	assert.Equal(t, a.Data.Repo, ev.Repo)
	assert.Equal(t, a.Owner, ev.Source)
	assert.Equal(t, model.ResultPass, ev.Result)
	assert.Equal(t, "actual-deployed-revision", ev.RCSRef)
	assert.Equal(t, "2026-10-25T12:30:00.123456Z", ev.FinishedAt.Format(time.RFC3339Nano))
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(ev.Metadata, &metadata))
	assert.Equal(t, false, metadata["procedure_modified"])
	assert.Equal(t, a.ID.String(), metadata["campaign"].(map[string]any)["attempt_id"])
	refs, err := testBlobRefStore.ForEvidence(context.Background(), ev.ID)
	require.NoError(t, err)
	assert.Contains(t, refs, blob.DigestOf(image))
	resp = draftRequest(t, testServer.URL, "", http.MethodPost, path+"/finalize", req)
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, final.EvidenceID, decodeJSON[store.CampaignAttempt](t, resp).EvidenceID)
	resp = draftRequest(t, testServer.URL, "", http.MethodPut, path, map[string]any{"revision": 2, "operation_id": uuid.New(), "data": a.Data})
	resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
	resp = draftRequest(t, testServer.URL, "", http.MethodPost, path+"/finalize", map[string]any{"revision": 1, "finished_at": "2026-10-26T12:00:00Z"})
	resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)

	// Retention removes the result copy too; retry cannot resurrect expired evidence.
	_, err = testEvidenceStore.DeleteBatch(context.Background(), []uuid.UUID{ev.ID})
	require.NoError(t, err)
	resp = getJSON(t, path)
	expired := decodeJSON[store.CampaignAttempt](t, resp)
	assert.Nil(t, expired.EvidenceID)
	assert.NotNil(t, expired.FinalizedAt)
	assert.Empty(t, expired.Data.Answers)
	assert.Empty(t, expired.Data.Repo)
	resp = draftRequest(t, testServer.URL, "", http.MethodPost, path+"/finalize", req)
	require.Equal(t, 200, resp.StatusCode)
	assert.Nil(t, decodeJSON[store.CampaignAttempt](t, resp).EvidenceID)
}

func TestCampaignFinalizationValidationAndOwnership(t *testing.T) {
	a, _ := finalizableAttempt(t)
	path := "/api/v1/campaign-attempts/" + a.ID.String() + "/finalize"
	_, err := testPool.Exec(context.Background(), `UPDATE campaign_attempt SET data=jsonb_set(data,'{result}','""'::jsonb) WHERE id=$1`, a.ID)
	require.NoError(t, err)
	resp := draftRequest(t, testServer.URL, "", "POST", path, map[string]any{"revision": 1, "finished_at": "2026-10-25T12:00:00Z"})
	resp.Body.Close()
	assert.Equal(t, 422, resp.StatusCode)
	row, err := store.NewCampaignStore(testPool).GetAttempt(context.Background(), a.ID, "anonymous")
	require.NoError(t, err)
	assert.Nil(t, row.FinalizedAt)
	var count int
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT count(*) FROM evidence WHERE repo=$1`, a.Data.Repo).Scan(&count))
	assert.Zero(t, count)
	ts := setupRBACServer(t, nil)
	other := issueKey(t, rbacSubject(t, "other"), "admin")
	viewer := issueKey(t, rbacSubject(t, "viewer"), "viewer")
	for token, status := range map[string]int{other: 404, viewer: 403, "": 401} {
		resp = draftRequest(t, ts.URL, token, "POST", path, map[string]any{"revision": 1, "finished_at": "2026-10-25T12:00:00Z"})
		resp.Body.Close()
		assert.Equal(t, status, resp.StatusCode)
	}
}

func TestCampaignConcurrentFinalization(t *testing.T) {
	a, _ := finalizableAttempt(t)
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 4)
	results := make(chan error, 4)
	created := make(chan bool, 4)
	for range 4 {
		wg.Go(func() {
			row, fresh, err := store.NewCampaignStore(testPool).FinalizeAttempt(context.Background(), a.ID, a.Owner, 1, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
			results <- err
			if err == nil {
				ids <- *row.EvidenceID
				created <- fresh
			}
		})
	}
	wg.Wait()
	close(ids)
	close(results)
	close(created)
	for err := range results {
		require.NoError(t, err)
	}
	var id uuid.UUID
	for got := range ids {
		if id == uuid.Nil {
			id = got
		}
		assert.Equal(t, id, got)
	}
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM evidence WHERE id=$1`, id)
		assert.NoError(t, err)
	})
	count := 0
	for fresh := range created {
		if fresh {
			count++
		}
	}
	assert.Equal(t, 1, count)
}
