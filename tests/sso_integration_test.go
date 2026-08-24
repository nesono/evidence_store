package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
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

const mockClientID = "evidence-store-test"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ssoServer stands up the store with single sign-on pointed at a mock provider,
// and returns a browser-like client: a cookie jar, and no automatic following
// of the final redirect so a test can see where it was sent.
func ssoServer(t *testing.T, idp *mockIdP, roleMap map[string]string) (string, *http.Client) {
	t.Helper()

	// The redirect URL has to be an absolute address the provider will send a
	// browser back to, and a real deployment spells it out in configuration for
	// exactly that reason. Here it means learning the port before building the
	// handler, so the server is created unstarted and given its handler after.
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()

	oidcCfg := config.OIDC{
		Issuer:       idp.server.URL,
		ClientID:     mockClientID,
		ClientSecret: "test-secret",
		RedirectURL:  base + "/auth/callback",
		Scopes:       []string{"openid", "profile", "email", "groups"},
		GroupsClaim:  "groups",
		RoleMap:      roleMap,
	}
	// The test server speaks plain HTTP, which is the one setting a real
	// deployment should never copy.
	sessionCfg := config.Session{TTL: time.Hour, CookieSecure: false}

	provider, err := auth.NewOIDCProvider(context.Background(), oidcCfg)
	require.NoError(t, err)

	cfg := &config.Config{
		DatabaseURL:     testDatabaseURL,
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		Auth: config.Auth{
			DB: true, OIDC: oidcCfg, RoleMap: roleMap, Session: sessionCfg,
		},
		Blob: testBlobConfig,
	}

	ts.Config.Handler = server.New(cfg, testPool, testBlobStore, server.SSO{OIDC: provider}).Handler()
	ts.Start()
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}

	return base, client
}

// postAs sends a write as a browser would: cookies from the jar, and the CSRF
// token in a header when there is one to send.
func postAs(t *testing.T, client *http.Client, url string, body any, csrf string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set(auth.CSRFHeader, csrf)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// logIn walks the whole flow the way a browser does: /auth/login, the
// provider's redirect, and back to /auth/callback.
func logIn(t *testing.T, base string, client *http.Client) *http.Response {
	t.Helper()
	resp, err := client.Get(base + "/auth/login")
	require.NoError(t, err)
	defer resp.Body.Close()
	// The client follows the redirect to the provider, which redirects back to
	// the callback, which redirects to "/" — so what lands here is the end of
	// the chain.
	return resp
}

func cookieNamed(t *testing.T, client *http.Client, base, name string) *http.Cookie {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// loginAndCaptureCookies walks the login the way logIn does, but keeps every
// Set-Cookie the server sent along the way.
//
// A cookie jar keeps a cookie's name and value and throws its attributes away,
// so HttpOnly and SameSite — which are most of what makes a session cookie safe
// — can only be checked on the wire.
func loginAndCaptureCookies(t *testing.T, base string, client *http.Client) []*http.Cookie {
	t.Helper()

	var captured []*http.Cookie
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.Response != nil {
			captured = append(captured, req.Response.Cookies()...)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	defer func() { client.CheckRedirect = previous }()

	resp, err := client.Get(base + "/auth/login")
	require.NoError(t, err)
	captured = append(captured, resp.Cookies()...)
	resp.Body.Close()
	return captured
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	// Last wins, the way a browser applies them.
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == name {
			found = c
		}
	}
	return found
}

// statusOf is for the cases where /me is expected to refuse: with credentials
// configured, a caller presenting none — or a cookie that no longer resolves —
// is a 401, not an authenticated-false.
func statusOf(t *testing.T, base string, client *http.Client, path string) int {
	t.Helper()
	resp, err := client.Get(base + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

func meOf(t *testing.T, base string, client *http.Client) meBody {
	t.Helper()
	resp, err := client.Get(base + "/api/v1/me")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var me meBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&me))
	return me
}

// dropPrincipal cleans up whoever the login created.
func dropPrincipal(t *testing.T, externalIDLike string) {
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(),
			`DELETE FROM principals WHERE external_id LIKE $1`, externalIDLike)
		assert.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Tests: the happy path
// ---------------------------------------------------------------------------

// The slot section 9 marked out, filled in: a person at an identity provider
// becomes a principal here, with the roles their groups imply.
func TestLoginCreatesAPrincipalAndASession(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})

	sent := loginAndCaptureCookies(t, base, client)

	session := findCookie(sent, auth.SessionCookie)
	require.NotNil(t, session, "a session cookie should have been set")
	assert.True(t, session.HttpOnly, "script must not be able to read the session")
	assert.Equal(t, http.SameSiteLaxMode, session.SameSite, "keeps the cookie off cross-site posts")

	// The other half of the double submit is deliberately readable: the page
	// has to echo it back in a header.
	csrf := findCookie(sent, auth.CSRFCookie)
	require.NotNil(t, csrf)
	assert.False(t, csrf.HttpOnly)

	me := meOf(t, base, client)
	assert.True(t, me.Authenticated)
	assert.True(t, me.ViaSession)
	assert.Equal(t, "user:alice@example.com", me.Subject, "the readable name is what evidence is filed under")
	assert.Equal(t, model.PrincipalKindUser, me.Kind)
	assert.Equal(t, []string{"contributor"}, me.Roles)
}

