package tests

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"
)

// mockIdP is an OpenID provider in the test process: discovery, a signing key,
// an authorization endpoint and a token endpoint.
//
// A real Keycloak in a container would test Keycloak. What is worth testing
// here is this store's half — that the state and PKCE checks hold, that a token
// which does not verify is refused, that claims become a principal and groups
// become roles — and for that a provider we can make misbehave on purpose is
// worth more than a correct one. It also keeps the suite fast enough to run on
// every change.
type mockIdP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string

	// What the next issued token will say.
	subject string
	email   string
	name    string
	groups  []string

	// Levers for the unhappy paths.
	signWithWrongKey bool
	issuerOverride   string
	audienceOverride string
	omitIDToken      bool
	// noEndSession drops the logout endpoint from discovery, which is a
	// provider that supports no RP-initiated logout at all — allowed, and the
	// store has to stay usable in front of one.
	noEndSession bool

	// codes maps an issued authorization code to the PKCE challenge it was
	// issued against, so the token endpoint can check the verifier the way a
	// real provider does.
	codes map[string]string
}

// endSessionEndpoint is what discovery advertises for logging out, or the empty
// string for a provider that offers none.
func (m *mockIdP) endSessionEndpoint() string {
	if m.noEndSession {
		return ""
	}
	return m.server.URL + "/logout"
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	idp := &mockIdP{
		key:     key,
		keyID:   "test-key-1",
		subject: "idp-subject-001",
		email:   "alice@example.com",
		name:    "Alice Example",
		codes:   map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeMockJSON(w, map[string]any{
			"issuer":                                idp.issuer(),
			"authorization_endpoint":                idp.server.URL + "/authorize",
			"token_endpoint":                        idp.server.URL + "/token",
			"jwks_uri":                              idp.server.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"end_session_endpoint":                  idp.endSessionEndpoint(),
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeMockJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     idp.keyID,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}})
	})

	// The browser would land here and sign in. The test drives the redirect
	// itself, so this only has to mint a code and remember the challenge.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		code := fmt.Sprintf("code-%d", time.Now().UnixNano())
		idp.codes[code] = r.URL.Query().Get("code_challenge")
		redirect, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := redirect.Query()
		q.Set("code", code)
		q.Set("state", r.URL.Query().Get("state"))
		redirect.RawQuery = q.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())

		challenge, known := idp.codes[r.Form.Get("code")]
		if !known {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		delete(idp.codes, r.Form.Get("code"))

		// The PKCE check, done as a real provider does it: the verifier the
		// client kept must hash to the challenge it sent at the start.
		if s256(r.Form.Get("code_verifier")) != challenge {
			http.Error(w, `{"error":"invalid_grant","error_description":"pkce"}`, http.StatusBadRequest)
			return
		}

		body := map[string]any{"access_token": "mock-access", "token_type": "Bearer"}
		if !idp.omitIDToken {
			body["id_token"] = idp.signIDToken(t)
		}
		writeMockJSON(w, body)
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (m *mockIdP) issuer() string {
	if m.issuerOverride != "" {
		return m.issuerOverride
	}
	return m.server.URL
}

func (m *mockIdP) signIDToken(t *testing.T) string {
	t.Helper()

	signingKey := m.key
	if m.signWithWrongKey {
		// A token signed by a key the provider never published: what an
		// attacker minting their own would look like.
		other, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		signingKey = other
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: signingKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", m.keyID),
	)
	require.NoError(t, err)

	audience := mockClientID
	if m.audienceOverride != "" {
		audience = m.audienceOverride
	}

	claims := map[string]any{
		"iss":    m.issuer(),
		"sub":    m.subject,
		"aud":    audience,
		"exp":    time.Now().Add(5 * time.Minute).Unix(),
		"iat":    time.Now().Unix(),
		"email":  m.email,
		"name":   m.name,
		"groups": m.groups,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// s256 is the PKCE challenge derivation, computed here independently of the
// implementation under test rather than borrowed from it — a check that agrees
// with the code by construction is not a check.
func s256(verifier string) string {
	if verifier == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func writeMockJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
