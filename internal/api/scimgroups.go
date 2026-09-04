package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
)

// Groups are where a role comes from.
//
// A group's displayName is looked up in EVIDENCE_GROUP_ROLE_MAP — the same map
// the login path uses, because which of our groups means which of your roles is
// one question and does not become two just because a second protocol asked it.
// A group with no entry grants nothing, so pointing this store at a company
// directory does not hand every employee an account that can write.

const schemaGroup = "urn:ietf:params:scim:schemas:core:2.0:Group"

type scimGroupResource struct {
	Schemas     []string             `json:"schemas"`
	ID          string               `json:"id"`
	ExternalID  string               `json:"externalId,omitempty"`
	DisplayName string               `json:"displayName"`
	Members     []scimMemberResource `json:"members"`
	Meta        scimMeta             `json:"meta"`
}

type scimMemberResource struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

func (h *SCIMHandler) groupResource(g *model.SCIMGroup) scimGroupResource {
	members := make([]scimMemberResource, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, scimMemberResource{
			Value: m.SCIMID, Display: m.Display, Ref: "/scim/v2/Users/" + m.SCIMID,
		})
	}
	return scimGroupResource{
		Schemas:     []string{schemaGroup},
		ID:          g.SCIMID,
		ExternalID:  g.ExternalID,
		DisplayName: g.DisplayName,
		Members:     members,
		Meta: scimMeta{
			ResourceType: "Group",
			Created:      g.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			LastModified: g.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Location:     "/scim/v2/Groups/" + g.SCIMID,
		},
	}
}

type scimGroupRequest struct {
	DisplayName string `json:"displayName"`
	ExternalID  string `json:"externalId"`
	Members     []any  `json:"members"`
}

func (r scimGroupRequest) write() store.SCIMGroupWrite {
	return store.SCIMGroupWrite{
		DisplayName: strings.TrimSpace(r.DisplayName),
		ExternalID:  r.ExternalID,
		Members:     memberValues(r.Members),
	}
}

func (h *SCIMHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var req scimGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "could not read the group: "+err.Error())
		return
	}
	if req.DisplayName == "" {
		scimError(w, http.StatusBadRequest, "invalidValue", "displayName is required")
		return
	}

	group, err := h.store.CreateGroup(r.Context(), req.write(), h.rolesFor)
	if errors.Is(err, store.ErrGroupNameTaken) {
		scimError(w, http.StatusConflict, "uniqueness", err.Error())
		return
	}
	if err != nil {
		h.fail(w, "create the group", err)
		return
	}
	// The grants are logged because an operator setting up provisioning is
	// otherwise guessing whether their role map matches the directory's names,
	// and a group that maps to nothing looks identical to one that worked.
	h.logger.Info("scim group provisioned",
		"display_name", group.DisplayName, "members", len(group.Members),
		"grants", h.rolesFor([]string{group.DisplayName}))
	scimJSON(w, http.StatusCreated, h.groupResource(group))
}

func (h *SCIMHandler) GetGroup(w http.ResponseWriter, r *http.Request) {
	group, err := h.store.FindGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "read the group", err)
		return
	}
	if group == nil {
		h.groupNotFound(w, chi.URLParam(r, "id"))
		return
	}
	scimJSON(w, http.StatusOK, h.groupResource(group))
}

func (h *SCIMHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	filter, err := parseGroupFilter(r.URL.Query().Get("filter"))
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}
	filter.StartIndex, filter.Count = pageOf(r)

	groups, total, err := h.store.ListGroups(r.Context(), filter)
	if err != nil {
		h.fail(w, "list groups", err)
		return
	}
	resources := make([]scimGroupResource, 0, len(groups))
	for i := range groups {
		resources = append(resources, h.groupResource(&groups[i]))
	}
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{schemaListResponse},
		"totalResults": total,
		"startIndex":   filter.StartIndex,
		"itemsPerPage": len(resources),
		"Resources":    resources,
	})
}

// ReplaceGroup is PUT: the provisioner states the whole group, membership
// included, so anyone dropped from it loses what it granted them.
func (h *SCIMHandler) ReplaceGroup(w http.ResponseWriter, r *http.Request) {
	scimID := chi.URLParam(r, "id")
	var req scimGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "could not read the group: "+err.Error())
		return
	}

	group, err := h.store.ReplaceGroup(r.Context(), scimID, req.write(), h.rolesFor)
	if errors.Is(err, store.ErrGroupNameTaken) {
		scimError(w, http.StatusConflict, "uniqueness", err.Error())
		return
	}
	if err != nil {
		h.fail(w, "replace the group", err)
		return
	}
	if group == nil {
		h.groupNotFound(w, scimID)
		return
	}
	scimJSON(w, http.StatusOK, h.groupResource(group))
}

