package auth

import (
	"context"
	"fmt"
	"net/url"
	"slices"
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
	// endSession is the provider's RP-initiated logout endpoint, read from the
	// discovery document. Empty for a provider that advertises none, which is
	// allowed: logout then ends the session here and nothing more.
	endSession string
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

	// Not every provider advertises this, and one that does not is not an
	// error — it just means there is no provider session to end.
	var discovery struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&discovery); err != nil {
		return nil, fmt.Errorf("read discovery document for %q: %w", cfg.Issuer, err)
	}

	return &OIDCProvider{
		cfg:        cfg,
		endSession: discovery.EndSessionEndpoint,
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
	// PreferredUsername is what the provider calls this person's login name.
	// Entra sends the UPN, which is frequently not the address in Email — and a
	// directory that provisioned them may know them by only one of the two.
	PreferredUsername string
	Name              string
	Groups            []string
	Issuer            string
	// IDToken is the raw token this login arrived on, kept so that logging out
	// can hand it back as id_token_hint. Empty from any front end that does not
	// have one, SAML included.
	IDToken string
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

// LoginNames are the names somebody may already be known by to a provisioner
// that created their account before they first logged in. Both are offered
// because a UPN and an email address are often not the same string, and which
// of them a directory sends is its own choice.
func (c Claims) LoginNames() []string {
	names := make([]string, 0, 2)
	for _, name := range []string{c.Email, c.PreferredUsername} {
		if name == "" || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
	}
	return names
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
		Subject:           idToken.Subject,
		IDToken:           rawID,
		Issuer:            idToken.Issuer,
		Email:             stringClaim(raw, "email"),
		PreferredUsername: stringClaim(raw, "preferred_username"),
		Name:              stringClaim(raw, "name"),
		Groups:            stringsClaim(raw, p.cfg.GroupsClaim),
	}, nil
}

// EndSessionURL is where to send a browser so the provider ends its own session
// too, and the empty string when there is nowhere to send it.
//
// Without this, logging out is local only: the provider still considers the
// person signed in, so the next login is answered silently and instantly, which
// looks exactly like the logout button not working. It is worse than cosmetic
// on a shared machine, where the next person inherits the session.
//
// idToken goes as id_token_hint, which is what lets the provider end the right
// session without stopping to ask the human which one they meant. client_id is
// sent alongside because a provider given no hint — a session that predates the
// stored token — needs some way to know who is asking.
func (p *OIDCProvider) EndSessionURL(idToken, postLogoutRedirect string) string {
	if p.endSession == "" {
		return ""
	}
	u, err := url.Parse(p.endSession)
	if err != nil {
		// The provider published something unparseable. Ending the local
		// session and staying here beats sending a browser to a broken URL.
		return ""
	}
	q := u.Query()
	if idToken != "" {
		q.Set("id_token_hint", idToken)
	}
	q.Set("client_id", p.cfg.ClientID)
	if postLogoutRedirect != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirect)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RolesForGroups turns the groups an identity provider reports into roles here.
//
// Shared by both front ends, because which of our groups means which of your
// roles is one question and does not become two just because the protocol did.
//
// A group with no entry in the map grants nothing, so pointing this store at a
// company directory does not hand every employee an account that can write.
// Unknown role names are dropped for the same reason a role_bindings row naming
// one grants nothing: a mapping can outlive the constant it names.
func RolesForGroups(roleMap map[string]string, groups []string) []string {
	seen := map[string]struct{}{}
	for _, group := range groups {
		name, mapped := roleMap[group]
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
