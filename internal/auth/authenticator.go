package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nesono/evidence-store/internal/config"
)

var (
	// ErrNoCredentials means the request carries nothing this scheme can read.
	// A chain treats it as "not mine" and tries the next authenticator.
	ErrNoCredentials = errors.New("no credentials presented")

	// ErrInvalidCredentials means the request presented credentials for this
	// scheme and they did not check out. A chain stops here: a wrong key is an
	// answer, not an absence.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrAuthDisabled means the authenticator has nothing configured to check
	// against, so it can neither accept nor reject. It is how the long-standing
	// "no keys configured means the API is open" posture travels from config to
	// the middleware.
	ErrAuthDisabled = errors.New("authentication not configured")
)

// Authenticator resolves a request to a principal. Implementations report
// ErrNoCredentials rather than failing when the request carries nothing for
// their scheme, so several can be chained: API keys for CI and, later, an OIDC
// session cookie for humans, on the same endpoints.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (*Principal, error)
}

// StaticKeyAuthenticator authenticates bearer tokens against the keys parsed
// from EVIDENCE_API_KEYS. Those keys are shared secrets rather than identities,
// so each one is given a synthetic subject and a role set by the compatibility
// mapping described below.
type StaticKeyAuthenticator struct {
	entries []staticEntry
}

type staticEntry struct {
	key       []byte
	principal *Principal
}

// NewStaticKeyAuthenticator maps today's rw/ro keys onto roles.
//
//   - ro becomes viewer: read everything, write nothing. Same as before.
//   - rw becomes ci *and* admin. ci because an rw key is overwhelmingly a build
//     robot writing its own source, and admin because an rw key can post an
//     inheritance declaration today. Dropping either would make this release
//     start rejecting requests that worked in the last one, and the keys are
//     store-wide secrets in an operator's environment regardless.
//
// Operators who want the finer split hold off until phase 2, where principals
// live in the database and roles are granted individually.
func NewStaticKeyAuthenticator(keys []config.APIKey) *StaticKeyAuthenticator {
	a := &StaticKeyAuthenticator{}
	for i, k := range keys {
		roles := []Role{RoleCI, RoleAdmin}
		label := "rw"
		if k.ReadOnly {
			roles = []Role{RoleViewer}
			label = "ro"
		}
		subject := fmt.Sprintf("apikey:%s-%d", label, i+1)
		a.entries = append(a.entries, staticEntry{
			key:       []byte(k.Key),
			principal: NewPrincipal(subject, KindAPIKey, "configured "+label+" API key", roles...),
		})
	}
	return a
}

func (a *StaticKeyAuthenticator) Authenticate(_ context.Context, r *http.Request) (*Principal, error) {
	if len(a.entries) == 0 {
		return nil, ErrAuthDisabled
	}
	token := extractBearer(r)
	if token == "" {
		return nil, ErrNoCredentials
	}

	// Compare against every entry rather than breaking on the first match, so
	// the work done is the same whichever key was presented.
	tokenBytes := []byte(token)
	var matched *Principal
	for _, e := range a.entries {
		if len(tokenBytes) == len(e.key) && subtle.ConstantTimeCompare(tokenBytes, e.key) == 1 {
			matched = e.principal
		}
	}
	if matched == nil {
		return nil, ErrInvalidCredentials
	}
	return matched, nil
}

// Chain tries each authenticator in order and returns the first one that
// resolves a principal or rejects the credentials it was given. Authenticators
// that see nothing of their own are skipped.
type Chain []Authenticator

func (c Chain) Authenticate(ctx context.Context, r *http.Request) (*Principal, error) {
	// The chain is only disabled if every member is: one configured scheme is
	// enough to close the door.
	allDisabled := true
	for _, a := range c {
		p, err := a.Authenticate(ctx, r)
		switch {
		case err == nil:
			return p, nil
		case errors.Is(err, ErrAuthDisabled):
			continue
		case errors.Is(err, ErrNoCredentials):
			allDisabled = false
			continue
		default:
			return nil, err
		}
	}
	if allDisabled {
		return nil, ErrAuthDisabled
	}
	return nil, ErrNoCredentials
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
