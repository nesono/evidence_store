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
	// ExternalID is the identity provider's own name for this person,
	// "<issuer>|<sub>". Empty for an API key, which answers to nobody but this
	// store. It is what a login matches on, so that a changed email address
	// corrects the subject rather than creating a second principal.
	ExternalID string `json:"external_id,omitempty"`
}

// SCIMUser is a principal as a provisioning client sees it.
//
// A separate shape from Principal rather than more fields on it, because the
// two are answering different questions. Principal is what authentication and
// the Access tab need; this is one resource in a protocol with its own names
// for things, its own idea of identity, and a client that will send back what
// it was given.
type SCIMUser struct {
	ID uuid.UUID
	// SCIMID is the id this store hands the provisioner, and the one every
	// later request addresses this person by.
	SCIMID string
	// ExternalID is the provisioner's own name for them. Stored and echoed
	// back, never matched on here: Entra maps it from mailNickname by default
	// and a tenant may map it to anything at all.
	ExternalID string
	// UserName is SCIM's login name, unique across the store. Usually a UPN,
	// which is often not the address in Subject.
	UserName    string
	Subject     string
	DisplayName string
	// Active false is how a directory says somebody has left. It is the whole
	// reason for provisioning: the account here is shut immediately rather than
	// at the next login that never comes.
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
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

// Where a role binding came from. IdP-derived grants are reconciled to the
// caller's claims on every login; local ones are an administrator's decision
// and a login must not undo them.
const (
	GrantSourceLocal = "local"
	GrantSourceIdP   = "idp"
)

// Session is a logged-in browser. It is a row rather than a signed cookie so
// that revoking a principal, or logging somebody out, takes effect on their
// next request — the same promise API keys have had since phase 2.
type Session struct {
	ID          uuid.UUID  `json:"id"`
	PrincipalID uuid.UUID  `json:"principal_id"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	UserAgent   string     `json:"user_agent,omitempty"`
}
