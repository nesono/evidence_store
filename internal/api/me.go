package api

import (
	"net/http"

	"github.com/nesono/evidence-store/internal/auth"
)

// MeHandler answers "who am I and what may I do".
//
// It exists for the web UI, which otherwise has to guess: without this it can
// only render every control and let the server refuse half of them, which is a
// worse experience than not offering what the caller cannot have. The SSO work
// in phase 5 needs the same answer for the same reason.
//
// It is the one route under /api/v1 that asserts no permission. Authentication
// has already run by the time it is reached, so a caller either has a principal
// or the store has nothing configured — and a principal holding no roles at all
// still deserves to be told that that is what it holds.
type MeHandler struct {
	authDB bool
}

func NewMeHandler(authDB bool) *MeHandler {
	return &MeHandler{authDB: authDB}
}

type meResponse struct {
	// Authenticated is false when the store runs open, which is the default
	// local-development posture rather than an error.
	Authenticated bool     `json:"authenticated"`
	Subject       string   `json:"subject,omitempty"`
	Kind          string   `json:"kind,omitempty"`
	DisplayName   string   `json:"display_name,omitempty"`
	Roles         []string `json:"roles"`
	Permissions   []string `json:"permissions"`
	// AuthDBEnabled tells an administrator whether a key issued through the
	// principals API will actually authenticate yet.
	AuthDBEnabled bool `json:"auth_db_enabled"`
}

func (h *MeHandler) Get(w http.ResponseWriter, r *http.Request) {
	resp := meResponse{
		Roles:         []string{},
		Permissions:   []string{},
		AuthDBEnabled: h.authDB,
	}

	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		// No principal on a request that got past Authenticate means nothing is
		// configured to check against: the store is open, and a client should
		// offer everything rather than nothing.
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Authenticated = true
	resp.Subject = principal.Subject
	resp.Kind = string(principal.Kind)
	resp.DisplayName = principal.DisplayName
	for _, role := range principal.Roles {
		resp.Roles = append(resp.Roles, string(role))
	}
	for _, perm := range principal.Permissions() {
		resp.Permissions = append(resp.Permissions, string(perm))
	}

	writeJSON(w, http.StatusOK, resp)
}
