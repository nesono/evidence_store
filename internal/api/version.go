package api

import (
	"net/http"

	"github.com/nesono/evidence-store/internal/version"
)

// VersionHandler answers which build is running.
//
// Public, like /healthz and unlike everything under /api/v1. Two reasons. The
// page shows the version before anyone has logged in, which is when somebody
// reporting "it does this on the login screen" most needs to say which build
// they were looking at. And there is nothing here to protect that is not
// already public: the web UI is served unauthenticated, so anyone who can reach
// this endpoint can already read the frontend of the exact build it names.
type VersionHandler struct {
	version version.Version
}

func NewVersionHandler() *VersionHandler {
	// Read once. It cannot change while the process runs, and a page that polls
	// health should not pay for a build-info lookup each time.
	return &VersionHandler{version: version.Current()}
}

func (h *VersionHandler) Get(w http.ResponseWriter, r *http.Request) {
	// Deliberately not cached by the browser: the point of the field is to say
	// what is running now, and a stale one would be worse than none — it is the
	// answer somebody reads out when they are already confused.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, h.version)
}
