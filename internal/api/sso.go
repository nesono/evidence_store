package api

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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
	// Either may be nil; a deployment can have one front end, both, or neither.
	oidc *auth.OIDCProvider
	saml *auth.SAMLProvider

	principals  *store.PrincipalStore
	sessions    *store.SessionStore
	samlPending *store.SAMLRequestStore
	roleMap     map[string]string
	oidcCfg     config.OIDC
	cfg         config.Session
	logger      *slog.Logger
}

type SSODeps struct {
	OIDC        *auth.OIDCProvider
	SAML        *auth.SAMLProvider
	Principals  *store.PrincipalStore
	Sessions    *store.SessionStore
	SAMLPending *store.SAMLRequestStore
	RoleMap     map[string]string
	OIDCConfig  config.OIDC
	Session     config.Session
	Logger      *slog.Logger
}

func NewSSOHandler(d SSODeps) *SSOHandler {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &SSOHandler{
		oidc: d.OIDC, saml: d.SAML,
		principals: d.Principals, sessions: d.Sessions, samlPending: d.SAMLPending,
		roleMap: d.RoleMap, oidcCfg: d.OIDCConfig, cfg: d.Session, logger: logger,
	}
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
	http.Redirect(w, r, h.oidc.AuthCodeURL(state, verifier), http.StatusFound)
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

	claims, err := h.oidc.Exchange(r.Context(), code, verifier)
	if err != nil {
		// A token that does not verify is the one thing here that is worth
		// alarming about: everything above is a browser being confused, and
		// this is a token claiming to be something it is not.
		h.logger.Error("failed to verify identity token", "error", err)
		writeError(w, http.StatusUnauthorized, "could not verify the identity token")
		return
	}

	h.completeLogin(w, r, claims)
}

