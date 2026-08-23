package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/store"
)

// loginStateCookie carries the CSRF state and the PKCE verifier between the
// redirect out to the provider and the redirect back. It is HttpOnly and lives
// for minutes: it is a detail of one login attempt, not a credential.
const loginStateCookie = "evidence_login"

// loginTimeout bounds how long somebody has to finish signing in. Long enough
// for a password manager, a second factor and a misplaced phone; short enough
// that an abandoned attempt is not left lying around.
const loginTimeout = 10 * time.Minute

// SSOHandler is the login flow: out to the identity provider, back with an
// identity, and a session cookie for the browser to carry.
//
// Everything from Principal inward already works without knowing where a caller
// came from, so this is genuinely a front end. Replacing it with SAML changes
// these three routes and nothing else.
type SSOHandler struct {
	provider   *auth.OIDCProvider
	principals *store.PrincipalStore
	sessions   *store.SessionStore
	cfg        config.OIDC
	logger     *slog.Logger
}

func NewSSOHandler(p *auth.OIDCProvider, principals *store.PrincipalStore, sessions *store.SessionStore, cfg config.OIDC, logger *slog.Logger) *SSOHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SSOHandler{provider: p, principals: principals, sessions: sessions, cfg: cfg, logger: logger}
}

// Login sends the browser to the identity provider.
func (h *SSOHandler) Login(w http.ResponseWriter, r *http.Request) {
	state, err := auth.GenerateSessionToken()
	if err != nil {
		h.fail(w, "start login", err)
		return
	}
	verifier, err := auth.GenerateSessionToken()
	if err != nil {
		h.fail(w, "start login", err)
		return
	}

	// State and verifier travel in the browser's own cookie rather than in
	// server memory, so a login survives the request landing on a different
	// replica than the one that started it.
	http.SetCookie(w, h.cookie(loginStateCookie, state+"|"+verifier, loginTimeout))
	http.Redirect(w, r, h.provider.AuthCodeURL(state, verifier), http.StatusFound)
}

// Callback is where the provider sends the browser back.
func (h *SSOHandler) Callback(w http.ResponseWriter, r *http.Request) {
	// Whatever happens next, this attempt is over.
	defer http.SetCookie(w, h.expiredCookie(loginStateCookie))

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		// The provider refused — consent declined, or the account is not
		// entitled to this application. Their description is more use than
		// anything this end could invent.
		h.logger.Info("identity provider refused login",
			"error", errParam, "description", r.URL.Query().Get("error_description"))
		writeError(w, http.StatusForbidden, "the identity provider refused the login: "+errParam)
		return
	}

	cookie, err := r.Cookie(loginStateCookie)
	if err != nil {
		// No cookie means this callback is not the continuation of a login
		// anybody started here, which is exactly what a forged one looks like.
		writeError(w, http.StatusBadRequest, "no login is in progress; start again at /auth/login")
		return
	}
	state, verifier, ok := strings.Cut(cookie.Value, "|")
	if !ok || state == "" || verifier == "" {
		writeError(w, http.StatusBadRequest, "malformed login state; start again at /auth/login")
		return
	}
	if r.URL.Query().Get("state") != state {
		// The state a browser comes back with has to be the one it left with.
		writeError(w, http.StatusBadRequest, "login state did not match; start again at /auth/login")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "no authorization code returned")
		return
	}

	claims, err := h.provider.Exchange(r.Context(), code, verifier)
	if err != nil {
		// A token that does not verify is the one thing here that is worth
		// alarming about: everything above is a browser being confused, and
		// this is a token claiming to be something it is not.
		h.logger.Error("failed to verify identity token", "error", err)
		writeError(w, http.StatusUnauthorized, "could not verify the identity token")
		return
	}

	principal, err := h.principals.UpsertFromIdP(r.Context(), store.IdPLogin{
		ExternalID:  claims.ExternalID(),
		Subject:     claims.PrincipalSubject(),
		DisplayName: claims.Name,
		Roles:       h.provider.RolesFor(claims.Groups),
	})
	if errors.Is(err, store.ErrSubjectTaken) {
		// An API key is already using this person's name. Guessing they are the
		// same party would hand somebody else's credential a human's roles.
		h.logger.Error("login blocked by an existing principal with the same subject",
			"subject", claims.PrincipalSubject())
		writeError(w, http.StatusConflict,
			"a principal named "+claims.PrincipalSubject()+" already exists; an administrator must rename it")
		return
	}
	if err != nil {
		h.fail(w, "record the login", err)
		return
	}

	if principal.Disabled() {
		// Revoked people can still authenticate with the provider; this store
		// is where that stops.
		h.logger.Info("refused login for disabled principal", "subject", principal.Subject)
		writeError(w, http.StatusForbidden, "this account has been disabled")
		return
	}

	token, err := auth.GenerateSessionToken()
	if err != nil {
		h.fail(w, "start the session", err)
		return
	}
	expires := time.Now().Add(h.cfg.SessionTTL)
	if _, err := h.sessions.Create(r.Context(), principal.ID, auth.HashKey(token), expires, r.UserAgent()); err != nil {
		h.fail(w, "start the session", err)
		return
	}

	csrf, err := auth.GenerateSessionToken()
	if err != nil {
		h.fail(w, "start the session", err)
		return
	}

	http.SetCookie(w, h.cookie(auth.SessionCookie, token, h.cfg.SessionTTL))
	// Readable by script, unlike the session cookie: the page has to be able to
	// echo it back in a header, which is the whole point of a double submit.
	// Knowing it is useless without also being able to send the session cookie.
	http.SetCookie(w, h.readableCookie(auth.CSRFCookie, csrf, h.cfg.SessionTTL))

	h.logger.Info("login", "subject", principal.Subject, "roles", principal.Roles)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout ends this session now rather than at expiry — which is what storing
// sessions rather than signing them buys.
func (h *SSOHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookie); err == nil && cookie.Value != "" {
		if err := h.sessions.Delete(r.Context(), auth.HashKey(cookie.Value)); err != nil {
			h.fail(w, "log out", err)
			return
		}
	}
	http.SetCookie(w, h.expiredCookie(auth.SessionCookie))
	http.SetCookie(w, h.expiredCookie(auth.CSRFCookie))
	w.WriteHeader(http.StatusNoContent)
}

func (h *SSOHandler) cookie(name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		// Lax rather than Strict: the login flow returns as a top-level
		// navigation from the provider, and Strict would withhold the cookie on
		// exactly that request. Lax still keeps it off cross-site POSTs, which
		// is what the CSRF token then backs up.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	}
}

func (h *SSOHandler) readableCookie(name, value string, ttl time.Duration) *http.Cookie {
	c := h.cookie(name, value, ttl)
	c.HttpOnly = false
	return c
}

func (h *SSOHandler) expiredCookie(name string) *http.Cookie {
	c := h.cookie(name, "", 0)
	c.MaxAge = -1
	return c
}

func (h *SSOHandler) fail(w http.ResponseWriter, doing string, err error) {
	h.logger.Error("sso: failed to "+doing, "error", err)
	writeError(w, http.StatusInternalServerError, "could not "+doing)
}
