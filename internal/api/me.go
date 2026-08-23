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
	sso    bool
}

func NewMeHandler(authDB, sso bool) *MeHandler {
	return &MeHandler{authDB: authDB, sso: sso}
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
	// SSOEnabled tells a client whether there is a login flow to send somebody
	// to. Without it the web UI has nothing to offer on a 401 but a prompt for
	// an API key, which is what it did before there was one.
	SSOEnabled bool `json:"sso_enabled"`
	// ViaSession distinguishes a logged-in browser from a pasted API key, which
	// is what decides whether the page shows a logout button and whether it
	// must send a CSRF token on writes.
	ViaSession bool `json:"via_session"`
}

// AuthConfig says which ways in this deployment has, to a caller who has not
// come in yet.
//
// It is unauthenticated on purpose, and /me cannot do this job: with
// credentials configured, /me refuses an anonymous caller — which is exactly
// the caller who needs to know whether there is somewhere to log in. Nothing
// here is a secret. Whether a login button exists is plain from the login page,
// and a store that answers "no" is one whose UI keeps asking for an API key.
func (h *MeHandler) AuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"sso_enabled":     h.sso,
		"auth_db_enabled": h.authDB,
	})
}

func (h *MeHandler) Get(w http.ResponseWriter, r *http.Request) {
	resp := meResponse{
		Roles:         []string{},
		Permissions:   []string{},
		AuthDBEnabled: h.authDB,
		SSOEnabled:    h.sso,
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
	resp.ViaSession = principal.ViaSession
	for _, role := range principal.Roles {
		resp.Roles = append(resp.Roles, string(role))
	}
	for _, perm := range principal.Permissions() {
		resp.Permissions = append(resp.Permissions, string(perm))
	}

	writeJSON(w, http.StatusOK, resp)
}
