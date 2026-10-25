package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/campaign"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/retention"
	"github.com/nesono/evidence-store/internal/server"
	"github.com/nesono/evidence-store/internal/store"
)

func campaignArchive(t *testing.T) ([]byte, []byte) {
	t.Helper()
	id := uuid.NewString()
	image := samplePNG(t, 77)
	files := map[string][]byte{
		"manifest.yaml": []byte(fmt.Sprintf("format_version: 1\ncampaign: {id: '%s', name: Equipment patrol}\nsource: {repository: 'https://example.org/scenarios'}\nscenarios: [{id: display, path: scenario.yaml}]\n", id)),
		"scenario.yaml": []byte("id: display\ntitle: Display\nprocedure_ref: patrol/display\ndescription: procedure.md\ndefaults: {evidence_type: manual_test}\nassets: [setup.png]\n"),
		"procedure.md":  []byte("# Display\n![Setup](setup.png)\n\n```test-check\nid: visible\nlabel: Display visible\nbehavior: expect\n```\n"),
		"setup.png":     image,
	}
	archive, _, err := campaign.BuildPackage(files)
	require.NoError(t, err)
	return archive, image
}

func importCampaign(t *testing.T, url, token string, archive []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/api/v1/campaigns/import", bytes.NewReader(archive))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/gzip")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func cleanupCampaign(t *testing.T, id uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM campaign_package WHERE id = $1`, id)
		assert.NoError(t, err)
	})
}

func TestCampaignImportRoundTrip(t *testing.T) {
	archive, image := campaignArchive(t)
	resp := importCampaign(t, testServer.URL, "", archive)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	row := decodeJSON[store.CampaignPackage](t, resp)
	cleanupCampaign(t, row.ID)
	assert.Equal(t, string(blob.DigestOf(archive)), row.Digest)
	assert.Equal(t, "anonymous", row.ImportedBy)
	assert.Equal(t, int64(len(archive)), row.ArchiveSize)
	assert.Equal(t, "/api/v1/campaigns/"+row.ID.String(), resp.Header.Get("Location"))
	assert.Equal(t, "visible", row.Definition.Scenarios[0].Procedure.Blocks[1].Check.ID)
	assert.Empty(t, row.Definition.Scenarios[0].Definition.ID)
	assert.Empty(t, row.Definition.Manifest.Scenarios[0].ID)
	assert.Equal(t, "patrol/display", row.Definition.Manifest.Scenarios[0].ProcedureRef)

	again := importCampaign(t, testServer.URL, "", archive)
	require.Equal(t, http.StatusOK, again.StatusCode)
	duplicate := decodeJSON[store.CampaignPackage](t, again)
	assert.Equal(t, row.ID, duplicate.ID)
	assert.Equal(t, row.ImportedAt, duplicate.ImportedAt)

	detail := getJSON(t, "/api/v1/campaigns/"+row.ID.String())
	require.Equal(t, http.StatusOK, detail.StatusCode)
	assert.Equal(t, row, decodeJSON[store.CampaignPackage](t, detail))
	download := getJSON(t, "/api/v1/campaigns/"+row.ID.String()+"/package")
	got, err := io.ReadAll(download.Body)
	download.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, archive, got)
	assert.Equal(t, "application/gzip", download.Header.Get("Content-Type"))
	assert.Contains(t, download.Header.Get("Content-Disposition"), "attachment")
	assert.Equal(t, "nosniff", download.Header.Get("X-Content-Type-Options"))

	asset := getJSON(t, "/api/v1/blobs/"+string(blob.DigestOf(image)))
	got, err = io.ReadAll(asset.Body)
	asset.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, image, got)
	assert.Equal(t, "image/png", asset.Header.Get("Content-Type"))
	refs, err := testBlobRefStore.Referenced(context.Background(), []blob.Digest{blob.DigestOf(archive), blob.DigestOf(image)})
	require.NoError(t, err)
	assert.Len(t, refs, 2)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, err := http.NewRequest(method, testServer.URL+"/api/v1/campaigns/"+row.ID.String()+"/package", nil)
		require.NoError(t, err)
		req.Header.Set("If-None-Match", `"`+row.Digest+`"`)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotModified, resp.StatusCode)
	}
}

func TestCampaignProcedureReferenceMigration(t *testing.T) {
	// Recreate the old persisted projection in a transaction. Migration changes
	// only that projection; the original archive and record identity stay intact.
	archive, _ := campaignArchive(t)
	p, err := campaign.ReadPackage(bytes.NewReader(archive))
	require.NoError(t, err)
	p.Manifest.Scenarios[0].ID = "old-display-id"
	p.Manifest.Scenarios[0].ProcedureRef = ""
	p.Scenarios[0].Definition.ID = "old-display-id"
	definition, err := json.Marshal(p)
	require.NoError(t, err)
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	id := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO campaign_package (id,digest,archive_size,imported_by,definition) VALUES ($1,$2,$3,'tester',$4)`, id, p.Digest, len(archive), definition)
	require.NoError(t, err)
	run := func(direction string) {
		data, err := os.ReadFile(filepath.Join(testMigrationsPath, "000017_campaign_procedure_references."+direction+".sql"))
		require.NoError(t, err)
		_, err = tx.Exec(ctx, string(data))
		require.NoError(t, err)
	}
	for _, direction := range []string{"up", "down", "up"} {
		run(direction)
		var digest string
		var stored []byte
		require.NoError(t, tx.QueryRow(ctx, `SELECT digest, definition FROM campaign_package WHERE id=$1`, id).Scan(&digest, &stored))
		assert.Equal(t, p.Digest, digest)
		var decoded campaign.Package
		require.NoError(t, json.Unmarshal(stored, &decoded))
		assert.Equal(t, 1, decoded.Manifest.FormatVersion)
		assert.Equal(t, "patrol/display", decoded.Scenarios[0].Definition.ProcedureRef)
		if direction == "up" {
			assert.Empty(t, decoded.Scenarios[0].Definition.ID)
			assert.Empty(t, decoded.Manifest.Scenarios[0].ID)
			assert.Equal(t, "patrol/display", decoded.Manifest.Scenarios[0].ProcedureRef)
		} else {
			assert.Equal(t, "patrol/display", decoded.Scenarios[0].Definition.ID)
			assert.Equal(t, "patrol/display", decoded.Manifest.Scenarios[0].ID)
		}
	}
}

