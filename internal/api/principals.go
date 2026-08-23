package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
	"github.com/nesono/evidence-store/internal/validate"
)

// PrincipalHandler administers the identities that authenticate against this
// store: who exists, what they may do, and whether their credential still
// works. Every route below it requires principal:admin.
//
// Notably absent is a delete. Revocation is a timestamp, not a DELETE, so that
// evidence already attributed to a principal still names something a reader can
// look up — and so an administrator can tell a credential that was taken away
// from one that never existed.
type PrincipalHandler struct {
	store *store.PrincipalStore
	// authDB reports whether the principals table is actually consulted at
	// authentication time. Keys can be issued before EVIDENCE_AUTH_DB is turned
	// on — that is a reasonable way to prepare a cutover — but an operator
	// handing one out deserves to know it will not work yet.
	authDB bool
}

func NewPrincipalHandler(s *store.PrincipalStore, authDB bool) *PrincipalHandler {
	return &PrincipalHandler{store: s, authDB: authDB}
}

// issuedPrincipal is the response to minting a credential. The key appears here
// and nowhere else, ever: only its digest is stored, so this response is the
// one and only chance to read it.
type issuedPrincipal struct {
	Principal *model.Principal `json:"principal"`
	APIKey    string           `json:"api_key"`
}

func (h *PrincipalHandler) List(w http.ResponseWriter, r *http.Request) {
	principals, err := h.store.List(r.Context())
	if err != nil {
		slog.Error("failed to list principals", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"principals":      principals,
		"auth_db_enabled": h.authDB,
	})
}

type principalCreateRequest struct {
	Subject     string   `json:"subject"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
}

func (h *PrincipalHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req principalCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	create := model.PrincipalCreate{
		Subject:     req.Subject,
		Kind:        model.PrincipalKindAPIKey,
		DisplayName: req.DisplayName,
		Roles:       req.Roles,
		GrantedBy:   callerID(r),
	}
	if errs := validate.PrincipalCreate(&create); len(errs) > 0 {
		writeErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	key, err := auth.GenerateKey()
	if err != nil {
		slog.Error("failed to generate api key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	create.KeyHash = auth.HashKey(key)

	created, err := h.store.Insert(r.Context(), create)
	if err != nil {
		slog.Error("failed to create principal", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if created == nil {
		writeError(w, http.StatusConflict, "a principal with subject "+req.Subject+" already exists")
		return
	}

	slog.Info("principal created", "subject", created.Subject, "roles", created.Roles,
		"by", callerSubject(r))
	writeJSON(w, http.StatusCreated, issuedPrincipal{Principal: created, APIKey: key})
}

// Disable revokes a credential. It takes effect on the principal's next
// request, which is the thing an environment variable full of shared secrets
// could never do.
func (h *PrincipalHandler) Disable(w http.ResponseWriter, r *http.Request) {
	h.setDisabled(w, r, true)
}

// Enable restores a revoked principal, with the roles it had. Undoing a
// revocation should not mean reissuing a key and re-granting roles.
func (h *PrincipalHandler) Enable(w http.ResponseWriter, r *http.Request) {
	h.setDisabled(w, r, false)
}

func (h *PrincipalHandler) setDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	target, ok := h.lookup(w, r)
	if !ok {
		return
	}

	if disabled {
		if err := h.refuseIfLastAdmin(r, target, nil); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}

	updated, err := h.store.SetDisabled(r.Context(), target.ID, disabled)
	if err != nil {
		slog.Error("failed to set principal disabled", "error", err, "disabled", disabled)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("principal access changed", "subject", target.Subject, "disabled", disabled,
		"by", callerSubject(r))
	writeJSON(w, http.StatusOK, updated)
}

type rolesRequest struct {
	Roles []string `json:"roles"`
}

// ReplaceRoles sets a principal's grants to exactly what was sent. A PUT rather
// than a pair of grant and revoke calls because that is what an administrator
// editing a set of checkboxes means, and because two admins submitting the same
// intent should not fight.
func (h *PrincipalHandler) ReplaceRoles(w http.ResponseWriter, r *http.Request) {
	target, ok := h.lookup(w, r)
	if !ok {
		return
	}

	var req rolesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Roles == nil {
		req.Roles = []string{}
	}
	if errs := validate.Roles(req.Roles); len(errs) > 0 {
		writeErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	if err := h.refuseIfLastAdmin(r, target, req.Roles); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	updated, err := h.store.ReplaceRoles(r.Context(), target.ID, req.Roles, callerID(r))
	if err != nil {
		slog.Error("failed to replace principal roles", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("principal roles changed", "subject", target.Subject, "roles", req.Roles,
		"by", callerSubject(r))
	writeJSON(w, http.StatusOK, updated)
}

// Rotate issues a fresh key for an existing principal and invalidates the old
// one immediately. It is the answer to a leaked or mislaid key: the identity,
// its roles, and everything already filed under its name survive, which
// disabling the principal and creating another would not.
func (h *PrincipalHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	target, ok := h.lookup(w, r)
	if !ok {
		return
	}

	key, err := auth.GenerateKey()
	if err != nil {
		slog.Error("failed to generate api key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	updated, err := h.store.RotateKey(r.Context(), target.ID, auth.HashKey(key))
	if err != nil {
		slog.Error("failed to rotate principal key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if updated == nil {
		writeError(w, http.StatusConflict, "only api_key principals have a key to rotate")
		return
	}

	slog.Info("principal key rotated", "subject", target.Subject, "by", callerSubject(r))
	writeJSON(w, http.StatusOK, issuedPrincipal{Principal: updated, APIKey: key})
}

// lookup resolves the {id} path parameter, answering the request itself when it
// cannot.
func (h *PrincipalHandler) lookup(w http.ResponseWriter, r *http.Request) (*model.Principal, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid principal id")
		return nil, false
	}
	target, err := h.store.FindByID(r.Context(), id)
	if err != nil {
		slog.Error("failed to look up principal", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "principal not found")
		return nil, false
	}
	return target, true
}

// refuseIfLastAdmin blocks the two operations that could leave a deployment
// with no administrator and no way back in but psql: disabling the last enabled
// admin, and taking the admin role off them. nextRoles is what the principal
// would hold afterwards, or nil for a disable.
//
// The check is about the store rather than about the caller, so it catches an
// administrator disabling themselves — one click in the web UI — as well as one
// locking out a colleague.
func (h *PrincipalHandler) refuseIfLastAdmin(r *http.Request, target *model.Principal, nextRoles []string) error {
	if target.Disabled() || !hasRole(target.Roles, string(auth.RoleAdmin)) {
		// Not currently an enabled administrator, so this change cannot be what
		// removes the last one.
		return nil
	}
	if nextRoles != nil && hasRole(nextRoles, string(auth.RoleAdmin)) {
		return nil
	}

	others, err := h.store.CountOtherEnabledAdmins(r.Context(), target.ID)
	if err != nil {
		return err
	}
	if others > 0 {
		return nil
	}
	return errLastAdmin{subject: target.Subject}
}

type errLastAdmin struct{ subject string }

func (e errLastAdmin) Error() string {
	return e.subject + " is the only principal that can still administer this store; " +
		"grant admin to another principal first"
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func callerID(r *http.Request) *uuid.UUID {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok || p.ID == uuid.Nil {
		// An environment-variable key is a secret rather than a row, so there
		// is nobody to credit with the grant.
		return nil
	}
	id := p.ID
	return &id
}

func callerSubject(r *http.Request) string {
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		return p.Subject
	}
	return "unauthenticated"
}
