package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
)

// SCIMHandler speaks SCIM 2.0 to a provisioning client — Entra, in the case
// this was built for.
//
// It is deliberately not part of /api/v1. SCIM is somebody else's protocol: its
// own resource shapes, its own error envelope, its own opinion about status
// codes. Translating it into this store's REST conventions would mean a client
// that follows the specification getting answers it cannot read.
//
// What it can do is narrow. It provisions people and it deactivates them; it
// cannot read evidence, cannot grant roles beyond what group membership implies
// (phase 3), and cannot delete anything.
type SCIMHandler struct {
	store  *store.SCIMStore
	logger *slog.Logger
}

// Sessions are not a dependency here: ending them is part of deactivating
// somebody and happens in the same transaction, because an account is not shut
// while a browser still holds a live session for it.
func NewSCIMHandler(s *store.SCIMStore, logger *slog.Logger) *SCIMHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SCIMHandler{store: s, logger: logger}
}

// SCIM schema URNs. Spelled out rather than abbreviated because a client
// matches on them exactly.
const (
	schemaUser         = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaListResponse = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaPatchOp      = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	schemaError        = "urn:ietf:params:scim:api:messages:2.0:Error"
)

// defaultPageSize is what a listing returns when the client does not say. The
// cap exists because count comes from the network and a directory asking for
// everything at once should not decide this process's memory use.
const (
	defaultPageSize = 100
	maxPageSize     = 500
)

// scimUser is one resource on the wire.
type scimUser struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id"`
	ExternalID  string      `json:"externalId,omitempty"`
	UserName    string      `json:"userName"`
	DisplayName string      `json:"displayName,omitempty"`
	Name        *scimName   `json:"name,omitempty"`
	Emails      []scimEmail `json:"emails,omitempty"`
	Active      bool        `json:"active"`
	Meta        scimMeta    `json:"meta"`
}

type scimName struct {
	Formatted  string `json:"formatted,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

type scimEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type,omitempty"`
}

type scimMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created"`
	LastModified string `json:"lastModified"`
	Location     string `json:"location"`
}

func (h *SCIMHandler) resource(u *model.SCIMUser) scimUser {
	out := scimUser{
		Schemas:     []string{schemaUser},
		ID:          u.SCIMID,
		ExternalID:  u.ExternalID,
		UserName:    u.UserName,
		DisplayName: u.DisplayName,
		Active:      u.Active,
		Meta: scimMeta{
			ResourceType: "User",
			Created:      u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			LastModified: u.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Location:     "/scim/v2/Users/" + u.SCIMID,
		},
	}
	// The address is echoed back from the subject, which is where this store
	// keeps it: a directory that sent an email expects to read one back, and
	// this is the same string a login will file evidence under.
	if email := strings.TrimPrefix(u.Subject, "user:"); email != "" && email != u.Subject {
		out.Emails = []scimEmail{{Value: email, Primary: true, Type: "work"}}
	}
	return out
}

// --- Requests ---

// scimUserRequest is a resource as a client sends it. Everything is optional on
// the wire, because PATCH sends fragments and providers disagree about which of
// displayName and name.formatted they populate.
type scimUserRequest struct {
	UserName    string      `json:"userName"`
	ExternalID  string      `json:"externalId"`
	DisplayName string      `json:"displayName"`
	Name        *scimName   `json:"name"`
	Emails      []scimEmail `json:"emails"`
	// Active is a pointer so that "absent" and "false" stay distinguishable. A
	// PUT that omits it must not read as a deactivation.
	Active *bool `json:"active"`
}

// write turns a request into what the store takes, filling in the parts a
// provider may have expressed differently.
func (r scimUserRequest) write() store.SCIMUserWrite {
	active := true
	if r.Active != nil {
		active = *r.Active
	}
	return store.SCIMUserWrite{
		UserName:    r.UserName,
		ExternalID:  r.ExternalID,
		Subject:     "user:" + r.primaryEmailOrUserName(),
		DisplayName: r.displayName(),
		Active:      active,
	}
}

