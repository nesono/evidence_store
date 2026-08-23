package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/retention"
)

// ---------------------------------------------------------------------------
// Tests: Blobs (images in test logs)
// ---------------------------------------------------------------------------

// samplePNG returns a real PNG whose bytes differ per seed, so tests can tell
// two uploads apart and still upload something a browser would render.
func samplePNG(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := range 4 {
		for y := range 4 {
			img.Set(x, y, color.RGBA{R: uint8(seed), G: uint8(x * 40), B: uint8(y * 40), A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func upload(t *testing.T, body []byte, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/v1/blobs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

type uploadedBlob struct {
	Ref         string `json:"ref"`
	Digest      string `json:"digest"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

func uploadPNG(t *testing.T, seed int) (uploadedBlob, []byte) {
	t.Helper()
	body := samplePNG(t, seed)
	resp := upload(t, body, "image/png")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	return decodeJSON[uploadedBlob](t, resp), body
}

func TestBlobRoundTrip(t *testing.T) {
	uploaded, body := uploadPNG(t, 1)

	assert.Equal(t, "image/png", uploaded.ContentType)
	assert.Equal(t, int64(len(body)), uploaded.Size)
	assert.Equal(t, string(blob.DigestOf(body)), uploaded.Digest)
	assert.Equal(t, "/api/v1/blobs/"+uploaded.Digest+".png", uploaded.Ref)

	resp, err := http.Get(testServer.URL + uploaded.Ref)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, body, got, "served bytes must be the bytes that were stored")
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	// The type is decided from the bytes, so a browser must not be allowed to
	// reach a different conclusion.
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	// Content named by its own hash can never go stale.
	assert.Contains(t, resp.Header.Get("Cache-Control"), "immutable")
	assert.Equal(t, `"`+uploaded.Digest+`"`, resp.Header.Get("ETag"))
}

// The extension is a hint for the renderer, not part of the name.
func TestBlobServesRegardlessOfExtension(t *testing.T) {
	uploaded, body := uploadPNG(t, 2)

	for _, path := range []string{
		uploaded.Ref,
		"/api/v1/blobs/" + uploaded.Digest,
		"/api/v1/blobs/" + uploaded.Digest + ".jpg",
	} {
		resp, err := http.Get(testServer.URL + path)
		require.NoError(t, err)
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
		assert.Equal(t, body, got, path)
		// Whatever the reference claims, what comes back is what the bytes are.
		assert.Equal(t, "image/png", resp.Header.Get("Content-Type"), path)
	}
}

func TestBlobUploadIsDeduplicated(t *testing.T) {
	first, _ := uploadPNG(t, 3)
	second, _ := uploadPNG(t, 3)

	// Pasting the same screenshot into two records costs one object, and the
	// two records point at the same reference.
	assert.Equal(t, first.Digest, second.Digest)
	assert.Equal(t, first.Ref, second.Ref)
}

func TestBlobNotModified(t *testing.T) {
	uploaded, _ := uploadPNG(t, 4)

	req, err := http.NewRequest(http.MethodGet, testServer.URL+uploaded.Ref, nil)
	require.NoError(t, err)
	req.Header.Set("If-None-Match", `"`+uploaded.Digest+`"`)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotModified, resp.StatusCode)
}

func TestBlobRejectsWhatALogMayNotEmbed(t *testing.T) {
	tests := []struct {
		name        string
		body        []byte
		contentType string
	}{
		// The dangerous case: markup that a browser would execute, announced as
		// an image. The claim is ignored and the bytes decide.
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "image/svg+xml"},
		{"html", []byte("<!DOCTYPE html><html><body>hi</body></html>"), "image/png"},
		{"pdf", []byte("%PDF-1.7\n"), "application/pdf"},
		{"text", []byte("this is just a log line"), "image/png"},
		{"empty", nil, "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := upload(t, tt.body, tt.contentType)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
		})
	}
}

func TestBlobRejectsOversizedUpload(t *testing.T) {
	// Noise rather than a pattern: PNG compresses a gradient down to a few
	// kilobytes, which would not reach the cap this test is about.
	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	_, err := rand.Read(img.Pix)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	require.Greater(t, buf.Len(), 1<<20, "fixture must exceed the configured cap")

	resp := upload(t, buf.Bytes(), "image/png")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

func TestBlobGetRejectsUnusableReferences(t *testing.T) {
	absent := blob.DigestOf([]byte("never uploaded"))

	tests := []struct {
		name string
		path string
		want int
	}{
		{"unknown digest", "/api/v1/blobs/" + string(absent), http.StatusNotFound},
		{"not a digest", "/api/v1/blobs/not-a-digest", http.StatusBadRequest},
		{"path traversal", "/api/v1/blobs/sha256:..%2f..%2fetc%2fpasswd", http.StatusBadRequest},
		{"wrong algorithm", "/api/v1/blobs/md5:" + absent.Hex(), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(testServer.URL + tt.path)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tt.want, resp.StatusCode)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: references from a test log
// ---------------------------------------------------------------------------

func TestEvidenceRecordsTheImagesItsLogReferences(t *testing.T) {
	first, _ := uploadPNG(t, 10)
	second, _ := uploadPNG(t, 11)

	log := "## Run 2\n\n1. Powered on the rig\n\n![before](" + first.Ref + ")\n\n" +
		"2. Pressed the brake pedal\n\n![after](" + second.Ref + ")\n"

	created := createWithLog(t, "org/blobref_"+uuid.New().String()[:8], log)

	// The log itself is untouched — annotating metadata is not a licence to
	// rewrite what the tester wrote.
	var meta struct {
		Observations string   `json:"observations"`
		PhotoURIs    []string `json:"photo_uris"`
	}
	require.NoError(t, json.Unmarshal(created.Metadata, &meta))
	assert.Equal(t, log, meta.Observations)

	// A client reading the API should not have to parse markdown to find out a
	// record has photos.
	assert.Equal(t, []string{first.Ref, second.Ref}, meta.PhotoURIs)

	digests, err := testBlobRefStore.ForEvidence(context.Background(), created.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]blob.Digest{blob.Digest(first.Digest), blob.Digest(second.Digest)},
		digests)
}

func TestEvidenceWithoutImagesRecordsNoReferences(t *testing.T) {
	created := createWithLog(t, "org/blobref_none_"+uuid.New().String()[:8],
		"1. Powered on the rig — all lights green\n2. No photos taken\n")

	var meta map[string]any
	require.NoError(t, json.Unmarshal(created.Metadata, &meta))
	assert.NotContains(t, meta, "photo_uris")

	digests, err := testBlobRefStore.ForEvidence(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Empty(t, digests)
}

// ---------------------------------------------------------------------------
// Tests: the sweep
// ---------------------------------------------------------------------------

func TestSweepKeepsBlobsALogStillReferences(t *testing.T) {
	uploaded, _ := uploadPNG(t, 20)
	createWithLog(t, "org/sweep_keep_"+uuid.New().String()[:8], "![shot]("+uploaded.Ref+")\n")

	runSweep(t, 0)

	assertBlobPresent(t, uploaded.Digest, true)
}

func TestSweepCollectsBlobsNothingReferences(t *testing.T) {
	// An image pasted into a form that was then abandoned: uploaded, never
	// filed, referenced by nothing.
	uploaded, _ := uploadPNG(t, 21)

	runSweep(t, 0)

	assertBlobPresent(t, uploaded.Digest, false)
}

// Between the upload and the record being filed, an image is unreachable and
// looks exactly like garbage. The grace period is the only thing standing
// between a tester still typing their log and a sweep that runs meanwhile.
func TestSweepSparesBlobsInsideTheGracePeriod(t *testing.T) {
	uploaded, _ := uploadPNG(t, 22)

	runSweep(t, time.Hour)

	assertBlobPresent(t, uploaded.Digest, true)
}

// Deleting a record releases its references by cascade, which is what makes an
// expired record's images collectable on the next pass.
func TestSweepCollectsBlobsAfterTheirRecordExpires(t *testing.T) {
	uploaded, _ := uploadPNG(t, 23)
	repo := "org/sweep_expire_" + uuid.New().String()[:8]
	created := createWithLog(t, repo, "![shot]("+uploaded.Ref+")\n")

	// While the record is alive, so is its image.
	runSweep(t, 0)
	assertBlobPresent(t, uploaded.Digest, true)

	backdateEvidence(t, created.ID, 100*24*time.Hour)
	deleted := runRetention(t, mustParseRetentionConfig(t, `
interval: 1h
rules:
  - name: default
    match: {}
    max_age: 2160h
    priority: 0
`))
	require.GreaterOrEqual(t, deleted, 1)

	runSweep(t, 0)
	assertBlobPresent(t, uploaded.Digest, false)
}

// A blob two records share outlives the first of them.
func TestSweepKeepsBlobsAnotherRecordStillReferences(t *testing.T) {
	uploaded, _ := uploadPNG(t, 24)
	log := "![shot](" + uploaded.Ref + ")\n"
	first := createWithLog(t, "org/sweep_shared_a_"+uuid.New().String()[:8], log)
	createWithLog(t, "org/sweep_shared_b_"+uuid.New().String()[:8], log)

	_, err := testEvidenceStore.DeleteBatch(context.Background(), []uuid.UUID{first.ID})
	require.NoError(t, err)

	runSweep(t, 0)
	assertBlobPresent(t, uploaded.Digest, true)
}

// ---------------------------------------------------------------------------
// Tests: authorisation
// ---------------------------------------------------------------------------

func TestBlobUploadRequiresAWriteKey(t *testing.T) {
	srv := setupAuthServer(t, []config.APIKey{
		{Key: "rw-key", ReadOnly: false},
		{Key: "ro-key", ReadOnly: true},
	})
	defer srv.Close()

	body := samplePNG(t, 30)

	post := func(key string) int {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/blobs", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusCreated, post("rw-key"))
	// Uploading is a write, so a read-only key is refused by the same rule that
	// keeps it from filing evidence.
	assert.Equal(t, http.StatusForbidden, post("ro-key"))

	// Images are as readable as the records that embed them, and no more: an
	// unauthenticated fetch is refused even though the digest is unguessable.
	resp, err := http.Get(srv.URL + "/api/v1/blobs/" + string(blob.DigestOf(body)))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func createWithLog(t *testing.T, repo, log string) model.Evidence {
	t.Helper()
	body := map[string]any{
		"repo":          repo,
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "manual/brake-check",
		"evidence_type": "manual_test",
		"source":        "j.tester",
		"result":        "PASS",
		"finished_at":   "2026-03-30 14:00",
		"metadata":      map[string]any{"observations": log},
	}
	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	return decodeJSON[model.Evidence](t, resp)
}

func runSweep(t *testing.T, grace time.Duration) {
	t.Helper()
	cfg := mustParseRetentionConfig(t, "interval: 1h\nrules: []\n")
	worker, err := retention.NewWorker(cfg, testEvidenceStore, testInheritanceStore, testLogger)
	require.NoError(t, err)
	worker = worker.WithBlobs(testBlobStore, testBlobRefStore, grace)
	_, err = worker.SweepBlobs(context.Background())
	require.NoError(t, err)
}

func assertBlobPresent(t *testing.T, digest string, want bool) {
	t.Helper()
	resp, err := http.Get(testServer.URL + "/api/v1/blobs/" + digest)
	require.NoError(t, err)
	defer resp.Body.Close()

	if want {
		assert.Equal(t, http.StatusOK, resp.StatusCode, "blob %s should still be stored", digest)
		return
	}
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "blob %s should have been swept", digest)
}
