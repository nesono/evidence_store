package tests

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/store"
)

// mockSAMLIdP is a SAML identity provider in the test process, built on
// crewjam's own IdentityProvider so the assertions it signs are real ones.
//
// Same reasoning as the OIDC mock: a container running a real directory would
// mostly test the directory. What is worth testing is this store's half — that
// a signature is checked, that an assertion answering a request nobody made is
// refused, that attributes become a principal — and for that a provider we can
// make misbehave on purpose is worth more than a correct one.
type mockSAMLIdP struct {
	server *httptest.Server
	idp    *saml.IdentityProvider

	// What the next assertion will say.
	nameID string
	email  string
	name   string
	groups []string

	// The service provider metadata registered with this IdP, filled in once
	// the store is serving its own.
	spMetadata *saml.EntityDescriptor
}

// GetServiceProvider satisfies saml.ServiceProviderProvider.
func (m *mockSAMLIdP) GetServiceProvider(_ *http.Request, _ string) (*saml.EntityDescriptor, error) {
	if m.spMetadata == nil {
		return nil, os.ErrNotExist
	}
	return m.spMetadata, nil
}

// GetSession satisfies saml.SessionProvider: the person is already signed in,
// because the login screen is not what these tests are about.
func (m *mockSAMLIdP) GetSession(_ http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	return &saml.Session{
		ID:             "session-" + m.nameID,
		CreateTime:     time.Now(),
		ExpireTime:     time.Now().Add(time.Hour),
		Index:          "0",
		NameID:         m.nameID,
		UserEmail:      m.email,
		UserCommonName: m.name,
		Groups:         m.groups,
	}
}

func newMockSAMLIdP(t *testing.T) *mockSAMLIdP {
	t.Helper()

	key, cert := selfSignedKeypair(t, "mock-saml-idp")

	m := &mockSAMLIdP{
		nameID: "alice-saml-nameid",
		email:  "alice@example.com",
		name:   "Alice Example",
	}

	mux := http.NewServeMux()
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)

	metadataURL, _ := url.Parse(m.server.URL + "/metadata")
	ssoURL, _ := url.Parse(m.server.URL + "/sso")

	m.idp = &saml.IdentityProvider{
		Key:                     key,
		Certificate:             cert,
		MetadataURL:             *metadataURL,
		SSOURL:                  *ssoURL,
		ServiceProviderProvider: m,
		SessionProvider:         m,
	}

	mux.HandleFunc("/metadata", m.idp.ServeMetadata)
	mux.HandleFunc("/sso", m.idp.ServeSSO)
	return m
}

// metadataFile writes the provider's metadata where the store can read it,
// which is the posture of a deployment that will not reach out to fetch it.
func (m *mockSAMLIdP) metadataFile(t *testing.T) string {
	t.Helper()
	raw, err := xml.MarshalIndent(m.idp.Metadata(), "", "  ")
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "idp-metadata.xml")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

// registerSP tells the IdP about the store, by fetching the metadata the store
// serves — which is exactly what an administrator does by hand.
func (m *mockSAMLIdP) registerSP(t *testing.T, storeURL string) {
	t.Helper()
	resp, err := http.Get(storeURL + "/auth/saml/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := rawBody(t, resp)
	metadata, err := samlsp.ParseMetadata([]byte(body))
	require.NoError(t, err)
	m.spMetadata = metadata
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

func selfSignedKeypair(t *testing.T, commonName string) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return key, cert
}

// writeSPKeypair produces the X.509 pair a service provider must have, in the
// PEM files the configuration points at.
func writeSPKeypair(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, cert := selfSignedKeypair(t, "evidence-store-test-sp")

	dir := t.TempDir()
	certFile = filepath.Join(dir, "sp.crt")
	keyFile = filepath.Join(dir, "sp.key")

	require.NoError(t, os.WriteFile(certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600))
	require.NoError(t, os.WriteFile(keyFile,
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	return certFile, keyFile
}

// ---------------------------------------------------------------------------
// Driving the browser's half of the flow
// ---------------------------------------------------------------------------

var (
	samlResponseField = regexp.MustCompile(`name="SAMLResponse" value="([^"]*)"`)
	relayStateField   = regexp.MustCompile(`name="RelayState" value="([^"]*)"`)
)

// postAssertion does what the browser does with the auto-submitting form the
// identity provider returns: pull the fields out and POST them to the ACS.
func postAssertion(t *testing.T, client *http.Client, storeURL, formHTML string) *http.Response {
	t.Helper()

	response := samlResponseField.FindStringSubmatch(formHTML)
	require.Len(t, response, 2, "no SAMLResponse in the provider's form: %s", truncate(formHTML))

	form := url.Values{"SAMLResponse": {html.UnescapeString(response[1])}}
	if relay := relayStateField.FindStringSubmatch(formHTML); len(relay) == 2 {
		form.Set("RelayState", html.UnescapeString(relay[1]))
	}

	resp, err := client.PostForm(storeURL+"/auth/saml/acs", form)
	require.NoError(t, err)
	return resp
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// corruptSAMLResponse flips a byte inside the signed document, so the signature
// no longer covers what it says.
func corruptSAMLResponse(t *testing.T, formHTML string) string {
	t.Helper()

	match := samlResponseField.FindStringSubmatch(formHTML)
	require.Len(t, match, 2)

	decoded, err := base64.StdEncoding.DecodeString(html.UnescapeString(match[1]))
	require.NoError(t, err)

	// Somewhere in the middle, inside the assertion rather than the envelope.
	at := len(decoded) / 2
	decoded[at] ^= 0xFF

	return strings.Replace(formHTML, match[1],
		html.EscapeString(base64.StdEncoding.EncodeToString(decoded)), 1)
}

// assertionFromAnotherProvider gets a properly formed, properly signed
// assertion for this store — from a provider the store has never been told
// about.
//
// The request id it answers is planted in the store's own table first, so that
// the request-id check passes and the signature is the only thing left to
// refuse it. Otherwise this would prove nothing beyond what the
// unknown-request test already proves.
func assertionFromAnotherProvider(t *testing.T, impostor *mockSAMLIdP, storeURL string) string {
	t.Helper()

	certFile, keyFile := writeSPKeypair(t)
	provider, err := auth.NewSAMLProvider(context.Background(), config.SAML{
		IDPMetadataFile: impostor.metadataFile(t),
		RootURL:         storeURL,
		CertFile:        certFile,
		KeyFile:         keyFile,
	})
	require.NoError(t, err)

	redirect, requestID, err := provider.AuthnRequest("/")
	require.NoError(t, err)

	// Plant it, so the assertion is refused for its signature and nothing else.
	require.NoError(t, store.NewSAMLRequestStore(testPool).
		Remember(context.Background(), requestID, time.Now().Add(time.Minute)))

	resp, err := http.Get(redirect.String())
	require.NoError(t, err)
	return rawBody(t, resp)
}
