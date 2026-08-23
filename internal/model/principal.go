package model

import (
	"time"

	"github.com/google/uuid"
)

// Principal is a stored identity: a row of the principals table together with
// the roles bound to it.
//
// It is deliberately the database's shape and not the authorization layer's.
// Roles are plain strings here, because a binding written last release may name
// a role this binary no longer defines, and it is internal/auth's job to decide
// that such a binding grants nothing. The key hash never leaves the store.
type Principal struct {
	ID          uuid.UUID  `json:"id"`
	Subject     string     `json:"subject"`
	Kind        string     `json:"kind"`
	DisplayName string     `json:"display_name"`
	Roles       []string   `json:"roles"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

// Disabled reports whether the principal has been revoked. A disabled
// principal is kept rather than deleted so that evidence already attributed to
// it still names something.
func (p *Principal) Disabled() bool {
	return p != nil && p.DisabledAt != nil
}

// PrincipalCreate is a new identity together with the roles it starts with.
// The caller mints the key and hashes it, so no layer below this one ever
// handles a plaintext secret.
//
// The roles travel with the identity rather than being granted afterwards
// because the two halves have to land together: a principal created without its
// roles is a credential somebody has been handed that can do nothing, and the
// plaintext is gone by the time anyone notices.
type PrincipalCreate struct {
	Subject     string
	Kind        string
	DisplayName string
	// KeyHash must be set for Kind api_key and empty for a user, matching the
	// table's own CHECK.
	KeyHash string
	Roles   []string
	// GrantedBy is the principal issuing the credential, or nil when the server
	// issues on its own authority — which is how the bootstrap admin is created,
	// before any principal exists to credit.
	GrantedBy *uuid.UUID
}

// Principal kinds. A principal is either a machine credential or a human, and
// phase 5's SSO login is what starts producing the second.
const (
	PrincipalKindAPIKey = "api_key"
	PrincipalKindUser   = "user"
)

// ScopeStoreWide is the only role-binding scope written today. Per-repo scoping
// is reserved but inert; see migration 000006.
const ScopeStoreWide = "*"
