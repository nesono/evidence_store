package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nesono/evidence-store/internal/blob"
)

// BlobHandler serves the images a test log embeds.
//
// Blobs are proxied through the API rather than handed out as presigned URLs so
// that the object store stays private and there is one place where access is
// decided — the same API key that reads a record reads its photos. When video
// arrives (#79) that trade will need revisiting, because streaming a large file
// through the app server is a different proposition than serving a screenshot.
type BlobHandler struct {
	blobs    blob.Store
	maxBytes int64
}

func NewBlobHandler(blobs blob.Store, maxBytes int64) *BlobHandler {
	return &BlobHandler{blobs: blobs, maxBytes: maxBytes}
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
			"unsupported image type: a test log can embed PNG, JPEG, WebP or GIF")
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
			"image exceeds the maximum size of "+strconv.FormatInt(h.maxBytes, 10)+" bytes")
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
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, string(digest)) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	rc, size, err := h.blobs.Get(r.Context(), digest)
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
	// Bytes that no longer sniff as an embeddable image are not served at all.
	contentType, _, err := blob.DetectMedia(head)
	if err != nil {
		slog.Error("stored blob is not a servable image", "digest", digest)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	// The type was decided here; a browser guessing a different one is exactly
	// the hole the allowlist exists to close.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")

	if _, err := w.Write(head); err != nil {
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		slog.Error("failed to write blob", "error", err, "digest", digest)
	}
}