// primaryEmailOrUserName decides the name this person will file evidence under,
// and the name a later login has to recognise them by.
//
// The email is preferred because that is what an ID token carries and what a
// reader of a record months later can act on. The userName is the fallback —
// often the same string, and when it is not, it is at least resolvable.
func (r scimUserRequest) primaryEmailOrUserName() string {
	for _, email := range r.Emails {
		if email.Primary && email.Value != "" {
			return email.Value
		}
	}
	for _, email := range r.Emails {
		if email.Value != "" {
			return email.Value
		}
	}
	return r.UserName
}

func (r scimUserRequest) displayName() string {
	if r.DisplayName != "" {
		return r.DisplayName
	}
	if r.Name == nil {
		return ""
	}
	if r.Name.Formatted != "" {
		return r.Name.Formatted
	}
	return strings.TrimSpace(r.Name.GivenName + " " + r.Name.FamilyName)
}

// --- Errors ---

// scimError is the envelope the specification requires. A client reading a
// failure expects this and not this store's own error shape.
func scimError(w http.ResponseWriter, status int, scimType, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schemas":  []string{schemaError},
		"status":   strconv.Itoa(status),
		"scimType": scimType,
		"detail":   detail,
	})
}

func scimJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail keeps the reason in the log and out of the response, which is the same
// division the rest of the API makes.
func (h *SCIMHandler) fail(w http.ResponseWriter, doing string, err error) {
	h.logger.Error("scim request failed", "doing", doing, "error", err)
	scimError(w, http.StatusInternalServerError, "", "could not "+doing)
}

// --- Routes ---

func (h *SCIMHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req scimUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "could not read the resource: "+err.Error())
		return
	}
	if strings.TrimSpace(req.UserName) == "" {
		scimError(w, http.StatusBadRequest, "invalidValue", "userName is required")
		return
	}

	user, err := h.store.CreateUser(r.Context(), req.write())
	switch {
	case errors.Is(err, store.ErrUserNameTaken):
		// The specification's own code for this, and the one a client branches
		// on to decide it should have looked first.
		scimError(w, http.StatusConflict, "uniqueness", err.Error())
		return
	case errors.Is(err, store.ErrNotProvisionable):
		scimError(w, http.StatusConflict, "uniqueness", err.Error())
		return
	case err != nil:
		h.fail(w, "create the user", err)
		return
	}

	h.logger.Info("scim user provisioned",
		"user_name", user.UserName, "subject", user.Subject, "active", user.Active)
	scimJSON(w, http.StatusCreated, h.resource(user))
}

func (h *SCIMHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	user, err := h.store.FindUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "read the user", err)
		return
	}
	if user == nil {
		h.notFound(w, chi.URLParam(r, "id"))
		return
	}
	scimJSON(w, http.StatusOK, h.resource(user))
}

// ListUsers answers the query a provisioner makes before it does anything else:
// whether it has already provisioned somebody.
func (h *SCIMHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	filter, err := parseUserFilter(r.URL.Query().Get("filter"))
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}
	filter.StartIndex, filter.Count = pageOf(r)

	users, total, err := h.store.ListUsers(r.Context(), filter)
	if err != nil {
		h.fail(w, "list users", err)
		return
	}

	resources := make([]scimUser, 0, len(users))
	for i := range users {
		resources = append(resources, h.resource(&users[i]))
	}
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{schemaListResponse},
		"totalResults": total,
		"startIndex":   filter.StartIndex,
		"itemsPerPage": len(resources),
		"Resources":    resources,
	})
}

