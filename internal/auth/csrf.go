package auth

import (
	"crypto/subtle"
	"net/http"
)

// CSRFCookie holds the token a session-authenticated page echoes back on
// writes. Deliberately not HttpOnly: the page has to read it to send it, which
// is what makes the double submit work.
const CSRFCookie = "evidence_csrf"

// CSRFHeader is where the page echoes it.
const CSRFHeader = "X-CSRF-Token"

// RequireCSRF protects the writes of callers who authenticated with a cookie.
//
// A bearer token is only ever sent by something that meant to send it. A cookie
// is sent by the browser on any request to this origin, including one triggered
// by a page the user did not write — which is why a session is worth more to an
// attacker than a header ever was. SameSite=Lax already keeps the cookie off
// cross-site POSTs in every browser that honours it; this is the part that does
// not depend on the browser getting that right.
//
// The check is a double submit: a token in a readable cookie, echoed in a
// header. Script on this origin can read the cookie and send the header; script
// on another origin can do neither, because the same-origin policy stops it
// reading the cookie and browsers will not let it set this header on a
// cross-origin request.
//
// Bearer-token callers pass straight through: CI has no cookies and no need of
// this.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		principal, ok := PrincipalFrom(r.Context())
		if !ok || !principal.ViaSession {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(CSRFCookie)
		header := r.Header.Get(CSRFHeader)
		if err != nil || cookie.Value == "" || header == "" ||
			subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			writeAuthError(w, http.StatusForbidden,
				"missing or invalid "+CSRFHeader+"; reload the page and try again")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isSafeMethod reports whether a method is one that only reads. These are the
// requests a cross-site page can cause anyway — an image tag is a GET — so
// requiring a token on them would buy nothing and break every ordinary link.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
