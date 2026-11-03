package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nesono/evidence-store/internal/store"
)

func (h *CampaignHandler) CreateExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        uuid.UUID `json:"id"`
		PackageID uuid.UUID `json:"package_id"`
		Name      string    `json:"name"`
		Repo      string    `json:"repo"`
		Branch    string    `json:"branch"`
		RCSRef    string    `json:"rcs_ref"`
	}
	if !readAttemptJSON(w, r, &req) {
		return
	}
	e, fresh, err := h.store.CreateExecution(r.Context(), store.CampaignExecution{ID: req.ID, PackageID: req.PackageID, Name: req.Name, Repo: req.Repo, Branch: req.Branch, RCSRef: req.RCSRef, CreatedBy: attemptOwner(r)})
	if err != nil {
		attemptError(w, err)
		return
	}
	status := 200
	if fresh {
		status = 201
	}
	writeJSON(w, status, e)
}

func (h *CampaignHandler) ListExecutions(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "completed" && state != "all" {
		writeError(w, 400, "state must be open, completed or all")
		return
	}
	limit, err := campaignPageNumber(r, "limit", 25, 1, 100)
	if err != nil {
		writeError(w, 400, "invalid limit")
		return
	}
	offset, err := campaignPageNumber(r, "offset", 0, 0, 1000000)
	if err != nil {
		writeError(w, 400, "invalid offset")
		return
	}
	query := r.URL.Query().Get("q")
	if len(query) > 200 {
		writeError(w, 400, "search is limited to 200 bytes")
		return
	}
	rows, err := h.store.ListExecutions(r.Context(), state, query, limit+1, offset)
	if err != nil {
		attemptError(w, err)
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	writeJSON(w, 200, map[string]any{"campaigns": rows, "has_more": more, "offset": offset})
}

func (h *CampaignHandler) GetExecution(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid campaign id")
		return
	}
	row, err := h.store.GetExecution(r.Context(), id)
	if err != nil {
		attemptError(w, err)
		return
	}
	writeJSON(w, 200, row)
}

func (h *CampaignHandler) SetExecutionClosed(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid campaign id")
		return
	}
	var req struct {
		Revision int64 `json:"revision"`
		Closed   *bool `json:"closed"`
	}
	if !readAttemptJSON(w, r, &req) {
		return
	}
	if req.Closed == nil || req.Revision < 1 {
		writeError(w, 422, "revision and closed are required")
		return
	}
	row, err := h.store.SetExecutionClosed(r.Context(), id, req.Revision, *req.Closed, attemptOwner(r))
	if err != nil {
		attemptError(w, err)
		return
	}
	writeJSON(w, 200, row)
}
