package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nesono/evidence-store/internal/blob"
)

func videoBytes() []byte {
	return append([]byte("\x00\x00\x00\x18ftypiso4\x00\x00\x00\x01iso4hvc1"), bytes.Repeat([]byte{42}, 4096)...)
}

func TestVideoUploadAndScopedPlayback(t *testing.T) {
	store, err := blob.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewBlobHandler(store, 8192)
	router := chi.NewRouter()
	router.Post("/api/v1/blobs", h.Upload)
	router.Get("/api/v1/blobs/{ref}/url", h.PlaybackURL)
	router.Get("/media/{ref}", h.Playback)
	router.Head("/media/{ref}", h.Playback)
	body := videoBytes()
	request := func(method, path, ranged string, data io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, data)
		if ranged != "" {
			r.Header.Set("Range", ranged)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	upload := request("POST", "/api/v1/blobs", "", bytes.NewReader(body))
	if upload.Code != 201 {
		t.Fatal(upload.Code, upload.Body.String())
	}
	var result blobResponse
	if err := json.Unmarshal(upload.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "video/mp4" || !strings.HasSuffix(result.Ref, ".mp4") {
		t.Fatal(result)
	}
	response := request("GET", result.Ref+"/url", "", nil)
	var link map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, rangeValue string
		status             int
		want               []byte
	}{
		{"GET", "", 200, body},
		{"GET", "bytes=1000-1099", 206, body[1000:1100]},
		{"GET", "bytes=-12", 206, body[len(body)-12:]},
		{"HEAD", "", 200, nil},
		{"GET", "bytes=9000-", 416, nil},
	} {
		w := request(tc.method, link["url"], tc.rangeValue, nil)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.rangeValue, w.Code, w.Body.String())
		}
		if tc.status != 416 && !bytes.Equal(w.Body.Bytes(), tc.want) {
			t.Fatalf("wrong range body: %s", tc.rangeValue)
		}
		if tc.status < 400 && w.Header().Get("Accept-Ranges") != "bytes" {
			t.Fatal("missing ranges")
		}
	}
	for _, path := range []string{
		"/media/" + result.Digest + ".mp4",
		strings.Replace(link["url"], ".mp4", ".webm", 1),
		link["url"] + "0",
	} {
		if w := request("GET", path, "", nil); w.Code != 403 {
			t.Fatalf("accepted invalid capability: %s", path)
		}
	}
	ref := result.Digest + ".mp4"
	expired := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	path := "/media/" + ref + "?expires=" + expired + "&token=" + h.signature(ref, expired)
	if w := request("GET", path, "", nil); w.Code != 403 {
		t.Fatal("accepted expired token")
	}
	tooBig := request("POST", "/api/v1/blobs", "", bytes.NewReader(append(body, body...)))
	if tooBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatal(tooBig.Code)
	}
	count := 0
	if err := store.List(context.Background(), func(blob.Object) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("failed upload left an object", count)
	}
}
