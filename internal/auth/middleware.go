// Package auth resolves who is calling and what they are allowed to do.
//
// The two questions are answered by two middlewares. Authenticate runs once
// for the whole API and puts a Principal in the request context; Require wraps
// an individual route and asserts a single Permission. Authorization used to
// be a property of the HTTP method, which made POST /inheritance — an elevated
// operation per DESIGN.md section 8 — indistinguishable from posting a test
// result.
package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Authenticate resolves the caller and stores the result in the request
// context. It answers *who*, never *may they*: a request with valid
// credentials for a route it has no permission on passes through here and is
// stopped by Require.
//
// A request with no credentials is rejected with 401 unless the authenticator
// reports that nothing is configured, in which case it passes through
// unidentified — the default local-development posture, unchanged.
func Authenticate(authenticator Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := authenticator.Authenticate(r.Context(), r)
			switch {
			case errors.Is(err, ErrAuthDisabled):
				next.ServeHTTP(w, r.WithContext(withAuthDisabled(r.Context())))
			case errors.Is(err, ErrNoCredentials):
				writeAuthError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
			case errors.Is(err, ErrAuthUnavailable):
				// The caller's key may well be fine. Telling them it is not
				// would send perfectly good CI pipelines rotating credentials
				// to fix a database outage.
				slog.Error("authentication backend unavailable", "error", err)
				writeAuthError(w, http.StatusServiceUnavailable, "authentication temporarily unavailable")
			case err != nil:
				writeAuthError(w, http.StatusUnauthorized, "invalid API key")
			default:
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
			}
		})
	}
}

// Require returns a middleware that admits only callers holding perm. It is
// mounted per route, so the permission a route needs is stated where the route
// is declared.
//
// 401 and 403 are kept distinct: an anonymous caller is told to authenticate,
// an authenticated one is told the answer will not change by trying again.
func Require(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				if authDisabled(r.Context()) {
					next.ServeHTTP(w, r)
					return
				}
				writeAuthError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if !principal.Can(perm) {
				writeAuthError(w, http.StatusForbidden, "permission denied: "+string(perm)+" required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