func TestCampaignReferenceOnlyImport(t *testing.T) {
	const ref = "//tests/display:check"
	archive, _, err := campaign.BuildPackage(map[string][]byte{
		"manifest.yaml": []byte(fmt.Sprintf("format_version: 2\ncampaign: {id: '%s', name: Demo}\nsource: {repository: 'https://example.org/scenarios'}\nscenarios: [{procedure_ref: '%s', path: scenario.yaml}]\n", uuid.NewString(), ref)),
		"scenario.yaml": []byte("title: Display\nprocedure_ref: '" + ref + "'\ndescription: procedure.md\ndefaults: {evidence_type: manual_test}\n"),
		"procedure.md":  []byte("# Display\nObserve the display."),
	})
	require.NoError(t, err)
	resp := importCampaign(t, testServer.URL, "", archive)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	row := decodeJSON[store.CampaignPackage](t, resp)
	cleanupCampaign(t, row.ID)
	assert.Equal(t, ref, row.Definition.Manifest.Scenarios[0].ProcedureRef)
	assert.Equal(t, ref, row.Definition.Scenarios[0].Definition.ProcedureRef)
	encoded, err := json.Marshal(row.Definition.Scenarios[0].Definition)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(encoded), `"id":`))
}

func TestCampaignListAndErrors(t *testing.T) {
	archive, _ := campaignArchive(t)
	resp := importCampaign(t, testServer.URL, "", archive)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	row := decodeJSON[store.CampaignPackage](t, resp)
	cleanupCampaign(t, row.ID)
	list := getJSON(t, "/api/v1/campaigns?limit=1")
	require.Equal(t, http.StatusOK, list.StatusCode)
	page := decodeJSON[struct {
		Campaigns []store.CampaignSummary `json:"campaigns"`
	}](t, list)
	require.Len(t, page.Campaigns, 1)
	assert.NotEmpty(t, page.Campaigns[0].Manifest.Campaign.ID)
	for _, suffix := range []string{"?limit=0", "?limit=101", "?limit=abc", "?offset=-1", "?limit=1&limit=2", "/invalid"} {
		resp := getJSON(t, "/api/v1/campaigns"+suffix)
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, suffix)
	}
	absent := getJSON(t, "/api/v1/campaigns/"+uuid.NewString())
	absent.Body.Close()
	assert.Equal(t, http.StatusNotFound, absent.StatusCode)

	bad := importCampaign(t, testServer.URL, "", []byte("not gzip"))
	bad.Body.Close()
	assert.Equal(t, http.StatusUnprocessableEntity, bad.StatusCode)
	large := importCampaign(t, testServer.URL, "", make([]byte, campaign.MaxArchiveBytes+1))
	large.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, large.StatusCode)
	wrongType, err := http.Post(testServer.URL+"/api/v1/campaigns/import", "application/json", bytes.NewReader(archive))
	require.NoError(t, err)
	wrongType.Body.Close()
	assert.Equal(t, http.StatusUnsupportedMediaType, wrongType.StatusCode)
}