// The IdP's subject is what identifies a person, not their address. Someone who
// changes their email stays one principal, with their history intact.
func TestARenameFollowsThePersonRatherThanSplittingThem(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	first := meOf(t, base, client)

	// Same person at the provider, new address.
	idp.email = "alice.example@example.com"
	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{Jar: jar2}
	logIn(t, base, client2).Body.Close()
	second := meOf(t, base, client2)

	assert.Equal(t, "user:alice.example@example.com", second.Subject, "the subject follows the address")
	assert.NotEqual(t, first.Subject, second.Subject)

	var count int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM principals WHERE external_id LIKE $1`, "%idp-subject-001").Scan(&count))
	assert.Equal(t, 1, count, "one human should be one principal")
}

// ---------------------------------------------------------------------------
// Tests: roles from groups
// ---------------------------------------------------------------------------

func TestGroupsBecomeRolesAndUnmappedGroupsGrantNothing(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all", "eng-leads", "everyone-at-the-company"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{
		"eng-all":   "contributor",
		"eng-leads": "admin",
	})
	logIn(t, base, client).Body.Close()

	me := meOf(t, base, client)
	assert.ElementsMatch(t, []string{"contributor", "admin"}, me.Roles)
	assert.Contains(t, me.Permissions, "principal:admin")
}

// Pointing this store at a company IdP must not hand every employee an account
// that can write.
func TestAPersonInNoMappedGroupGetsNothing(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"everyone-at-the-company"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-leads": "admin"})
	logIn(t, base, client).Body.Close()

	me := meOf(t, base, client)
	assert.True(t, me.Authenticated, "they are who they say they are")
	assert.Empty(t, me.Roles, "which is not the same as being allowed anything")

	resp, err := client.Get(base + "/api/v1/evidence")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// Removing somebody from a group at the provider has to take the role away
// here, or the mapping is decoration.
func TestLosingAGroupLosesTheRoleAtTheNextLogin(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all", "eng-leads"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor", "eng-leads": "admin"})
	logIn(t, base, client).Body.Close()
	require.ElementsMatch(t, []string{"admin", "contributor"}, meOf(t, base, client).Roles)

	idp.groups = []string{"eng-all"}
	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{Jar: jar2}
	logIn(t, base, client2).Body.Close()

	assert.Equal(t, []string{"contributor"}, meOf(t, base, client2).Roles)
}

// A role an administrator granted by hand must survive a login, or the Access
// tab would be quietly undone every time its subject signed in — and an IdP
// with no useful groups would leave nobody able to grant anything.
func TestALocallyGrantedRoleSurvivesALogin(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	me := meOf(t, base, client)
	require.Equal(t, []string{"contributor"}, me.Roles)

	// An administrator adds ci by hand, the way the Access tab does.
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO role_bindings (principal_id, role, scope, source)
		SELECT id, 'ci', '*', 'local' FROM principals WHERE subject = $1
	`, me.Subject)
	require.NoError(t, err)

	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{Jar: jar2}
	logIn(t, base, client2).Body.Close()

	assert.ElementsMatch(t, []string{"contributor", "ci"}, meOf(t, base, client2).Roles,
		"the login should reconcile its own grants and leave the administrator's alone")
}

