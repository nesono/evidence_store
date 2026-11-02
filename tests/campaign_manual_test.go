package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/retention"
	"github.com/nesono/evidence-store/internal/store"
)

func TestCampaignManualFieldsAndContextReuse(t *testing.T) {
	archive, _ := campaignArchive(t)
	p := decodeJSON[store.CampaignPackage](t, importCampaign(t, testServer.URL, "", archive))
	cleanupCampaign(t, p.ID)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM campaign_attempt WHERE campaign_id=$1`, p.ID)
		assert.NoError(t, err)
	})
	create := func() store.CampaignAttempt {
		resp := draftRequest(t, testServer.URL, "", http.MethodPost, "/api/v1/campaign-attempts", map[string]any{"id": uuid.New(), "campaign_id": p.ID, "procedure_ref": "patrol/display"})
		require.Equal(t, 201, resp.StatusCode)
		return decodeJSON[store.CampaignAttempt](t, resp)
	}
	a := create()
	assert.Equal(t, "manual_test", a.Data.EvidenceType)
	assert.Equal(t, "anonymous", a.Data.Source)
	assert.Empty(t, a.Data.RCSRef)
	image := samplePNG(t, 193)
	digest, _, err := testBlobStore.Put(context.Background(), bytes.NewReader(image))
	require.NoError(t, err)
	metadata := map[string]json.RawMessage{}
	for key, value := range map[string]any{
		"location": "Test track", "weather_conditions": "Rain", "tags": []string{"campaign", "vehicle"},
		"observations": "Observed stopping.\n![Photo](/api/v1/blobs/" + string(digest) + ".png)",
		"vehicle_id":   "vehicle-a", "custom_object": map[string]any{"nested": true},
	} {
		metadata[key], err = json.Marshal(value)
		require.NoError(t, err)
	}
	a.Data.Repo = "software/repo"
	a.Data.Branch = "release"
	a.Data.RCSRef = "deployed-123"
	a.Data.Metadata = metadata
	a.Data.Notes = "Tester notes"
	a.Data.Result = "PASS"
	a.Data.Answers["visible"] = json.RawMessage(`{"outcome":"PASS"}`)
	resp := draftRequest(t, testServer.URL, "", http.MethodPut, "/api/v1/campaign-attempts/"+a.ID.String(), map[string]any{"revision": 1, "operation_id": uuid.New(), "data": a.Data})
	require.Equal(t, 200, resp.StatusCode)
	saved := decodeJSON[store.CampaignAttempt](t, resp)
	assert.Equal(t, a.Data, saved.Data)
	w, err := retention.NewWorker(&retention.Config{}, testEvidenceStore, testInheritanceStore, testLogger)
	require.NoError(t, err)
	w.WithBlobs(testBlobStore, testBlobRefStore, 0)
	_, err = w.SweepBlobs(context.Background())
	require.NoError(t, err)
	_, err = testBlobStore.Stat(context.Background(), digest)
	require.NoError(t, err, "saved draft attachment must survive sweeping")
	next := create()
	assert.Equal(t, "software/repo", next.Data.Repo)
	assert.Equal(t, "release", next.Data.Branch)
	assert.Equal(t, "deployed-123", next.Data.RCSRef)
	assert.Empty(t, next.Data.Notes)
	assert.Empty(t, next.Data.Result)
	assert.Empty(t, next.Data.Answers)
	assert.NotContains(t, next.Data.Metadata, "weather_conditions")
	other, err := store.NewCampaignStore(testPool).LatestSoftwareContext(context.Background(), p.ID, "different owner")
	require.NoError(t, err)
	assert.Empty(t, other.Repo)
	final, _, err := store.NewCampaignStore(testPool).FinalizeAttempt(context.Background(), a.ID, "anonymous", saved.Revision, time.Now().UTC().Truncate(time.Microsecond))
	require.NoError(t, err)
	require.NotNil(t, final.EvidenceID)
	t.Cleanup(func() {
		_, err := testEvidenceStore.DeleteBatch(context.Background(), []uuid.UUID{*final.EvidenceID})
		assert.NoError(t, err)
	})
	ev, err := testEvidenceStore.GetByID(context.Background(), *final.EvidenceID)
	require.NoError(t, err)
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ev.Metadata, &stored))
	for key, value := range metadata {
		assert.JSONEq(t, string(value), string(stored[key]), key)
	}
	assert.Equal(t, model.TypeManualTest, ev.EvidenceType)
	var roots int
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT count(*) FROM campaign_draft_blob_ref WHERE attempt_id=$1`, a.ID).Scan(&roots))
	assert.Zero(t, roots)
	refs, err := testBlobRefStore.ForEvidence(context.Background(), ev.ID)
	require.NoError(t, err)
	assert.Contains(t, refs, digest)
	_, err = testEvidenceStore.DeleteBatch(context.Background(), []uuid.UUID{ev.ID})
	require.NoError(t, err)
	_, err = w.SweepBlobs(context.Background())
	require.NoError(t, err)
	_, err = testBlobStore.Stat(context.Background(), digest)
	assert.ErrorIs(t, err, blob.ErrNotFound)
}

func TestCampaignManualFieldsRejectSpoofing(t *testing.T) {
	ts := setupRBACServer(t, nil)
	key := issueKey(t, rbacSubject(t, "tester"), "contributor")
	archive, _ := campaignArchive(t)
	p := decodeJSON[store.CampaignPackage](t, importCampaign(t, ts.URL, key, archive))
	cleanupCampaign(t, p.ID)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM campaign_attempt WHERE campaign_id=$1`, p.ID)
		assert.NoError(t, err)
	})
	resp := draftRequest(t, ts.URL, key, "POST", "/api/v1/campaign-attempts", map[string]any{"id": uuid.New(), "campaign_id": p.ID, "procedure_ref": "patrol/display"})
	a := decodeJSON[store.CampaignAttempt](t, resp)
	url := "/api/v1/campaign-attempts/" + a.ID.String()
	data := a.Data
	data.Source = "another person"
	resp = draftRequest(t, ts.URL, key, "PUT", url, map[string]any{"revision": 1, "operation_id": uuid.New(), "data": data})
	resp.Body.Close()
	assert.Equal(t, 403, resp.StatusCode)
	data = a.Data
	data.Metadata = map[string]json.RawMessage{"procedure_ref": json.RawMessage(`"other"`)}
	resp = draftRequest(t, ts.URL, key, "PUT", url, map[string]any{"revision": 1, "operation_id": uuid.New(), "data": data})
	resp.Body.Close()
	assert.Equal(t, 422, resp.StatusCode)
	data.Metadata = map[string]json.RawMessage{"observations": json.RawMessage(`"![missing](/api/v1/blobs/sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png)"`)}
	resp = draftRequest(t, ts.URL, key, "PUT", url, map[string]any{"revision": 1, "operation_id": uuid.New(), "data": data})
	resp.Body.Close()
	assert.Equal(t, 422, resp.StatusCode)
}
