package tests

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/server"
	"github.com/nesono/evidence-store/internal/store"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// samlServer stands the store up with SAML pointed at the mock provider, and
// registers the store with it the way an administrator would.
func samlServer(t *testing.T, idp *mockSAMLIdP, roleMap map[string]string) (string, *http.Client) {
	t.Helper()

	certFile, keyFile := writeSPKeypair(t)

	// The service provider's own URLs go into the metadata the provider is
	// given, so the address has to be known before the handler is built.
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()

	samlCfg := config.SAML{
		IDPMetadataFile: idp.metadataFile(t),
		RootURL:         base,
		CertFile:        certFile,
		KeyFile:         keyFile,
		EmailAttribute:  "mail",
		NameAttribute:   "cn",
		// crewjam's identity provider reports group membership as
		// eduPersonAffiliation, which is as good an illustration as any of why
		// the attribute name is configurable at all.
		GroupsAttribute: "eduPersonAffiliation",
	}

	provider, err := auth.NewSAMLProvider(context.Background(), samlCfg)
	require.NoError(t, err)

	cfg := &config.Config{
		DatabaseURL:     testDatabaseURL,
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		Auth: config.Auth{
			DB:      true,
			SAML:    samlCfg,
			RoleMap: roleMap,
			Session: config.Session{TTL: time.Hour, CookieSecure: false},
		},
		Blob: testBlobConfig,
	}

	ts.Config.Handler = server.New(cfg, testPool, testBlobStore, server.SSO{SAML: provider}).Handler()
	ts.Start()
	t.Cleanup(ts.Close)

	idp.registerSP(t, base)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return base, &http.Client{Jar: jar}
}

// samlLogIn walks the flow a browser walks: out to the provider, and back with
// the assertion its auto-submitting form carries.
func samlLogIn(t *testing.T, base string, client *http.Client) *http.Response {
	t.Helper()

	resp, err := client.Get(base + auth.SAMLLoginPath)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the provider should answer with its post form")
	form := rawBody(t, resp)

	return postAssertion(t, client, base, form)
}

func dropSAMLPrincipal(t *testing.T) {
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(),
			`DELETE FROM principals WHERE external_id LIKE $1`, "%alice-saml-nameid")
		assert.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Tests: the flow
// ---------------------------------------------------------------------------

// The last piece of docs/rbac-design.md: a second front end, and nothing behind
// Principal able to tell which one somebody came through.
func TestSAMLLoginCreatesAPrincipalAndASession(t *testing.T) {
	idp := newMockSAMLIdP(t)
	idp.groups = []string{"eng-leads"}
	dropSAMLPrincipal(t)

	base, client := samlServer(t, idp, map[string]string{"eng-leads": "admin"})
	resp := samlLogIn(t, base, client)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the assertion should land back on the app")

	me := meOf(t, base, client)
	assert.True(t, me.Authenticated)
	assert.True(t, me.ViaSession)
	assert.Equal(t, model.PrincipalKindUser, me.Kind)
	assert.Equal(t, "user:alice@example.com", me.Subject, "the readable name is what evidence is filed under")
	assert.Equal(t, []string{"admin"}, me.Roles, "from the group mapping, exactly as OIDC does it")
	assert.Contains(t, me.LoginMethods, "saml")
	assert.NotContains(t, me.LoginMethods, "oidc")
}