// ---------------------------------------------------------------------------
// Tests: sessions are revocable, which is why they are rows
// ---------------------------------------------------------------------------

func TestLogoutEndsTheSessionImmediately(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	me := meOf(t, base, client)
	require.True(t, me.Authenticated)

	req, err := http.NewRequest(http.MethodPost, base+"/auth/logout", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	// The cookie is gone from the browser and the row is gone from the table,
	// so the next request is simply an anonymous one — which, with credentials
	// configured, is a 401 rather than an authenticated-false.
	assert.Equal(t, http.StatusUnauthorized, statusOf(t, base, client, "/api/v1/me"))

	var remaining int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM sessions WHERE principal_id = (SELECT id FROM principals WHERE subject = $1)`,
		me.Subject).Scan(&remaining))
	assert.Zero(t, remaining, "logging out should end the session, not just forget it")
}

// The reason sessions are a table and not a signed cookie: disabling somebody
// stops the browser they left open, not just their next login.
func TestDisablingAPrincipalKillsTheirOpenSession(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	me := meOf(t, base, client)
	require.True(t, me.Authenticated)

	_, err := testPool.Exec(context.Background(),
		`UPDATE principals SET disabled_at = now() WHERE subject = $1`, me.Subject)
	require.NoError(t, err)

	resp, err := client.Get(base + "/api/v1/evidence")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a signed cookie would have kept working until it expired")
	resp.Body.Close()
}

func TestAnExpiredSessionStopsWorking(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	me := meOf(t, base, client)
	require.True(t, me.Authenticated)

	// Expiry is asserted in the query, so a session is dead when it is due
	// rather than when something gets round to sweeping it.
	_, err := testPool.Exec(context.Background(), `
		UPDATE sessions SET expires_at = now() - INTERVAL '1 minute'
		WHERE principal_id = (SELECT id FROM principals WHERE subject = $1)
	`, me.Subject)
	require.NoError(t, err)

	// The browser still holds the cookie; the server is what stops honouring it.
	assert.Equal(t, http.StatusUnauthorized, statusOf(t, base, client, "/api/v1/me"))

	swept, err := store.NewSessionStore(testPool).DeleteExpired(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, swept, int64(1))
}

// ---------------------------------------------------------------------------
// Tests: the checks that make the flow safe
// ---------------------------------------------------------------------------

// A callback that is not the continuation of a login started here is what a
// forged one looks like.
func TestCallbackRefusesWhatItDidNotStart(t *testing.T) {
	idp := newMockIdP(t)
	base, client := ssoServer(t, idp, nil)

	resp, err := client.Get(base + "/auth/callback?code=made-up&state=made-up")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// The state a browser comes back with has to be the one it left with.
func TestCallbackRefusesAMismatchedState(t *testing.T) {
	idp := newMockIdP(t)
	base, client := ssoServer(t, idp, nil)

	// Start a login so the state cookie exists, then come back with the wrong
	// value in the query.
	noRedirect := &http.Client{Jar: client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noRedirect.Get(base + "/auth/login")
	require.NoError(t, err)
	resp.Body.Close()

	resp, err = noRedirect.Get(base + "/auth/callback?code=anything&state=not-the-one-we-sent")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// A token signed by a key the provider never published is the one failure here
// worth alarming about.
func TestATokenSignedByTheWrongKeyIsRefused(t *testing.T) {
	idp := newMockIdP(t)
	idp.signWithWrongKey = true
	base, client := ssoServer(t, idp, nil)

	resp := logIn(t, base, client)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
	assert.Nil(t, cookieNamed(t, client, base, auth.SessionCookie), "no session should have been created")
}

func TestATokenForAnotherAudienceIsRefused(t *testing.T) {
	idp := newMockIdP(t)
	idp.audienceOverride = "some-other-application"
	base, client := ssoServer(t, idp, nil)

	resp := logIn(t, base, client)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// Permission to call an API on somebody's behalf is not the same as knowing who
// they are, and only the second is what this flow is for.
func TestAResponseWithNoIDTokenIsRefused(t *testing.T) {
	idp := newMockIdP(t)
	idp.omitIDToken = true
	base, client := ssoServer(t, idp, nil)

	resp := logIn(t, base, client)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// Somebody the store has revoked can still authenticate with the provider.
// This is where that stops.
func TestADisabledPrincipalCannotLogBackIn(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	me := meOf(t, base, client)

	_, err := testPool.Exec(context.Background(),
		`UPDATE principals SET disabled_at = now() WHERE subject = $1`, me.Subject)
	require.NoError(t, err)

	jar2, _ := cookiejar.New(nil)
	resp := logIn(t, base, &http.Client{Jar: jar2})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// An API key already answering to this person's name is not evidence they are
// the same party, and a login is the wrong place to decide that they are.
func TestALoginBlockedByAnExistingSubject(t *testing.T) {
	idp := newMockIdP(t)
	base, client := ssoServer(t, idp, nil)
	dropPrincipal(t, "%idp-subject-001")

	_, err := store.NewPrincipalStore(testPool).Insert(context.Background(), model.PrincipalCreate{
		Subject:     "user:alice@example.com",
		Kind:        model.PrincipalKindAPIKey,
		DisplayName: "an API key that got there first",
		KeyHash:     auth.HashKey("some-key-for-the-collision-test"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(),
			`DELETE FROM principals WHERE subject = $1`, "user:alice@example.com")
		assert.NoError(t, err)
	})

	resp := logIn(t, base, client)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: CSRF
// ---------------------------------------------------------------------------

// A cookie is sent by the browser whether or not the page meant to send it,
// which is why a session needs a token where a bearer header did not.
func TestSessionWritesNeedTheCSRFToken(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	me := meOf(t, base, client)

	record := makeEvidence("org/sso_csrf", "main", "r1", "//pkg:test", me.Subject, model.ResultPass)

	// Without the header: refused, even though the cookie is perfectly valid.
	resp := postAs(t, client, base+"/api/v1/evidence", record, "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// With it: filed.
	csrf := cookieNamed(t, client, base, auth.CSRFCookie)
	require.NotNil(t, csrf, "the readable half of the double submit")
	resp = postAs(t, client, base+"/api/v1/evidence", record, csrf.Value)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

func TestAWrongCSRFTokenIsRefused(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()
	me := meOf(t, base, client)

	resp := postAs(t, client, base+"/api/v1/evidence",
		makeEvidence("org/sso_csrf2", "main", "r1", "//pkg:test", me.Subject, model.ResultPass),
		"a-token-from-somewhere-else")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// Reads are what a cross-site page can cause anyway, so requiring a token on
// them would buy nothing and break every ordinary link.
func TestSessionReadsNeedNoToken(t *testing.T) {
	idp := newMockIdP(t)
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()

	resp, err := client.Get(base + "/api/v1/evidence")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// CI has no cookies and no need of any of this. Requiring a token from a bearer
// caller would break every pipeline the day SSO was switched on.
func TestBearerWritesAreUnaffectedByCSRF(t *testing.T) {
	idp := newMockIdP(t)
	base, _ := ssoServer(t, idp, nil)
	subject := rbacSubject(t, "pipeline")
	key := issueKey(t, subject, "ci")

	resp := doRequest(t, http.MethodPost, base+"/api/v1/evidence", "Bearer "+key,
		makeEvidence("org/sso_bearer", "main", "r1", "//pkg:test", "https://ci/build/1", model.ResultPass))
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: what a client is told
// ---------------------------------------------------------------------------

// Without this the web UI has nothing to offer on a 401 but a prompt for an API
// key, which is what it did before there was a login flow.
func TestMeAdvertisesWhetherThereIsSomewhereToLogIn(t *testing.T) {
	idp := newMockIdP(t)
	base, client := ssoServer(t, idp, nil)

	resp, err := client.Get(base + "/api/v1/me")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"an anonymous caller is still refused; the advertisement is for callers who get through")

	// A store with no provider configured says so, and its UI keeps prompting.
	plain := setupRBACServer(t, nil)
	plainResp := doRequest(t, http.MethodGet, plain.URL+"/api/v1/me", "Bearer "+issueKey(t, rbacSubject(t, "plain"), "viewer"), nil)
	defer plainResp.Body.Close()
	var me meBody
	require.NoError(t, json.NewDecoder(plainResp.Body).Decode(&me))
	assert.False(t, me.SSOEnabled)
	assert.False(t, me.ViaSession)
}
