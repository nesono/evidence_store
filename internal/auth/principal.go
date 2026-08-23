package auth

import (
	"context"
	"slices"

	"github.com/google/uuid"
)

// Kind distinguishes a machine credential from a human one. Phases 1 and 2 only
// ever mint KindAPIKey; KindUser is what an SSO login will produce.
type Kind string

const (
	KindAPIKey Kind = "api_key"
	KindUser   Kind = "user"
)

// ParseKind reads the kind column. An unrecognised value is treated as a
// machine credential: the column is CHECK-constrained to the two, so anything
// else is a database from the future, and the conservative reading of an
// unknown caller is the one that is not a person.
func ParseKind(s string) Kind {
	if Kind(s) == KindUser {
		return KindUser
	}
	return KindAPIKey
}

// Principal is the caller's identity, as resolved by an Authenticator. It
// replaces the bare role that used to sit in the request context: everything
// downstream of authentication can now tell one caller from another, which is
// what binding a record's source to its author (phase 3) needs.
type Principal struct {
	// ID is the principals row this caller came from. Zero for a principal
	// that has no row: an env-var key, which is a secret rather than a record
	// of anybody.
	ID          uuid.UUID
	Subject     string // "ci:nightly-build" or "user:alice@example.com"
	Kind        Kind
	DisplayName string
	Roles       []Role
	// ViaSession records that this caller arrived with a cookie rather than a
	// bearer token. It is what the CSRF check keys on: a cookie is sent by the
	// browser whether or not the page meant to send it, and an Authorization
	// header never is.
	ViaSession bool

	perms permSet // flattened from Roles once, at authentication time
}

// NewPrincipal flattens roles into the permission set the request will be
// checked against. Duplicate and unknown roles are harmless: the former
// collapse, the latter contribute nothing.
func NewPrincipal(subject string, kind Kind, displayName string, roles ...Role) *Principal {
	perms := permSet{}
	for _, r := range roles {
		for p := range rolePermissions[r] {
			perms[p] = struct{}{}
		}
	}
	return &Principal{
		Subject:     subject,
		Kind:        kind,
		DisplayName: displayName,
		Roles:       roles,
		perms:       perms,
	}
}

// Can reports whether the principal holds perm.
func (p *Principal) Can(perm Permission) bool {
	if p == nil {
		return false
	}
	_, ok := p.perms[perm]
	return ok
}

// Permissions returns everything the principal may do, sorted so the answer is
// stable. Callers are the "who am I" endpoint and the web UI deciding which
// controls to show — a client that renders a button it will be refused for is
// worse than one that renders nothing.
func (p *Principal) Permissions() []Permission {
	if p == nil {
		return []Permission{}
	}
	perms := make([]Permission, 0, len(p.perms))
	for perm := range p.perms {
		perms = append(perms, perm)
	}
	slices.Sort(perms)
	return perms
}

// HasRole reports whether the principal was granted role directly.
func (p *Principal) HasRole(role Role) bool {
	if p == nil {
		return false
	}
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

type contextKey string

const (
	principalKey    contextKey = "auth_principal"
	authDisabledKey contextKey = "auth_disabled"
)

// WithPrincipal returns a context carrying p. Exported for tests and for
// authenticators living outside this package later on.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the authenticated principal, if any. A request can
// legitimately have none: with no credentials configured the store runs open,
// which is the default local-development posture.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey).(*Principal)
	return p, ok && p != nil
}

// withAuthDisabled marks a request as having passed through an authentication
// stage that had nothing configured to check. Require honours the marker and
// nothing else sets it, so a route that is missing Authenticate entirely fails
// closed rather than falling open.
func withAuthDisabled(ctx context.Context) context.Context {
	return context.WithValue(ctx, authDisabledKey, true)
}

func authDisabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(authDisabledKey).(bool)
	return disabled
}