// PatchGroup applies membership changes — how a directory reports somebody
// joining or leaving a team, and so how a role is granted or taken away from
// somebody who keeps their account either way.
//
// Removal is the half that matters. Without it a promotion would be reversible
// only by deleting the person.
func (h *SCIMHandler) PatchGroup(w http.ResponseWriter, r *http.Request) {
	scimID := chi.URLParam(r, "id")

	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "could not read the patch: "+err.Error())
		return
	}

	var add, remove []string
	var displayName string
	for _, op := range req.Operations {
		path := strings.TrimSpace(op.Path)

		// `members[value eq "x"]` names its subject in the path rather than the
		// value, and is how a provisioner most often removes a single member.
		if inner, ok := memberFilterPath(path); ok {
			if strings.EqualFold(op.Op, "remove") {
				remove = append(remove, inner)
			} else {
				add = append(add, inner)
			}
			continue
		}

		if strings.EqualFold(path, "displayname") {
			var name string
			if err := json.Unmarshal(op.Value, &name); err == nil {
				displayName = strings.TrimSpace(name)
			}
			continue
		}

		if !strings.EqualFold(path, "members") {
			// An attribute with no counterpart here. Ignored rather than
			// refused: a directory sends what its own schema defines, and
			// failing a whole sync over one would stop the membership changes
			// landing with it.
			continue
		}

		var raw []any
		if err := json.Unmarshal(op.Value, &raw); err != nil {
			continue
		}
		values := memberValues(raw)
		switch {
		case strings.EqualFold(op.Op, "remove"):
			remove = append(remove, values...)
		case strings.EqualFold(op.Op, "replace"):
			// Replacing the members attribute states the whole membership, so
			// everyone currently in the group who is not named is on their way
			// out.
			group, err := h.store.FindGroup(r.Context(), scimID)
			if err != nil {
				h.fail(w, "read the group", err)
				return
			}
			if group == nil {
				h.groupNotFound(w, scimID)
				return
			}
			for _, existing := range group.Members {
				remove = append(remove, existing.SCIMID)
			}
			add = append(add, values...)
		default:
			add = append(add, values...)
		}
	}

	group, err := h.store.PatchGroupMembers(r.Context(), scimID, add, remove, displayName, h.rolesFor)
	if errors.Is(err, store.ErrGroupNameTaken) {
		scimError(w, http.StatusConflict, "uniqueness", err.Error())
		return
	}
	if err != nil {
		h.fail(w, "update the group", err)
		return
	}
	if group == nil {
		h.groupNotFound(w, scimID)
		return
	}
	h.logger.Info("scim group membership changed",
		"display_name", group.DisplayName, "added", len(add), "removed", len(remove))
	scimJSON(w, http.StatusOK, h.groupResource(group))
}

// DeleteGroup removes the group and the access it granted.
//
// A real delete, unlike a user: a group holds no evidence and names nothing a
// reader will look up months later, so keeping a husk of one would only leave a
// name the role map could still match.
func (h *SCIMHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	scimID := chi.URLParam(r, "id")
	found, err := h.store.DeleteGroup(r.Context(), scimID, h.rolesFor)
	if err != nil {
		h.fail(w, "delete the group", err)
		return
	}
	if !found {
		h.groupNotFound(w, scimID)
		return
	}
	h.logger.Info("scim group deleted", "id", scimID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *SCIMHandler) groupNotFound(w http.ResponseWriter, scimID string) {
	scimError(w, http.StatusNotFound, "", "no group with id "+scimID)
}

func parseGroupFilter(raw string) (store.SCIMGroupFilter, error) {
	var f store.SCIMGroupFilter
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return f, nil
	}
	attr, value, ok := cutEquality(raw)
	if !ok {
		return f, errors.New(`only filters of the form 'attribute eq "value"' are supported`)
	}
	if !strings.EqualFold(attr, "displayName") {
		return f, errors.New("filtering is supported on displayName only")
	}
	f.DisplayName = value
	return f, nil
}

// memberValues reads the user ids out of a members array, which providers spell
// either as bare strings or as objects carrying a "value".
func memberValues(raw []any) []string {
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if v := memberValue(item); v != "" {
			values = append(values, v)
		}
	}
	return values
}

// memberValue reads the user id out of one SCIM member entry, which providers
// spell either as a bare string or as an object carrying a "value".
func memberValue(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if s, ok := v["value"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// memberFilterPath reads the id out of `members[value eq "x"]`.
func memberFilterPath(path string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(path), "members[") || !strings.HasSuffix(path, "]") {
		return "", false
	}
	inner := path[strings.Index(path, "[")+1 : len(path)-1]
	_, value, ok := cutEquality(inner)
	return value, ok
}
