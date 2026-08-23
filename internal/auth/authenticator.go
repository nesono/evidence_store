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

	// ErrAuthUnavailable means the authenticator could not reach what it checks
	// against — the database is down, not the credential wrong. It fails the
	// request closed like any other error, but as a 503 rather than a 401, so
	// an outage does not present itself to every caller as a bad key.
	ErrAuthUnavailable = errors.New("authentication backend unavailable")
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

// Chain tries every authenticator in order and returns the first principal any
// of them resolves. Authenticators that see nothing of their own are skipped.
//
// A rejection does not stop the chain; it is remembered and returned only if
// nothing later recognises the caller. Phase 1 stopped at the first rejection,
// on the reasoning that a presented-but-wrong credential is an answer rather
// than an absence. That was indistinguishable from correct while one scheme
// read the Authorization header, and is wrong now that two do: an env-var key
// and a database key arrive in the same header, and whichever authenticator is
// asked first has never heard of the other's keys. Deferring is what phase 5
// needs too, where a stale bearer token should not shadow a valid session
// cookie on the same request.
//
// Nothing is loosened by this. A credential no scheme accepts is still
// rejected, with the first rejection's reason; it just takes every scheme
// having looked to conclude that.
type Chain []Authenticator

func (c Chain) Authenticate(ctx context.Context, r *http.Request) (*Principal, error) {
	// The chain is only disabled if every member is: one configured scheme is
	// enough to close the door.
	allDisabled := true
	var rejected error
	for _, a := range c {
		p, err := a.Authenticate(ctx, r)
		switch {
		case err == nil:
			return p, nil
		case errors.Is(err, ErrAuthDisabled):
			continue
		case errors.Is(err, ErrNoCredentials):
			allDisabled = false
		case errors.Is(err, ErrAuthUnavailable):
			// Not an answer about the credential at all. Asking the remaining
			// schemes would risk reporting "wrong key" during an outage.
			return nil, err
		default:
			allDisabled = false
			if rejected == nil {
				rejected = err
			}
		}
	}
	if rejected != nil {
		return nil, rejected
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
