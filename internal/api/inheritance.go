package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
	"github.com/nesono/evidence-store/internal/validate"
)

type InheritanceHandler struct {
	store *store.InheritanceStore
}

func NewInheritanceHandler(s *store.InheritanceStore) *InheritanceHandler {
	return &InheritanceHandler{store: s}
}

func (h *InheritanceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req model.InheritanceCreate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if errs := validate.InheritanceCreate(&req); len(errs) > 0 {
		writeErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	decl, err := h.store.Insert(r.Context(), &req)
	if err != nil {
		slog.Error("failed to insert inheritance declaration", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, decl)
}

func (h *InheritanceHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := model.InheritanceFilter{}
	if v := q.Get("repo"); v != "" {
		filter.Repo = &v
	}
	if v := q.Get("source_rcs_ref"); v != "" {
		filter.SourceRCSRef = &v
	}
	if v := q.Get("target_rcs_ref"); v != "" {
		filter.TargetRCSRef = &v
	}

	declarations, err := h.store.List(r.Context(), filter)
	if err != nil {
		slog.Error("failed to list inheritance declarations", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if declarations == nil {
		declarations = []model.InheritanceDeclaration{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"declarations": declarations})
}
