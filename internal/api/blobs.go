package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nesono/evidence-store/internal/blob"
)

// BlobHandler streams private media, with scoped playback URLs for browsers.
type BlobHandler struct {
	blobs      blob.Store
	maxBytes   int64
	signingKey []byte
}

func NewBlobHandler(blobs blob.Store, maxBytes int64, signingKey ...string) *BlobHandler {
	key := rand.Text()
	if len(signingKey) > 0 && signingKey[0] != "" {
		key = signingKey[0]
	}
	return &BlobHandler{blobs: blobs, maxBytes: maxBytes, signingKey: []byte(key)}
}

type blobResponse struct {
	Ref         string `json:"ref"`
	Digest      string `json:"digest"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Upload stores the request body and answers with the reference to write into a
// test log. Uploading the same image twice returns the same reference and costs
// one object.
func (h *BlobHandler) Upload(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, h.maxBytes)

	// The type has to be settled before anything is stored, and it is settled
	// from the bytes: the client's Content-Type is a claim, and this one gets
	// echoed back to a browser later.
	head := make([]byte, blob.SniffLen)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		h.writeReadError(w, err)
		return
	}
	head = head[:n]

	contentType, ext, err := blob.DetectMedia(head)
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType,
			"file contents were not recognized as a supported image or video (PNG, JPEG, WebP, GIF, MP4 or WebM); the filename extension alone is not sufficient")
		return
	}

	digest, size, err := h.blobs.Put(r.Context(), io.MultiReader(bytes.NewReader(head), body))
	if err != nil {
		h.writeReadError(w, err)
		return
	}

	ref := blob.Ref{Digest: digest, Ext: ext}
	writeJSON(w, http.StatusCreated, blobResponse{
		Ref:         ref.Path(),
		Digest:      string(digest),
		ContentType: contentType,
		Size:        size,
	})
}

// writeReadError separates the client running over the size cap from the store
// failing, which look the same at the call site but are a 413 and a 500.
func (h *BlobHandler) writeReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge,
			"media exceeds the maximum size of "+strconv.FormatInt(h.maxBytes, 10)+" bytes")
		return
	}
	slog.Error("failed to store blob", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// Get serves a blob's bytes.
func (h *BlobHandler) Get(w http.ResponseWriter, r *http.Request) {
	// The extension in the reference is a hint for the renderer, not part of the
	// name: the digest alone identifies the bytes.
	ref := chi.URLParam(r, "ref")
	if i := strings.LastIndex(ref, "."); i >= 0 {
		ref = ref[:i]
	}

	digest, err := blob.ParseDigest(ref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Content is immutable and named by its own hash, so a cached copy can never
	// be stale and the digest is the only ETag that makes sense.
	etag := `"` + string(digest) + `"`

	rc, _, err := h.blobs.Get(r.Context(), digest)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "blob not found")
			return
		}
		slog.Error("failed to read blob", "error", err, "digest", digest)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rc.Close()

	head := make([]byte, blob.SniffLen)
	n, err := io.ReadFull(rc, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		slog.Error("failed to read blob", "error", err, "digest", digest)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	head = head[:n]

	// Sniffed again on the way out rather than recorded at upload: the type is a
	// property of the bytes, and one answer is better than two that can drift.
	// Bytes that no longer sniff as an embeddable media are not served at all.
	contentType, _, err := blob.DetectMedia(head)
	if err != nil {
		slog.Error("stored blob is not a servable media", "digest", digest)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", contentType)

	w.Header().Set("ETag", etag)
	if strings.HasPrefix(r.URL.Path, "/media/") {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	// The type was decided here; a browser guessing a different one is exactly
	// the hole the allowlist exists to close.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")

	if _, err := rc.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "could not seek media")
		return
	}
	http.ServeContent(w, r, string(digest), time.Time{}, rc)
}

const playbackLifetime = 15 * time.Minute

func (h *BlobHandler) signature(ref, expires string) string {
	mac := hmac.New(sha256.New, h.signingKey)
	_, _ = io.WriteString(mac, ref+"\n"+expires)
	return hex.EncodeToString(mac.Sum(nil))
}

// PlaybackURL requires blob:read. The capability is bound to one exact ref and
// expires even if shared; it never grants upload or access to another blob.
func (h *BlobHandler) PlaybackURL(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	digestText, ext, hasExt := strings.Cut(ref, ".")
	if hasExt && ext != "png" && ext != "jpg" && ext != "webp" && ext != "gif" && ext != "mp4" && ext != "webm" {
		writeError(w, http.StatusBadRequest, "invalid media extension")
		return
	}
	digest, err := blob.ParseDigest(digestText)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid blob reference")
		return
	}
	if _, err := h.blobs.Stat(r.Context(), digest); err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "blob not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	expires := strconv.FormatInt(time.Now().Add(playbackLifetime).Unix(), 10)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": "/media/" + ref + "?expires=" + expires + "&token=" + h.signature(ref, expires)})
}

func (h *BlobHandler) Playback(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	expires := r.URL.Query().Get("expires")
	deadline, err := strconv.ParseInt(expires, 10, 64)
	supplied, decodeErr := hex.DecodeString(r.URL.Query().Get("token"))
	expected, _ := hex.DecodeString(h.signature(ref, expires))
	if err != nil || decodeErr != nil || deadline <= time.Now().Unix() || !hmac.Equal(supplied, expected) {
		writeError(w, http.StatusForbidden, "playback link expired or invalid")
		return
	}
	// Do not let a browser reuse protected media beyond the capability lifetime.
	w.Header().Set("Referrer-Policy", "no-referrer")
	h.Get(w, r)
}