// ReplaceUser is PUT: the client states the whole resource.
func (h *SCIMHandler) ReplaceUser(w http.ResponseWriter, r *http.Request) {
	scimID := chi.URLParam(r, "id")
	var req scimUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "could not read the resource: "+err.Error())
		return
	}

	// A PUT that turns somebody off is a deactivation like any other, and has
	// to end their sessions rather than only their next login.
	if req.Active != nil && !*req.Active {
		if !h.deactivate(w, r, scimID) {
			return
		}
	}

	user, err := h.store.ReplaceUser(r.Context(), scimID, req.write())
	if errors.Is(err, store.ErrUserNameTaken) {
		scimError(w, http.StatusConflict, "uniqueness", err.Error())
		return
	}
	if err != nil {
		h.fail(w, "replace the user", err)
		return
	}
	if user == nil {
		h.notFound(w, scimID)
		return
	}
	scimJSON(w, http.StatusOK, h.resource(user))
}

// patchRequest is SCIM's PatchOp. Entra sends these for everything it changes
// after creation, deactivation included.
type patchRequest struct {
	Schemas    []string `json:"schemas"`
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"Operations"`
}

// PatchUser applies the operations a provisioner sends.
//
// Only the paths that carry meaning here are acted on, and an unknown one is
// ignored rather than refused: a directory sends the attributes its own schema
// defines, and failing a whole sync because one of them has no counterpart here
// would stop the deactivations landing too.
func (h *SCIMHandler) PatchUser(w http.ResponseWriter, r *http.Request) {
	scimID := chi.URLParam(r, "id")

	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "could not read the patch: "+err.Error())
		return
	}

	user, err := h.store.FindUser(r.Context(), scimID)
	if err != nil {
		h.fail(w, "read the user", err)
		return
	}
	if user == nil {
		h.notFound(w, scimID)
		return
	}

	for _, op := range req.Operations {
		if strings.EqualFold(op.Op, "remove") {
			// Nothing here is removable: the attributes this store keeps are
			// the ones it needs to name somebody.
			continue
		}
		active, ok := activeFromPatch(op.Path, op.Value)
		if !ok {
			continue
		}
		if !active && !h.deactivate(w, r, scimID) {
			return
		}
		updated, err := h.store.SetActive(r.Context(), scimID, active)
		if err != nil {
			h.fail(w, "update the user", err)
			return
		}
		if updated == nil {
			h.notFound(w, scimID)
			return
		}
		h.logger.Info("scim user set active",
			"user_name", updated.UserName, "subject", updated.Subject, "active", active)
		user = updated
	}

	scimJSON(w, http.StatusOK, h.resource(user))
}

// DeleteUser is a disable. Evidence names its source, so removing the row would
// leave records attributed to nothing — and an administrator could no longer
// tell somebody who was revoked from somebody who never existed.
func (h *SCIMHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	scimID := chi.URLParam(r, "id")
	if !h.deactivate(w, r, scimID) {
		return
	}
	user, err := h.store.SetActive(r.Context(), scimID, false)
	if err != nil {
		h.fail(w, "delete the user", err)
		return
	}
	if user == nil {
		h.notFound(w, scimID)
		return
	}
	h.logger.Info("scim user deleted", "user_name", user.UserName, "subject", user.Subject)
	w.WriteHeader(http.StatusNoContent)
}

// deactivate guards the one deactivation this store must refuse, and reports
// whether the caller may go on.
//
// A directory that removes the last administrator would otherwise leave a
// deployment with no way in but psql. The Access tab makes the same check, for
// the same reason.
func (h *SCIMHandler) deactivate(w http.ResponseWriter, r *http.Request, scimID string) bool {
	others, err := h.store.CountOtherEnabledAdmins(r.Context(), scimID)
	if err != nil {
		h.fail(w, "check the remaining administrators", err)
		return false
	}
	if others > 0 {
		return true
	}

	user, err := h.store.FindUser(r.Context(), scimID)
	if err != nil {
		h.fail(w, "check the remaining administrators", err)
		return false
	}
	// Only a refusal if this person is themselves an administrator; a store
	// with no administrators at all is a different problem and not one this
	// request is creating.
	if user == nil || !h.isAdmin(r, user) {
		return true
	}

	h.logger.Warn("refused to deactivate the last administrator",
		"user_name", user.UserName, "subject", user.Subject)
	scimError(w, http.StatusForbidden, "mutability",
		"refusing to deactivate the last enabled administrator; grant admin elsewhere first")
	return false
}

func (h *SCIMHandler) isAdmin(r *http.Request, user *model.SCIMUser) bool {
	admin, err := h.store.HasAdminRole(r.Context(), user.ID)
	if err != nil {
		h.logger.Error("could not read roles while deactivating", "error", err)
		// Fail towards refusing, since the alternative is locking everybody
		// out on the strength of a failed query.
		return true
	}
	return admin
}

func (h *SCIMHandler) notFound(w http.ResponseWriter, scimID string) {
	scimError(w, http.StatusNotFound, "", "no user with id "+scimID)
}

// --- Parsing ---

// parseUserFilter reads the two equality filters a provisioner sends.
//
// Not the SCIM filter grammar, which is a parser's worth of work for operators
// no client here sends; see docs/scim-provisioning-plan.md. Anything else is
// refused rather than silently answered with the whole directory, which would
// look to a client like every user matching every query.
func parseUserFilter(raw string) (store.SCIMUserFilter, error) {
	var f store.SCIMUserFilter
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return f, nil
	}

	attr, value, ok := cutEquality(raw)
	if !ok {
		return f, errors.New(`only filters of the form 'attribute eq "value"' are supported`)
	}
	switch strings.ToLower(attr) {
	case "username":
		f.UserName = value
	case "externalid":
		f.ExternalID = value
	default:
		return f, errors.New("filtering is supported on userName and externalId only")
	}
	return f, nil
}

// cutEquality splits `attr eq "value"` into its two halves.
func cutEquality(raw string) (attr, value string, ok bool) {
	attr, rest, ok := strings.Cut(raw, " eq ")
	if !ok {
		return "", "", false
	}
	value = strings.TrimSpace(rest)
	if len(value) < 2 || !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) {
		return "", "", false
	}
	return strings.TrimSpace(attr), value[1 : len(value)-1], true
}

// pageOf reads SCIM's 1-based paging parameters, correcting rather than
// refusing what falls outside them: a client sending startIndex=0 means the
// beginning, and the specification says to treat it that way.
func pageOf(r *http.Request) (startIndex, count int) {
	startIndex = 1
	if v, err := strconv.Atoi(r.URL.Query().Get("startIndex")); err == nil && v > 1 {
		startIndex = v
	}
	count = defaultPageSize
	if v, err := strconv.Atoi(r.URL.Query().Get("count")); err == nil && v >= 0 {
		count = v
	}
	if count > maxPageSize {
		count = maxPageSize
	}
	return startIndex, count
}

// activeFromPatch finds a change to `active` in one operation, whether it
// arrived as a path or inside the value.
//
// Both spellings are real. Entra sends {"op":"replace","path":"active",
// "value":false} in some flows and {"op":"replace","value":{"active":false}}
// in others, and missing the second one would mean silently not deprovisioning
// anybody — the failure this whole protocol is here to prevent.
func activeFromPatch(path string, value json.RawMessage) (active bool, ok bool) {
	if strings.EqualFold(strings.TrimSpace(path), "active") {
		// The value may be a JSON boolean or the string "False", which some
		// clients send.
		var b bool
		if err := json.Unmarshal(value, &b); err == nil {
			return b, true
		}
		var s string
		if err := json.Unmarshal(value, &s); err == nil {
			parsed, err := strconv.ParseBool(strings.ToLower(s))
			return parsed, err == nil
		}
		return false, false
	}
	if path != "" {
		return false, false
	}
	var body map[string]any
	if err := json.Unmarshal(value, &body); err != nil {
		return false, false
	}
	for key, v := range body {
		if !strings.EqualFold(key, "active") {
			continue
		}
		switch typed := v.(type) {
		case bool:
			return typed, true
		case string:
			parsed, err := strconv.ParseBool(strings.ToLower(typed))
			return parsed, err == nil
		}
	}
	return false, false
}
