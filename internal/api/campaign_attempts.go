package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/campaign"
	"github.com/nesono/evidence-store/internal/store"
)

func attemptOwner(r *http.Request) string {
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		return p.Subject
	}
	return "anonymous"
}

func readAttemptJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(w, 413, "draft exceeds 1 MiB")
		} else {
			writeError(w, 400, "cannot read draft")
		}
		return false
	}
	if err := campaign.StrictJSON(data, target); err != nil {
		writeError(w, 400, "invalid draft JSON: "+err.Error())
		return false
	}
	return true
}

func (h *CampaignHandler) CreateAttempt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID           uuid.UUID `json:"id"`
		CampaignID   uuid.UUID `json:"campaign_id"`
		ProcedureRef string    `json:"procedure_ref"`
	}
	if !readAttemptJSON(w, r, &req) {
		return
	}
	if req.ID == uuid.Nil || req.CampaignID == uuid.Nil {
		writeError(w, 422, "id and campaign_id must be nonzero UUIDs")
		return
	}
	procedure := h.attemptProcedure(w, r, req.CampaignID, req.ProcedureRef)
	if procedure == nil {
		return
	}
	a, created, err := h.store.CreateAttempt(r.Context(), req.ID, req.CampaignID, req.ProcedureRef, attemptOwner(r), campaign.InitialDraft(procedure))
	if err != nil {
		attemptError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, a)
}

func (h *CampaignHandler) attemptProcedure(w http.ResponseWriter, r *http.Request, id uuid.UUID, ref string) *campaign.Procedure {
	row, err := h.store.Get(r.Context(), id)
	if err != nil {
		attemptError(w, err)
		return nil
	}
	for _, s := range row.Definition.Scenarios {
		if s.Definition.ProcedureRef == ref {
			return s.Procedure
		}
	}
	writeError(w, 422, "procedure_ref is not in this campaign package")
	return nil
}

func (h *CampaignHandler) ownedAttempt(w http.ResponseWriter, r *http.Request) *store.CampaignAttempt {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid attempt id")
		return nil
	}
	a, err := h.store.GetAttempt(r.Context(), id, attemptOwner(r))
	if err != nil {
		attemptError(w, err)
		return nil
	}
	return a
}

func (h *CampaignHandler) GetAttempt(w http.ResponseWriter, r *http.Request) {
	if a := h.ownedAttempt(w, r); a != nil {
		writeJSON(w, 200, a)
	}
}

func (h *CampaignHandler) SaveAttempt(w http.ResponseWriter, r *http.Request) {
	a := h.ownedAttempt(w, r)
	if a == nil {
		return
	}
	var req struct {
		Revision    int64              `json:"revision"`
		OperationID uuid.UUID          `json:"operation_id"`
		Data        campaign.DraftData `json:"data"`
	}
	if !readAttemptJSON(w, r, &req) {
		return
	}
	if req.Revision < 1 || req.OperationID == uuid.Nil {
		writeError(w, 422, "revision and nonzero operation_id are required")
		return
	}
	procedure := h.attemptProcedure(w, r, a.CampaignID, a.ProcedureRef)
	if procedure == nil {
		return
	}
	if err := campaign.ValidateDraft(procedure, req.Data); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	saved, err := h.store.SaveAttempt(r.Context(), a.ID, attemptOwner(r), req.Revision, req.OperationID, req.Data)
	if err != nil {
		attemptError(w, err)
		return
	}
	writeJSON(w, 200, saved)
}

func (h *CampaignHandler) ListAttempts(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.URL.Query().Get("campaign_id"))
	if err != nil {
		writeError(w, 400, "campaign_id is required and must be a UUID")
		return
	}
	ref := r.URL.Query().Get("procedure_ref")
	if ref == "" {
		writeError(w, 400, "procedure_ref is required")
		return
	}
	offset, err := campaignPageNumber(r, "offset", 0, 0, 1000000)
	if err != nil {
		writeError(w, 400, "invalid offset")
		return
	}
	rows, err := h.store.ListAttempts(r.Context(), id, ref, attemptOwner(r), offset)
	if err != nil {
		attemptError(w, err)
		return
	}
	more := len(rows) > 25
	if more {
		rows = rows[:25]
	}
	writeJSON(w, 200, map[string]any{"attempts": rows, "has_more": more, "offset": offset})
}

func attemptError(w http.ResponseWriter, err error) {
	var invalid *store.FinalizationError
	if errors.As(err, &invalid) {
		writeError(w, 422, invalid.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "attempt or campaign not found")
		return
	}
	if errors.Is(err, store.ErrAttemptConflict) {
		writeError(w, 409, "attempt changed or is finalized; reload the saved attempt")
		return
	}
	campaignError(w, err)
}

func (h *CampaignHandler) FinalizeAttempt(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid attempt id")
		return
	}
	var req struct {
		Revision   int64     `json:"revision"`
		FinishedAt time.Time `json:"finished_at"`
	}
	if !readAttemptJSON(w, r, &req) {
		return
	}
	if req.Revision < 1 || req.FinishedAt.IsZero() {
		writeError(w, 422, "revision and finished_at (RFC3339) are required")
		return
	}
	// PostgreSQL timestamps are microsecond-precision; normalize before both
	// comparison and insertion so nanosecond RFC3339 requests remain retryable.
	a, created, err := h.store.FinalizeAttempt(r.Context(), id, attemptOwner(r), req.Revision, req.FinishedAt.UTC().Truncate(time.Microsecond))
	if err != nil {
		attemptError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	if a.EvidenceID != nil {
		w.Header().Set("Location", "/api/v1/evidence/"+a.EvidenceID.String())
	}
	writeJSON(w, status, a)
}
