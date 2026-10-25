package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/campaign"
	"github.com/nesono/evidence-store/internal/store"
)

type CampaignHandler struct {
	store *store.CampaignStore
	blobs blob.Store
}

func NewCampaignHandler(s *store.CampaignStore, blobs blob.Store) *CampaignHandler {
	return &CampaignHandler{store: s, blobs: blobs}
}

func (h *CampaignHandler) Import(w http.ResponseWriter, r *http.Request) {
	if h.blobs == nil {
		writeError(w, http.StatusServiceUnavailable, "blob storage is unavailable")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/gzip" && mediaType != "application/octet-stream") {
		writeError(w, http.StatusUnsupportedMediaType, "send the raw archive as application/gzip or application/octet-stream")
		return
	}
	data := readCampaignBody(w, r)
	if data == nil {
		return
	}
	p, assets, err := campaign.ReadPackageAssets(bytes.NewReader(data))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	subject := "anonymous"
	if principal, ok := auth.PrincipalFrom(r.Context()); ok {
		subject = principal.Subject
	}
	row, created, err := h.store.Import(r.Context(), h.blobs, data, p, assets, subject)
	if err != nil {
		campaignError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Location", "/api/v1/campaigns/"+row.ID.String())
	writeJSON(w, status, row)
}

func readCampaignBody(w http.ResponseWriter, r *http.Request) []byte {
	r.Body = http.MaxBytesReader(w, r.Body, campaign.MaxArchiveBytes)
	data, err := io.ReadAll(r.Body)
	if err == nil {
		return data
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "campaign archive exceeds 16 MiB")
		return nil
	}
	writeError(w, http.StatusBadRequest, "could not read archive")
	return nil
}

func (h *CampaignHandler) Get(w http.ResponseWriter, r *http.Request) {
	row := h.lookup(w, r)
	if row != nil {
		writeJSON(w, http.StatusOK, row)
	}
}

func (h *CampaignHandler) lookup(w http.ResponseWriter, r *http.Request) *store.CampaignPackage {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid campaign id")
		return nil
	}
	row, err := h.store.Get(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "campaign not found")
		return nil
	}
	if err != nil {
		campaignError(w, err)
		return nil
	}
	return row
}

func (h *CampaignHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := campaignPageNumber(r, "limit", 50, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 100")
		return
	}
	offset, err := campaignPageNumber(r, "offset", 0, 0, 1000000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "offset must be an integer between 0 and 1000000")
		return
	}
	rows, err := h.store.List(r.Context(), limit+1, offset)
	if err != nil {
		campaignError(w, err)
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": rows, "limit": limit, "offset": offset, "has_more": hasMore})
}

func campaignPageNumber(r *http.Request, name string, fallback, min, max int) (int, error) {
	values, exists := r.URL.Query()[name]
	if !exists {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, errors.New("repeated pagination parameter")
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n < min || n > max {
		return 0, errors.New("invalid pagination parameter")
	}
	return n, nil
}

func (h *CampaignHandler) Download(w http.ResponseWriter, r *http.Request) {
	row := h.lookup(w, r)
	if row == nil {
		return
	}
	if h.blobs == nil {
		writeError(w, http.StatusServiceUnavailable, "blob storage is unavailable")
		return
	}
	digest, err := blob.ParseDigest(row.Digest)
	if err != nil {
		campaignError(w, err)
		return
	}
	f, _, err := h.blobs.Get(r.Context(), digest)
	if err != nil {
		campaignError(w, err) // Missing CAS for an existing row is a storage failure.
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="campaign.tar.gz"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+row.Digest+`"`)
	http.ServeContent(w, r, "campaign.tar.gz", time.Time{}, f)
}

func campaignError(w http.ResponseWriter, err error) {
	slog.Error("campaign storage operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
