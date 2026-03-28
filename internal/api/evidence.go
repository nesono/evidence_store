package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
	"github.com/nesono/evidence-store/internal/validate"
)

type EvidenceHandler struct {
	evidence    *store.EvidenceStore
	inheritance *store.InheritanceStore
	cfg         *config.Config
}

func NewEvidenceHandler(es *store.EvidenceStore, is *store.InheritanceStore, cfg *config.Config) *EvidenceHandler {
	return &EvidenceHandler{evidence: es, inheritance: is, cfg: cfg}
}

func (h *EvidenceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req model.EvidenceCreate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if errs := validate.EvidenceCreate(&req); len(errs) > 0 {
		writeErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	ev, err := h.evidence.Insert(r.Context(), &req)
	if err != nil {
		slog.Error("failed to insert evidence", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, ev)
}

func (h *EvidenceHandler) CreateBatch(w http.ResponseWriter, r *http.Request) {
	var req model.BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Records) == 0 {
		writeError(w, http.StatusBadRequest, "records array is empty")
		return
	}
	if len(req.Records) > h.cfg.MaxBatchSize {
		writeError(w, http.StatusBadRequest, "batch size exceeds maximum")
		return
	}

	// Validate all records, separate valid from invalid.
	var validRecords []model.EvidenceCreate
	var validIndices []int
	results := make([]model.BatchRecordStatus, len(req.Records))
	hasErrors := false

	for i, rec := range req.Records {
		if errs := validate.EvidenceCreate(&rec); len(errs) > 0 {
			results[i] = model.BatchRecordStatus{
				Index:  i,
				Status: "error",
				Error:  strings.Join(errs, "; "),
			}
			hasErrors = true
		} else {
			validRecords = append(validRecords, rec)
			validIndices = append(validIndices, i)
		}
	}

	// Insert all valid records.
	if len(validRecords) > 0 {
		inserted, err := h.evidence.InsertBatch(r.Context(), validRecords)
		if err != nil {
			slog.Error("failed to batch insert evidence", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		for j, ev := range inserted {
			idx := validIndices[j]
			results[idx] = model.BatchRecordStatus{
				Index:  idx,
				ID:     ev.ID,
				Status: "created",
			}
		}
	}

	status := http.StatusCreated
	if hasErrors {
		status = http.StatusMultiStatus
	}

	writeJSON(w, status, model.BatchResponse{Results: results})
}

func (h *EvidenceHandler) Get(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	ev, err := h.evidence.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "evidence not found")
			return
		}
		slog.Error("failed to get evidence", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, ev)
}

func (h *EvidenceHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := model.EvidenceFilter{}
	if v := q.Get("repo"); v != "" {
		filter.Repo = &v
	}
	if v := q.Get("rcs_ref"); v != "" {
		filter.RCSRef = &v
	}
	if v := q.Get("branch"); v != "" {
		filter.Branch = &v
	}
	if v := q.Get("evidence_type"); v != "" {
		filter.EvidenceType = &v
	}
	if v := q.Get("source"); v != "" {
		filter.Source = &v
	}
	if v := q.Get("procedure_ref"); v != "" {
		filter.ProcedureRef = &v
	}
	if v := q.Get("result"); v != "" {
		for _, s := range strings.Split(v, ",") {
			result, err := model.ParseEvidenceResult(strings.TrimSpace(s))
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			filter.Result = append(filter.Result, result)
		}
	}
	if v := q.Get("finished_after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid finished_after: "+err.Error())
			return
		}
		filter.FinishedAfter = &t
	}
	if v := q.Get("finished_before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid finished_before: "+err.Error())
			return
		}
		filter.FinishedBefore = &t
	}
	if v := q.Get("tags"); v != "" {
		filter.Tags = strings.Split(v, ",")
	}

	limit := h.cfg.DefaultPageSize
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= h.cfg.MaxPageSize {
			limit = n
		}
	}

	var cursor *store.Cursor
	if v := q.Get("cursor"); v != "" {
		c, err := store.DecodeCursor(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cursor = c
	}

	params := store.ListParams{
		Filter: filter,
		Cursor: cursor,
		Limit:  limit,
	}

	// Check if inheritance should be included.
	includeInherited := q.Get("include_inherited") != "false"

	result, err := h.evidence.List(r.Context(), params)
	if err != nil {
		slog.Error("failed to list evidence", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Build response with inheritance annotation.
	response := struct {
		Records    []model.EvidenceResponse `json:"records"`
		NextCursor *string                  `json:"next_cursor,omitempty"`
	}{
		NextCursor: result.NextCursor,
	}

	inherited := false
	for _, ev := range result.Records {
		response.Records = append(response.Records, model.EvidenceResponse{Evidence: ev, Inherited: &inherited})
	}

	// Resolve inheritance if requested and rcs_ref filter is set.
	if includeInherited && filter.RCSRef != nil && filter.Repo != nil {
		declarations, err := h.inheritance.FindForTarget(r.Context(), *filter.Repo, *filter.RCSRef)
		if err != nil {
			slog.Error("failed to find inheritance declarations", "error", err)
			// Continue without inheritance rather than failing the whole request.
		} else {
			for _, decl := range declarations {
				inheritedFilter := filter
				inheritedFilter.RCSRef = &decl.SourceRCSRef

				inheritedParams := store.ListParams{
					Filter: inheritedFilter,
					Limit:  h.cfg.MaxPageSize,
				}

				inheritedResult, err := h.evidence.List(r.Context(), inheritedParams)
				if err != nil {
					slog.Error("failed to list inherited evidence", "error", err)
					continue
				}

				isInherited := true
				declID := decl.ID
				for _, ev := range inheritedResult.Records {
					response.Records = append(response.Records, model.EvidenceResponse{
						Evidence:               ev,
						Inherited:              &isInherited,
						InheritanceDeclaration: &declID,
					})
				}
			}
		}
	}

	if response.Records == nil {
		response.Records = []model.EvidenceResponse{}
	}

	writeJSON(w, http.StatusOK, response)
}
