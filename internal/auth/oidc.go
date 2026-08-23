package auth

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/nesono/evidence-store/internal/config"
)

// OIDCProvider is the slot docs/rbac-design.md section 9 marked out, filled in.
//
// Everything from Principal inward already works without knowing where a caller
// came from, so single sign-on is a front end: it turns a person at an identity
// provider into a principals row and a session, and the rest of the store
// carries on as it did for API keys. SAML replaces this front end and nothing
// behind it.
type OIDCProvider struct {
	cfg      config.OIDC
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewOIDCProvider fetches the provider's discovery document, which is also the
// first honest check that the configuration works: an unreachable issuer or a
// typo in the URL fails at startup rather than the first time somebody tries to
// log in.
func NewOIDCProvider(ctx context.Context, cfg config.OIDC) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC issuer %q: %w", cfg.Issuer, err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}

	return &OIDCProvider{
		cfg: cfg,
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

// AuthCodeURL is where the browser is sent to log in. Authorization Code with
// PKCE: the verifier never leaves this server, so an authorization code
// intercepted on its way back cannot be redeemed by whoever caught it.
func (p *OIDCProvider) AuthCodeURL(state, verifier string) string {
	return p.oauth.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", oauth2.S256ChallengeFromVerifier(verifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// Claims is what a login tells us about a person: who they are, what to call
// them, and which groups they are in.
type Claims struct {
	// Subject is the provider's own identifier, stable across renames. It is
	// what a principal is matched on.
	Subject string
	Email   string
	Name    string
	Groups  []string
	Issuer  string
}

// ExternalID is the key an upsert matches on, qualified by issuer so that two
// providers cannot collide on a subject that is only unique within one of them.
func (c Claims) ExternalID() string { return c.Issuer + "|" + c.Subject }

// PrincipalSubject is the readable name this person files evidence under.
//
// Their email address if the provider gave one, because that is what a reader
// looking at a record months later can act on. Failing that the opaque subject,
// which is ugly but resolvable — and better than inventing something that looks
// like an address and is not.
func (c Claims) PrincipalSubject() string {
	if c.Email != "" {
		return "user:" + c.Email
	}
	return "user:" + c.Subject
}

// Exchange redeems the authorization code and verifies the ID token that comes
// back — signature, issuer, audience and expiry, all by go-oidc against the
// provider's published keys.
func (p *OIDCProvider) Exchange(ctx context.Context, code, verifier string) (*Claims, error) {
	token, err := p.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}

	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		// Without an ID token there is no identity, only permission to call an
		// API on somebody's behalf. That is not what this flow is for.
		return nil, fmt.Errorf("provider returned no id_token")
	}

	idToken, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}

	// Claims are read into a map rather than a struct because the group claim's
	// name is configurable — providers disagree, and Entra calls it "roles".
	var raw map[string]any
	if err := idToken.Claims(&raw); err != nil {
		return nil, fmt.Errorf("read id_token claims: %w", err)
	}

	return &Claims{
		Subject: idToken.Subject,
		Issuer:  idToken.Issuer,
		Email:   stringClaim(raw, "email"),
		Name:    stringClaim(raw, "name"),
		Groups:  stringsClaim(raw, p.cfg.GroupsClaim),
	}, nil
}

// RolesFor turns the groups a provider reports into roles here.
//
// A group with no entry in the map grants nothing, so pointing this store at a
// company IdP does not hand every employee an account that can write. Unknown
// role names are dropped for the same reason a role_bindings row naming one
// grants nothing: a mapping can outlive the constant it names.
func (p *OIDCProvider) RolesFor(groups []string) []string {
	seen := map[string]struct{}{}
	for _, group := range groups {
		name, mapped := p.cfg.RoleMap[group]
		if !mapped {
			continue
		}
		if _, ok := ParseRole(name); !ok {
			continue
		}
		seen[name] = struct{}{}
	}

	roles := make([]string, 0, len(seen))
	for role := range seen {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func stringClaim(raw map[string]any, key string) string {
	s, _ := raw[key].(string)
	return strings.TrimSpace(s)
}

// stringsClaim reads a claim that should be a list of strings but might be one
// string: providers vary, and a single group arriving unwrapped should not
// silently mean no groups at all.
func stringsClaim(raw map[string]any, key string) []string {
	switch v := raw[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	default:
		return nil
	}
}
