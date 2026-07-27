package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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

// Distinct returns up to `limit` distinct values of a whitelisted field
// (`repo`, `evidence_type`, `source`), optionally filtered by `q` substring.
// Powers the editable combo boxes in the UI.
func (h *EvidenceHandler) Distinct(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	field := q.Get("field")
	if field == "" {
		writeError(w, http.StatusBadRequest, "field parameter is required")
		return
	}

	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	values, err := h.evidence.Distinct(r.Context(), field, q.Get("q"), limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string][]string{"values": values})
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

	filter, err := parseEvidenceFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
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

	// Offset and cursor are separate pagination modes. Silently honouring one
	// would show the caller a different window than the one they asked for.
	offset := 0
	if v := q.Get("offset"); v != "" {
		if cursor != nil {
			writeError(w, http.StatusBadRequest, "offset and cursor cannot be combined")
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}

	// Keyset pagination is tied to the (ingested_at, id) ordering, so re-sorting
	// mid-walk would skip or repeat records.
	sort := q.Get("sort")
	if sort != "" {
		if cursor != nil {
			writeError(w, http.StatusBadRequest, "sort and cursor cannot be combined; page with offset instead")
			return
		}
		if !store.IsSortable(sort) {
			writeError(w, http.StatusBadRequest, "cannot sort by "+sort)
			return
		}
	}

	desc := false
	switch order := q.Get("order"); order {
	case "", "asc":
	case "desc":
		desc = true
	default:
		writeError(w, http.StatusBadRequest, `order must be "asc" or "desc"`)
		return
	}

	// Counting is the expensive part of the query, so callers walking a result set
	// can fetch the total once and opt out on subsequent windows. Cursor mode never
	// reports a total, preserving the previous behaviour.
	withTotal := cursor == nil && q.Get("include_total") != "false"

	params := store.ListParams{
		Filter:    filter,
		Cursor:    cursor,
		Limit:     limit,
		Offset:    offset,
		Sort:      sort,
		Desc:      desc,
		WithTotal: withTotal,
	}

	// Check if inheritance should be included.
	includeInherited := q.Get("include_inherited") != "false"

	result, err := h.evidence.List(r.Context(), params)
	if err != nil {
		slog.Error("failed to list evidence", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Build response with inheritance annotation. Inherited records are reported
	// separately: they are resolved outside the paginated window, so mixing them
	// into `records` would make the window's length disagree with `limit` and its
	// position disagree with `total`.
	response := struct {
		Records          []model.EvidenceResponse `json:"records"`
		InheritedRecords []model.EvidenceResponse `json:"inherited_records,omitempty"`
		NextCursor       *string                  `json:"next_cursor,omitempty"`
		Total            *int64                   `json:"total,omitempty"`
	}{
		NextCursor: result.NextCursor,
		Total:      result.Total,
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
					response.InheritedRecords = append(response.InheritedRecords, model.EvidenceResponse{
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
