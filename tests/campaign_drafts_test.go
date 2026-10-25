package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/campaign"
	"github.com/nesono/evidence-store/internal/store"
)

func draftRequest(t *testing.T, base, token, method, path string, data any) *http.Response {
	t.Helper()
	b, err := json.Marshal(data)
	require.NoError(t, err)
	req, err := http.NewRequest(method, base+path, bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestCampaignDraftLifecycle(t *testing.T) {
	archive, _ := campaignArchive(t)
	resp := importCampaign(t, testServer.URL, "", archive)
	row := decodeJSON[store.CampaignPackage](t, resp)
	cleanupCampaign(t, row.ID)
	id := uuid.New()
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM campaign_attempt WHERE campaign_id=$1`, row.ID)
		assert.NoError(t, err)
	})
	create := map[string]any{"id": id, "campaign_id": row.ID, "procedure_ref": "patrol/display"}
	resp = draftRequest(t, testServer.URL, "", "POST", "/api/v1/campaign-attempts", create)
	require.Equal(t, 201, resp.StatusCode)
	a := decodeJSON[store.CampaignAttempt](t, resp)
	assert.Equal(t, int64(1), a.Revision)
	assert.Empty(t, a.Data.Result)
	assert.Empty(t, a.Data.Answers)
	path := "/api/v1/campaign-attempts/" + id.String()
	data := campaign.DraftData{Repo: "example/software", Branch: "main", RCSRef: "actual-build", Notes: "Tester observation", Answers: map[string]json.RawMessage{"visible": json.RawMessage(`{"outcome":"FAIL"}`)}}
	save := map[string]any{"revision": 1, "operation_id": uuid.New(), "data": data}
	for range 2 {
		resp = draftRequest(t, testServer.URL, "", "PUT", path, save)
		require.Equal(t, 200, resp.StatusCode)
		a = decodeJSON[store.CampaignAttempt](t, resp)
		assert.Equal(t, int64(2), a.Revision)
		assert.Equal(t, data, a.Data)
	}
	// The same operation ID with different data must not be accepted as a retry.
	changed := data
	changed.Notes = "different payload"
	save["data"] = changed
	resp = draftRequest(t, testServer.URL, "", "PUT", path, save)
	resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
	save["data"] = data
	// A lost create response can be retried without replacing saved work.
	resp = draftRequest(t, testServer.URL, "", "POST", "/api/v1/campaign-attempts", create)
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, int64(2), decodeJSON[store.CampaignAttempt](t, resp).Revision)
	save["operation_id"] = uuid.New()
	resp = draftRequest(t, testServer.URL, "", "PUT", path, save)
	resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
	save["revision"] = 2
	data.Answers["unknown"] = json.RawMessage(`true`)
	save["data"] = data
	resp = draftRequest(t, testServer.URL, "", "PUT", path, save)
	resp.Body.Close()
	assert.Equal(t, 422, resp.StatusCode)
	resp = getJSON(t, path)
	require.Equal(t, 200, resp.StatusCode)
	a = decodeJSON[store.CampaignAttempt](t, resp)
	assert.Equal(t, int64(2), a.Revision)
	assert.NotContains(t, a.Data.Answers, "unknown")
	resp = getJSON(t, "/api/v1/campaign-attempts?campaign_id="+row.ID.String()+"&procedure_ref=patrol%2Fdisplay")
	page := decodeJSON[struct {
		Attempts []store.CampaignAttempt `json:"attempts"`
	}](t, resp)
	require.Len(t, page.Attempts, 1)
	assert.Equal(t, id, page.Attempts[0].ID)
	var count int
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT count(*) FROM evidence WHERE repo='example/software'`).Scan(&count))
	assert.Zero(t, count)
}

func TestCampaignDraftOwnership(t *testing.T) {
	ts := setupRBACServer(t, nil)
	one := issueKey(t, rbacSubject(t, "one"), "contributor")
	two := issueKey(t, rbacSubject(t, "two"), "admin")
	viewer := issueKey(t, rbacSubject(t, "viewer"), "viewer")
	archive, _ := campaignArchive(t)
	row := decodeJSON[store.CampaignPackage](t, importCampaign(t, ts.URL, one, archive))
	cleanupCampaign(t, row.ID)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM campaign_attempt WHERE campaign_id=$1`, row.ID)
		assert.NoError(t, err)
	})
	id := uuid.New()
	create := map[string]any{"id": id, "campaign_id": row.ID, "procedure_ref": "patrol/display"}
	resp := draftRequest(t, ts.URL, one, "POST", "/api/v1/campaign-attempts", create)
	require.Equal(t, 201, resp.StatusCode)
	resp.Body.Close()
	for token, want := range map[string]int{two: 404, viewer: 403, "": 401} {
		resp = draftRequest(t, ts.URL, token, "GET", "/api/v1/campaign-attempts/"+id.String(), nil)
		resp.Body.Close()
		assert.Equal(t, want, resp.StatusCode)
	}
	resp = draftRequest(t, ts.URL, two, "POST", "/api/v1/campaign-attempts", create)
	resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
	resp = draftRequest(t, ts.URL, two, "PUT", "/api/v1/campaign-attempts/"+id.String(), map[string]any{})
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
	create["id"] = uuid.New()
	create["owner"] = "spoofed"
	resp = draftRequest(t, ts.URL, one, "POST", "/api/v1/campaign-attempts", create)
	resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestCampaignDraftConcurrentSave(t *testing.T) {
	archive, _ := campaignArchive(t)
	row := decodeJSON[store.CampaignPackage](t, importCampaign(t, testServer.URL, "", archive))
	cleanupCampaign(t, row.ID)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM campaign_attempt WHERE campaign_id=$1`, row.ID)
		assert.NoError(t, err)
	})
	s := store.NewCampaignStore(testPool)
	a, _, err := s.CreateAttempt(context.Background(), uuid.New(), row.ID, "patrol/display", "owner", campaign.DraftData{Answers: map[string]json.RawMessage{}})
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, notes := range []string{"first", "second"} {
		wg.Go(func() {
			_, err := s.SaveAttempt(context.Background(), a.ID, "owner", 1, uuid.New(), campaign.DraftData{Notes: notes, Answers: map[string]json.RawMessage{}})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	passed, conflicts := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else {
			assert.ErrorIs(t, err, store.ErrAttemptConflict)
			conflicts++
		}
	}
	assert.Equal(t, 1, passed)
	assert.Equal(t, 1, conflicts)
}