func TestCampaignConcurrentImport(t *testing.T) {
	archive, _ := campaignArchive(t)
	const count = 6
	ids := make(chan uuid.UUID, count)
	statuses := make(chan int, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			resp, err := http.Post(testServer.URL+"/api/v1/campaigns/import", "application/gzip", bytes.NewReader(archive))
			if err != nil {
				errors <- err
				return
			}
			defer resp.Body.Close()
			var row store.CampaignPackage
			if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
				errors <- err
				return
			}
			ids <- row.ID
			statuses <- resp.StatusCode
		})
	}
	wg.Wait()
	close(errors)
	close(ids)
	close(statuses)
	for err := range errors {
		require.NoError(t, err)
	}
	var id uuid.UUID
	for got := range ids {
		require.NotEqual(t, uuid.Nil, got)
		if id == uuid.Nil {
			id = got
			cleanupCampaign(t, id)
		}
		assert.Equal(t, id, got)
	}
	created := 0
	for status := range statuses {
		if status == http.StatusCreated {
			created++
		} else {
			assert.Equal(t, http.StatusOK, status)
		}
	}
	assert.Equal(t, 1, created)
}

func TestCampaignPermissionsAndAttribution(t *testing.T) {
	ts := setupRBACServer(t, nil)
	viewer := issueKey(t, rbacSubject(t, "viewer"), "viewer")
	writerSubject := rbacSubject(t, "writer")
	writer := issueKey(t, writerSubject, "ci")
	archive, _ := campaignArchive(t)
	for token, status := range map[string]int{"": http.StatusUnauthorized, viewer: http.StatusForbidden} {
		resp := importCampaign(t, ts.URL, token, archive)
		resp.Body.Close()
		assert.Equal(t, status, resp.StatusCode)
	}
	resp := importCampaign(t, ts.URL, writer, archive)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	row := decodeJSON[store.CampaignPackage](t, resp)
	cleanupCampaign(t, row.ID)
	assert.Equal(t, writerSubject, row.ImportedBy)
	for _, endpoint := range []string{"/api/v1/campaigns", "/api/v1/campaigns/" + row.ID.String(), "/api/v1/campaigns/" + row.ID.String() + "/package"} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+endpoint, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+viewer)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

type failingCampaignBlobs struct {
	blob.Store
	puts int
}

func (b *failingCampaignBlobs) Put(ctx context.Context, r io.Reader) (blob.Digest, int64, error) {
	b.puts++
	if b.puts > 1 {
		return "", 0, errors.New("asset write failed")
	}
	return b.Store.Put(ctx, r)
}

func TestCampaignFailedImportIsNotPublished(t *testing.T) {
	archive, _ := campaignArchive(t)
	backend, err := blob.NewFS(t.TempDir())
	require.NoError(t, err)
	cfg := &config.Config{ListenAddr: ":0", Blob: testBlobConfig}
	ts := httptest.NewServer(server.New(cfg, testPool, &failingCampaignBlobs{Store: backend}, server.SSO{}).Handler())
	t.Cleanup(ts.Close)
	resp := importCampaign(t, ts.URL, "", archive)
	resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	var count int
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT count(*) FROM campaign_package WHERE digest=$1`, string(blob.DigestOf(archive))).Scan(&count))
	assert.Zero(t, count)
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT count(*) FROM campaign_blob_ref WHERE digest=$1`, string(blob.DigestOf(archive))).Scan(&count))
	assert.Zero(t, count)
}

