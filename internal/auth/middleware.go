package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nesono/evidence-store/internal/config"
)

type contextKey string

const roleKey contextKey = "auth_role"

// Role represents the access level of an authenticated request.
type Role string

const (
	RoleReadWrite Role = "rw"
	RoleReadOnly  Role = "ro"
)

// GetRole returns the authenticated role from the request context.
// Returns empty string if not authenticated.
func GetRole(ctx context.Context) Role {
	if r, ok := ctx.Value(roleKey).(Role); ok {
		return r
	}
	return ""
}

type keyEntry struct {
	key      []byte
	readOnly bool
}

// Middleware returns an HTTP middleware that validates Bearer tokens
// against the configured API keys. If keys is empty, the middleware
// is a no-op (all requests pass through).
func Middleware(keys []config.APIKey) func(http.Handler) http.Handler {
	entries := make([]keyEntry, len(keys))
	for i, k := range keys {
		entries[i] = keyEntry{key: []byte(k.Key), readOnly: k.ReadOnly}
	}

	return func(next http.Handler) http.Handler {
		if len(entries) == 0 {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
				return
			}

			tokenBytes := []byte(token)
			matched := -1
			for i, e := range entries {
				if len(tokenBytes) == len(e.key) && subtle.ConstantTimeCompare(tokenBytes, e.key) == 1 {
					matched = i
					break
				}
			}

			if matched < 0 {
				writeAuthError(w, http.StatusUnauthorized, "invalid API key")
				return
			}

			entry := entries[matched]
			if entry.readOnly && !isReadMethod(r.Method) {
				writeAuthError(w, http.StatusForbidden, "read-only API key cannot perform write operations")
				return
			}

			role := RoleReadWrite
			if entry.readOnly {
				role = RoleReadOnly
			}
			ctx := context.WithValue(r.Context(), roleKey, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return auth[len(prefix):]
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