// A SAML login and an OIDC one produce the same kind of principal, the same
// session, and the same roles. That equivalence is what the whole design was
// for, so it is worth asserting rather than assuming.
func TestASAMLPrincipalIsAnOrdinaryPrincipal(t *testing.T) {
	idp := newMockSAMLIdP(t)
	idp.groups = []string{"eng-all"}
	dropSAMLPrincipal(t)

	base, client := samlServer(t, idp, map[string]string{"eng-all": "contributor"})
	samlLogIn(t, base, client).Body.Close()
	me := meOf(t, base, client)

	// It writes evidence, and the source binding fills in its subject.
	csrf := cookieNamed(t, client, base, auth.CSRFCookie)
	require.NotNil(t, csrf)
	record := makeEvidence("org/saml_check", "main", "r1", "//pkg:test", "", model.ResultPass)
	resp := postAs(t, client, base+"/api/v1/evidence", record, csrf.Value)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := decodeJSON[model.Evidence](t, resp)
	assert.Equal(t, me.Subject, created.Source)

	// It shows up in the Access tab's listing like anybody else, and can be
	// revoked from there.
	var kind, externalID string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT kind, external_id FROM principals WHERE subject = $1`, me.Subject).Scan(&kind, &externalID))
	assert.Equal(t, model.PrincipalKindUser, kind)
	assert.Contains(t, externalID, "alice-saml-nameid")

	_, err := testPool.Exec(context.Background(),
		`UPDATE principals SET disabled_at = now() WHERE subject = $1`, me.Subject)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, statusOf(t, base, client, "/api/v1/me"),
		"revocation cuts a SAML session exactly as it cuts an OIDC one")
}

// Roles are reconciled on each login, and an administrator's grants survive —
// the same rules as OIDC, because it is the same code.
func TestSAMLGroupsReconcileAndLocalGrantsSurvive(t *testing.T) {
	idp := newMockSAMLIdP(t)
	idp.groups = []string{"eng-all", "eng-leads"}
	dropSAMLPrincipal(t)

	base, client := samlServer(t, idp, map[string]string{"eng-all": "contributor", "eng-leads": "admin"})
	samlLogIn(t, base, client).Body.Close()
	me := meOf(t, base, client)
	require.ElementsMatch(t, []string{"admin", "contributor"}, me.Roles)

	_, err := testPool.Exec(context.Background(), `
		INSERT INTO role_bindings (principal_id, role, scope, source)
		SELECT id, 'ci', '*', 'local' FROM principals WHERE subject = $1
	`, me.Subject)
	require.NoError(t, err)

	// Demoted at the directory.
	idp.groups = []string{"eng-all"}
	jar, _ := cookiejar.New(nil)
	second := &http.Client{Jar: jar}
	samlLogIn(t, base, second).Body.Close()

	assert.ElementsMatch(t, []string{"contributor", "ci"}, meOf(t, base, second).Roles,
		"the login drops its own admin grant and leaves the administrator's ci alone")
}

func TestSAMLMetadataDescribesThisStore(t *testing.T) {
	idp := newMockSAMLIdP(t)
	base, _ := samlServer(t, idp, nil)

	resp, err := http.Get(base + auth.SAMLMetadataPath)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/samlmetadata+xml", resp.Header.Get("Content-Type"))

	body := rawBody(t, resp)
	// The two things an administrator registering this store actually needs:
	// where assertions go, and the certificate that signs requests.
	assert.Contains(t, body, base+auth.SAMLACSPath)
	assert.Contains(t, body, "X509Certificate")
	assert.NotContains(t, body, "PRIVATE KEY", "the metadata is public")
}

// ---------------------------------------------------------------------------
// Tests: the refusals
// ---------------------------------------------------------------------------

// An assertion answering a request nobody here made. This is the check the
// saml_requests table exists for — SAML has nowhere client-side to keep it,
// because the assertion arrives as a cross-site POST.
func TestAnAssertionForALoginNobodyStartedIsRefused(t *testing.T) {
	idp := newMockSAMLIdP(t)
	dropSAMLPrincipal(t)
	base, client := samlServer(t, idp, nil)

	// Capture a valid assertion...
	resp, err := client.Get(base + auth.SAMLLoginPath)
	require.NoError(t, err)
	form := rawBody(t, resp)

	// ...then forget the request it answers, as an expiry sweep would.
	_, err = testPool.Exec(context.Background(), `DELETE FROM saml_requests`)
	require.NoError(t, err)

	replay := postAssertion(t, client, base, form)
	defer replay.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, replay.StatusCode)
	assert.Nil(t, cookieNamed(t, client, base, auth.SessionCookie))
}

// The same assertion twice. The request is consumed on the way through, so the
// second attempt is answering a login that is no longer outstanding.
func TestAnAssertionCannotBeUsedTwice(t *testing.T) {
	idp := newMockSAMLIdP(t)
	idp.groups = []string{"eng-all"}
	dropSAMLPrincipal(t)

	base, client := samlServer(t, idp, map[string]string{"eng-all": "contributor"})

	resp, err := client.Get(base + auth.SAMLLoginPath)
	require.NoError(t, err)
	form := rawBody(t, resp)

	first := postAssertion(t, client, base, form)
	first.Body.Close()
	require.Equal(t, http.StatusOK, first.StatusCode)

	second := postAssertion(t, client, base, form)
	defer second.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, second.StatusCode,
		"the request id was consumed by the first use")
}

// A tampered assertion is the one failure here worth alarming about.
func TestATamperedAssertionIsRefused(t *testing.T) {
	idp := newMockSAMLIdP(t)
	dropSAMLPrincipal(t)
	base, client := samlServer(t, idp, nil)

	resp, err := client.Get(base + auth.SAMLLoginPath)
	require.NoError(t, err)
	form := rawBody(t, resp)

	// Flip a character inside the base64 payload: the signature no longer
	// covers what the document says.
	tampered := corruptSAMLResponse(t, form)

	bad := postAssertion(t, client, base, tampered)
	defer bad.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, bad.StatusCode)
	assert.Nil(t, cookieNamed(t, client, base, auth.SessionCookie))
}

// An assertion signed by a key the provider never published — somebody minting
// their own.
func TestAnAssertionFromAnotherProviderIsRefused(t *testing.T) {
	real := newMockSAMLIdP(t)
	dropSAMLPrincipal(t)
	base, client := samlServer(t, real, nil)

	// A second provider, unknown to this store, that has been told about the
	// store and will happily issue assertions for it.
	impostor := newMockSAMLIdP(t)
	impostor.registerSP(t, base)

	form := assertionFromAnotherProvider(t, impostor, base)
	require.True(t, samlResponseField.MatchString(form),
		"the impostor should have produced a well-formed assertion")

	bad := postAssertion(t, client, base, form)
	defer bad.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, bad.StatusCode)
	assert.Nil(t, cookieNamed(t, client, base, auth.SessionCookie))
}

// A revoked person can still authenticate with the directory; this store is
// where that stops.
func TestADisabledPrincipalCannotLogInWithSAML(t *testing.T) {
	idp := newMockSAMLIdP(t)
	idp.groups = []string{"eng-all"}
	dropSAMLPrincipal(t)

	base, client := samlServer(t, idp, map[string]string{"eng-all": "contributor"})
	samlLogIn(t, base, client).Body.Close()
	me := meOf(t, base, client)

	_, err := testPool.Exec(context.Background(),
		`UPDATE principals SET disabled_at = now() WHERE subject = $1`, me.Subject)
	require.NoError(t, err)

	jar, _ := cookiejar.New(nil)
	resp := samlLogIn(t, base, &http.Client{Jar: jar})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Tests: housekeeping
// ---------------------------------------------------------------------------

func TestPendingSAMLRequestsExpireAndAreSwept(t *testing.T) {
	ctx := context.Background()
	idp := newMockSAMLIdP(t)
	base, client := samlServer(t, idp, nil)

	resp, err := client.Get(base + auth.SAMLLoginPath)
	require.NoError(t, err)
	resp.Body.Close()

	requests := store.NewSAMLRequestStore(testPool)
	pending, err := requests.Pending(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pending, "the login should be outstanding")

	_, err = testPool.Exec(ctx, `UPDATE saml_requests SET expires_at = now() - INTERVAL '1 minute'`)
	require.NoError(t, err)

	// Expired ids stop counting whether or not the sweep has run.
	pending, err = requests.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)

	swept, err := requests.DeleteExpired(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, swept, int64(1))
}