func TestCampaignSurvivesBlobSweep(t *testing.T) {
	archive, image := campaignArchive(t)
	resp := importCampaign(t, testServer.URL, "", archive)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	row := decodeJSON[store.CampaignPackage](t, resp)
	cleanupCampaign(t, row.ID)
	w, err := retention.NewWorker(&retention.Config{}, testEvidenceStore, testInheritanceStore, testLogger)
	require.NoError(t, err)
	w.WithBlobs(testBlobStore, testBlobRefStore, 0)
	_, err = w.SweepBlobs(context.Background())
	require.NoError(t, err)
	_, err = testBlobStore.Stat(context.Background(), blob.DigestOf(archive))
	require.NoError(t, err)
	_, err = testBlobStore.Stat(context.Background(), blob.DigestOf(image))
	require.NoError(t, err)
	// Another campaign owns the same setup asset. Removing the first package
	// releases its archive but must not release the second package's image.
	secondArchive, _ := campaignArchive(t)
	secondResp := importCampaign(t, testServer.URL, "", secondArchive)
	require.Equal(t, http.StatusCreated, secondResp.StatusCode)
	second := decodeJSON[store.CampaignPackage](t, secondResp)
	cleanupCampaign(t, second.ID)
	_, err = testPool.Exec(context.Background(), `DELETE FROM campaign_package WHERE id=$1`, row.ID)
	require.NoError(t, err)
	_, err = w.SweepBlobs(context.Background())
	require.NoError(t, err)
	_, err = testBlobStore.Stat(context.Background(), blob.DigestOf(archive))
	assert.ErrorIs(t, err, blob.ErrNotFound)
	_, err = testBlobStore.Stat(context.Background(), blob.DigestOf(image))
	require.NoError(t, err)
}

type pausedCampaignBlobs struct {
	blob.Store
	started chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (b *pausedCampaignBlobs) Put(ctx context.Context, r io.Reader) (blob.Digest, int64, error) {
	b.once.Do(func() { close(b.started); <-b.proceed })
	return b.Store.Put(ctx, r)
}

func TestCampaignSweepWaitsForImport(t *testing.T) {
	archive, _ := campaignArchive(t)
	p, assets, err := campaign.ReadPackageAssets(bytes.NewReader(archive))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paused := &pausedCampaignBlobs{Store: testBlobStore, started: make(chan struct{}), proceed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		row, _, err := store.NewCampaignStore(testPool).Import(ctx, paused, archive, p, assets, "test")
		if err == nil {
			cleanupCampaign(t, row.ID)
		}
		done <- err
	}()
	<-paused.started
	sweepDone := make(chan error, 1)
	go func() {
		sweepDone <- testBlobRefStore.WithSweepLock(ctx, []blob.Digest{blob.DigestOf(archive)}, func(refs map[blob.Digest]bool) error {
			if !refs[blob.DigestOf(archive)] {
				return errors.New("sweep did not see committed campaign reference")
			}
			return nil
		})
	}()
	select {
	case err := <-sweepDone:
		close(paused.proceed)
		<-done
		t.Fatalf("sweep completed during import: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(paused.proceed)
	require.NoError(t, <-done)
	require.NoError(t, <-sweepDone)
}

func TestCampaignImportWaitsForSweep(t *testing.T) {
	archive, _ := campaignArchive(t)
	p, assets, err := campaign.ReadPackageAssets(bytes.NewReader(archive))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	err = testBlobRefStore.WithSweepLock(ctx, nil, func(map[blob.Digest]bool) error {
		go func() {
			close(started)
			row, _, err := store.NewCampaignStore(testPool).Import(ctx, testBlobStore, archive, p, assets, "test")
			if err == nil {
				cleanupCampaign(t, row.ID)
			}
			done <- err
		}()
		<-started
		select {
		case err := <-done:
			return fmt.Errorf("import completed while sweep held lock: %v", err)
		case <-time.After(100 * time.Millisecond):
			return nil
		}
	})
	require.NoError(t, err)
	require.NoError(t, <-done)
}