// SAMLMetadata is what an administrator registers with the identity provider.
// Serving it beats asking somebody to write the XML by hand, which is how the
// two ends end up disagreeing about a URL or a certificate.
func (h *SSOHandler) SAMLMetadata(w http.ResponseWriter, _ *http.Request) {
	metadata, err := h.saml.Metadata()
	if err != nil {
		h.fail(w, "produce SAML metadata", err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(metadata)
}

// SAMLLogin sends the browser to the identity provider.
func (h *SSOHandler) SAMLLogin(w http.ResponseWriter, r *http.Request) {
	redirect, requestID, err := h.saml.AuthnRequest("/")
	if err != nil {
		h.fail(w, "start login", err)
		return
	}

	// Remembered server-side rather than in a cookie: the assertion comes back
	// as a cross-site POST, and SameSite=Lax is exactly what stops a cookie
	// riding along with one.
	if err := h.samlPending.Remember(r.Context(), requestID, time.Now().Add(loginTimeout)); err != nil {
		h.fail(w, "start login", err)
		return
	}

	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// SAMLACS is the Assertion Consumer Service: where the identity provider posts
// the assertion. It is section 9's "same shape, different front end" — from the
// claims onward this is the OIDC path.
func (h *SSOHandler) SAMLACS(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "malformed assertion post")
		return
	}

	pending, err := h.samlPending.Pending(r.Context())
	if err != nil {
		h.fail(w, "check the login", err)
		return
	}

	claims, err := h.saml.ParseAssertion(r, pending)
	if err != nil {
		// An assertion that does not validate is the one thing in this flow
		// worth alarming about. The reason stays in the log: it names which
		// check failed, which is more use to an attacker than to the browser.
		h.logger.Error("failed to validate SAML assertion", "error", err)
		writeError(w, http.StatusUnauthorized, "could not verify the assertion; start again at "+auth.SAMLLoginPath)
		return
	}

	// Consume the request so the same assertion cannot be presented twice.
	// Best effort: the assertion's own validity window bounds a replay even if
	// this fails, and refusing a login somebody has already completed would be
	// the worse outcome.
	if id := requestIDOf(r); id != "" {
		if err := h.samlPending.Forget(r.Context(), id); err != nil {
			h.logger.Warn("failed to consume saml request id", "error", err)
		}
	}

	h.completeLogin(w, r, claims)
}

// requestIDOf reads the InResponseTo the provider echoed back, so the matching
// pending request can be consumed. Reading it from the assertion the library
// already validated would be better; it does not expose it, and re-parsing the
// XML to find out would mean trusting a second, unvalidated read of the same
// document. So this is deliberately only used to clean up, never to decide
// anything.
func requestIDOf(r *http.Request) string {
	raw, err := base64.StdEncoding.DecodeString(r.PostFormValue("SAMLResponse"))
	if err != nil {
		return ""
	}
	const marker = `InResponseTo="`
	start := strings.Index(string(raw), marker)
	if start < 0 {
		return ""
	}
	rest := string(raw)[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// completeLogin is everything that happens once a front end has established who
// somebody is: the principal, the roles their groups imply, the session, and
// the cookies.
//
// Both front ends end here, which is the arrangement docs/rbac-design.md was
// written to make possible — from this point nothing can tell an OIDC login
// from a SAML one, and neither can the session, the roles, or the source
// binding downstream of them.
func (h *SSOHandler) completeLogin(w http.ResponseWriter, r *http.Request, claims *auth.Claims) {
	principal, err := h.principals.UpsertFromIdP(r.Context(), store.IdPLogin{
		ExternalID:  claims.ExternalID(),
		Subject:     claims.PrincipalSubject(),
		DisplayName: claims.Name,
		// Only consulted if the external id matches nothing, to find the row a
		// provisioner created for this person before their first login.
		LoginNames: claims.LoginNames(),
		Roles:      auth.RolesForGroups(h.roleMap, claims.Groups),
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
	expires := time.Now().Add(h.cfg.TTL)
	if _, err := h.sessions.Create(r.Context(), principal.ID, auth.HashKey(token), expires, r.UserAgent(), claims.IDToken); err != nil {
		h.fail(w, "start the session", err)
		return
	}

	csrf, err := auth.GenerateSessionToken()
	if err != nil {
		h.fail(w, "start the session", err)
		return
	}

	http.SetCookie(w, h.cookie(auth.SessionCookie, token, h.cfg.TTL))
	// Readable by script, unlike the session cookie: the page has to be able to
	// echo it back in a header, which is the whole point of a double submit.
	// Knowing it is useless without also being able to send the session cookie.
	http.SetCookie(w, h.readableCookie(auth.CSRFCookie, csrf, h.cfg.TTL))

	h.logger.Info("login", "subject", principal.Subject, "roles", principal.Roles)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout ends this session now rather than at expiry — which is what storing
// sessions rather than signing them buys — and then says where to go to end the
// provider's session as well.
//
// Ending only the local one is not a logout. The provider still has its own
// session, so the next login is answered without a password: the store signs
// the same person straight back in, which reads as the button being broken, and
// on a shared machine hands the next person the last one's account.
//
// The answer names a URL rather than redirecting, because this is called with
// fetch from a page that then has to navigate: a redirect here would be
// followed inside the request and end up fetching the provider's logout page
// into a variable, leaving the browser exactly where it was.
func (h *SSOHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var ended store.EndedSession
	if cookie, err := r.Cookie(auth.SessionCookie); err == nil && cookie.Value != "" {
		var err error
		if ended, err = h.sessions.Delete(r.Context(), auth.HashKey(cookie.Value)); err != nil {
			h.fail(w, "log out", err)
			return
		}
	}
	http.SetCookie(w, h.expiredCookie(auth.SessionCookie))
	http.SetCookie(w, h.expiredCookie(auth.CSRFCookie))

	// Where to land when there is no provider to visit: a SAML login, a
	// provider that advertises no logout endpoint, or a logout by somebody who
	// was not signed in. The marker is what stops the page treating the first
	// 401 as an expired session and sending them back out to log in.
	next := signedOutPath
	if h.oidc != nil {
		if url := h.oidc.EndSessionURL(ended.IDToken, h.postLogoutURL()); url != "" {
			next = url
		}
	}

	// Logged at the same level as its opposite number. Before this, a logout
	// left no trace at all, which made "I pressed log out and stayed logged in"
	// impossible to tell from "I never pressed it" without a network trace.
	h.logger.Info("logout", "subject", subjectOrAnonymous(ended.Subject), "provider_logout", next != signedOutPath)
	writeJSON(w, http.StatusOK, map[string]string{"logout_url": next})
}

// signedOutPath is where a logout lands when the provider is not involved. The
// marker is read by the page, not by this server.
const signedOutPath = "/?signed_out=1"

// postLogoutURL is where the provider should return the browser afterwards:
// the store's own front page, marked so the page knows it arrived by logging
// out rather than by a session going stale.
//
// Derived from the redirect URL, which a deployment already has to spell out
// because the provider must be told the same value. An operator whose logout
// should land somewhere else says so with EVIDENCE_OIDC_POST_LOGOUT_URL.
//
// The empty string when there is nothing to derive it from; the provider then
// returns the browser wherever it was configured to.
func (h *SSOHandler) postLogoutURL() string {
	if h.oidcCfg.PostLogoutURL != "" {
		return h.oidcCfg.PostLogoutURL
	}
	u, err := url.Parse(h.oidcCfg.RedirectURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + signedOutPath
}

// subjectOrAnonymous names whoever was signed in, for the log line only.
// Logging out a browser that was not signed in is a no-op worth recording as
// one rather than dropping silently.
func subjectOrAnonymous(subject string) string {
	if subject == "" {
		return "(anonymous)"
	}
	return subject
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
