package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/server"
	"github.com/nesono/evidence-store/internal/store"
)

// Phase 2 of #146: /scim/v2/Users.
//
// The request shapes here are the ones Microsoft documents Entra sending,
// including the two spellings of a deactivation patch — the whole feature turns
// on recognising both, since missing one means silently not deprovisioning
// anybody.

// scimClient is a provisioner: a server, and an admin key to call it with.
type scimClient struct {
	base string
	key  string
	t    *testing.T
}

// scimRoleMap is what the directory's group names mean here. The same map the
// login path reads, which is the point: a group means one thing however this
// store hears about it.
var scimRoleMap = map[string]string{
	"eng-all":   "contributor",
	"eng-leads": "admin",
}

func newSCIMClient(t *testing.T) *scimClient {
	t.Helper()
	key := issueKey(t, rbacSubject(t, "provisioner"), "admin")
	ts := setupSCIMServer(t)
	return &scimClient{base: ts.URL, key: key, t: t}
}

// setupSCIMServer is setupRBACServer with a group-to-role map, since a
// provisioner that maps no groups can only ever grant nothing.
func setupSCIMServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL:     testDatabaseURL,
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		Auth:            config.Auth{DB: true, RoleMap: scimRoleMap},
		Blob:            testBlobConfig,
	}
	ts := httptest.NewServer(server.New(cfg, testPool, testBlobStore, server.SSO{}).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func (c *scimClient) do(method, path, body string) (*http.Response, map[string]any) {
	c.t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	require.NoError(c.t, err)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/scim+json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()

	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp, decoded
}

// createUser provisions somebody the way Entra does, and cleans them up.
func (c *scimClient) createUser(userName, email, display string) map[string]any {
	c.t.Helper()
	resp, body := c.do(http.MethodPost, "/scim/v2/Users", fmt.Sprintf(`{
	  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
	  "userName": %q,
	  "externalId": "entra-%s",
	  "name": {"givenName": %q, "familyName": "Provisioned"},
	  "emails": [{"value": %q, "primary": true, "type": "work"}],
	  "active": true
	}`, userName, userName, display, email))
	require.Equal(c.t, http.StatusCreated, resp.StatusCode, "provisioning should succeed: %v", body)
	c.t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(),
			`DELETE FROM principals WHERE user_name = $1`, userName)
		assert.NoError(c.t, err)
	})
	return body
}

func scimName(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%s@example.com", strings.ToLower(t.Name()), suffix)
}

// ---------------------------------------------------------------------------
// Creating
// ---------------------------------------------------------------------------

func TestSCIMProvisionsSomebodyWhoHasNeverLoggedIn(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "new")

	body := c.createUser(name, name, "Newly")

	assert.Equal(t, name, body["userName"])
	assert.Equal(t, true, body["active"])
	assert.NotEmpty(t, body["id"], "the store has to name the resource for later requests")
	assert.Contains(t, body["schemas"], "urn:ietf:params:scim:schemas:core:2.0:User")

	meta, _ := body["meta"].(map[string]any)
	require.NotNil(t, meta)
	assert.Equal(t, "User", meta["resourceType"])
	assert.Equal(t, "/scim/v2/Users/"+body["id"].(string), meta["location"])

	// The row is a principal like any other, ready for the login that claims it.
	var subject, kind string
	var externalID *string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT subject, kind, external_id FROM principals WHERE user_name = $1`,
		name).Scan(&subject, &kind, &externalID))
	assert.Equal(t, "user:"+name, subject, "the subject is what their evidence will be filed under")
	assert.Equal(t, model.PrincipalKindUser, kind)
	assert.Nil(t, externalID, "a provisioned row stays unclaimed until its first login")
}

// The subject has to be the address a later ID token will carry, or the login
// will not recognise the row provisioned for it and will make a second one.
func TestTheSubjectComesFromThePrimaryEmailNotTheUserName(t *testing.T) {
	c := newSCIMClient(t)
	upn := scimName(t, "upn")
	email := scimName(t, "mail")

	c.createUser(upn, email, "Two Names")

	var subject string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT subject FROM principals WHERE user_name = $1`, upn).Scan(&subject))
	assert.Equal(t, "user:"+email, subject)
}

func TestProvisioningTheSamePersonTwiceIsAConflict(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "dup")
	c.createUser(name, name, "Duplicate")

	resp, body := c.do(http.MethodPost, "/scim/v2/Users",
		fmt.Sprintf(`{"userName": %q, "emails": [{"value": %q, "primary": true}]}`, name, name))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "uniqueness", body["scimType"],
		"the code a client branches on to know it should have looked first")
	assert.Contains(t, body["schemas"], "urn:ietf:params:scim:api:messages:2.0:Error")
}

// A deployment that had single sign-on before it had provisioning is full of
// people who logged in and made their own principal. Refusing those would leave
// the directory permanently unable to deactivate them.
func TestProvisioningAdoptsSomebodyWhoAlreadyLoggedIn(t *testing.T) {
	c := newSCIMClient(t)
	email := scimName(t, "already")

	var existing uuid.UUID
	require.NoError(t, testPool.QueryRow(context.Background(), `
		INSERT INTO principals (subject, kind, display_name, external_id)
		VALUES ($1, 'user', 'Logged In Already', $2)
		RETURNING id
	`, "user:"+email, "https://idp.example.com|sub-"+uuid.NewString()).Scan(&existing))
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE id = $1`, existing)
		assert.NoError(t, err)
	})

	c.createUser(email, email, "Logged In Already")

	var count int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM principals WHERE subject = $1`, "user:"+email).Scan(&count))
	assert.Equal(t, 1, count, "provisioning should adopt the person, not duplicate them")

	var id uuid.UUID
	var scimID, externalID *string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id, scim_id, external_id FROM principals WHERE subject = $1`,
		"user:"+email).Scan(&id, &scimID, &externalID))
	assert.Equal(t, existing, id)
	require.NotNil(t, scimID, "the directory's identity should now be attached")
	require.NotNil(t, externalID, "and the login they already have must survive it")
}

// An API key named after a person is not that person.
func TestProvisioningCannotTakeOverAnAPIKey(t *testing.T) {
	c := newSCIMClient(t)
	email := scimName(t, "robot")
	// Named exactly as a provisioned person would be, which is what makes this
	// a collision rather than two unrelated rows.
	issueKey(t, "user:"+email, "contributor")

	resp, body := c.do(http.MethodPost, "/scim/v2/Users",
		fmt.Sprintf(`{"userName": %q, "emails": [{"value": %q, "primary": true}]}`, email, email))
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "%v", body)

	var kind string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT kind FROM principals WHERE subject = $1`, "user:"+email).Scan(&kind))
	assert.Equal(t, model.PrincipalKindAPIKey, kind, "the key must not have become a person")
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// The query a provisioner makes before anything else: have I already
// provisioned this person?
func TestFilteringByUserNameIsWhatAProvisionerAsksFirst(t *testing.T) {
	c := newSCIMClient(t)
	wanted := scimName(t, "wanted")
	c.createUser(wanted, wanted, "Wanted")
	c.createUser(scimName(t, "other"), scimName(t, "other"), "Other")

	resp, body := c.do(http.MethodGet,
		`/scim/v2/Users?filter=`+urlQuery(`userName eq "`+wanted+`"`), "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Contains(t, body["schemas"], "urn:ietf:params:scim:api:messages:2.0:ListResponse")
	assert.Equal(t, float64(1), body["totalResults"])
	resources, _ := body["Resources"].([]any)
	require.Len(t, resources, 1)
	assert.Equal(t, wanted, resources[0].(map[string]any)["userName"])
}

func TestAskingAboutSomebodyWhoIsNotHereIsAnEmptyList(t *testing.T) {
	c := newSCIMClient(t)
	resp, body := c.do(http.MethodGet,
		`/scim/v2/Users?filter=`+urlQuery(`userName eq "nobody@example.com"`), "")

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"an empty answer is an answer; a 404 here reads as the endpoint being wrong")
	assert.Equal(t, float64(0), body["totalResults"])
}

// An unsupported filter is refused rather than answered with the whole
// directory, which would look to a client like every user matching every query.
func TestAnUnsupportedFilterIsRefused(t *testing.T) {
	c := newSCIMClient(t)
	resp, body := c.do(http.MethodGet,
		`/scim/v2/Users?filter=`+urlQuery(`displayName co "Ann"`), "")

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalidFilter", body["scimType"])
}

func TestListingPagesTheWaySCIMCounts(t *testing.T) {
	c := newSCIMClient(t)
	for i := range 3 {
		name := scimName(t, fmt.Sprintf("page%d", i))
		c.createUser(name, name, "Paged")
	}

	// startIndex is 1-based, so this asks for the second of three.
	resp, body := c.do(http.MethodGet, "/scim/v2/Users?startIndex=2&count=1", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, float64(2), body["startIndex"])
	assert.Equal(t, float64(1), body["itemsPerPage"])
	assert.GreaterOrEqual(t, body["totalResults"], float64(3),
		"the total is of everything that matched, not of this page")
}

func TestReadingAUserBackByTheIdWeGaveOut(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "byid")
	created := c.createUser(name, name, "By Id")

	resp, body := c.do(http.MethodGet, "/scim/v2/Users/"+created["id"].(string), "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, name, body["userName"])

	resp, body = c.do(http.MethodGet, "/scim/v2/Users/"+uuid.NewString(), "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, body["schemas"], "urn:ietf:params:scim:api:messages:2.0:Error")
}

// ---------------------------------------------------------------------------
// Deprovisioning — the point of the exercise
// ---------------------------------------------------------------------------

// Entra's first spelling: the value is the boolean.
func TestDeactivatingWithAPathPatchDisablesTheAccount(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "leaver")
	created := c.createUser(name, name, "Leaver")

	resp, body := c.do(http.MethodPatch, "/scim/v2/Users/"+created["id"].(string), `{
	  "schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
	  "Operations": [{"op": "replace", "path": "active", "value": false}]
	}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, false, body["active"])

	assertDisabled(t, name, true)
}

// Entra's other spelling, which is the one that would silently deprovision
// nobody if it were not recognised.
func TestDeactivatingWithABodyPatchDisablesTheAccount(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "leaver2")
	created := c.createUser(name, name, "Leaver Two")

	resp, body := c.do(http.MethodPatch, "/scim/v2/Users/"+created["id"].(string), `{
	  "schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
	  "Operations": [{"op": "Replace", "value": {"active": false}}]
	}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, false, body["active"])

	assertDisabled(t, name, true)
}

// Some clients send the flag as a string.
func TestDeactivatingWithAStringFlagIsUnderstood(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "leaver3")
	created := c.createUser(name, name, "Leaver Three")

	resp, _ := c.do(http.MethodPatch, "/scim/v2/Users/"+created["id"].(string), `{
	  "Operations": [{"op": "replace", "path": "active", "value": "False"}]
	}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assertDisabled(t, name, true)
}

// The failure provisioning exists to close: an account is not shut while the
// browser somebody walked away from still holds a live session for it.
func TestDeactivatingEndsTheirOpenSessions(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "session")
	created := c.createUser(name, name, "Has A Session")

	var principalID uuid.UUID
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM principals WHERE user_name = $1`, name).Scan(&principalID))

	sessions := store.NewSessionStore(testPool)
	_, err := sessions.Create(context.Background(), principalID,
		auth.HashKey("a-live-session"), timeHourFromNow(), "their laptop", "")
	require.NoError(t, err)

	resp, _ := c.do(http.MethodPatch, "/scim/v2/Users/"+created["id"].(string),
		`{"Operations": [{"op": "replace", "path": "active", "value": false}]}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var remaining int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM sessions WHERE principal_id = $1`, principalID).Scan(&remaining))
	assert.Zero(t, remaining, "deactivating has to close the browser they left open")
}

// DELETE is a disable. Evidence names its source, so the row has to survive.
func TestDeleteDisablesRatherThanRemoves(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "deleted")
	created := c.createUser(name, name, "Deleted")

	resp, _ := c.do(http.MethodDelete, "/scim/v2/Users/"+created["id"].(string), "")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	assertDisabled(t, name, true)

	var count int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM principals WHERE user_name = $1`, name).Scan(&count))
	assert.Equal(t, 1, count, "the principal has to survive so its evidence still names somebody")
}

// Somebody coming back from leave, or provisioned into the wrong group and
// fixed. Reactivation has to work or the protocol is one-way.
func TestReactivatingLetsThemBackIn(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "returner")
	created := c.createUser(name, name, "Returner")

	c.do(http.MethodDelete, "/scim/v2/Users/"+created["id"].(string), "")
	assertDisabled(t, name, true)

	resp, body := c.do(http.MethodPatch, "/scim/v2/Users/"+created["id"].(string),
		`{"Operations": [{"op": "replace", "path": "active", "value": true}]}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, body["active"])
	assertDisabled(t, name, false)
}

// A directory that removed the last administrator would leave the deployment
// with no way in but psql.
//
// Checked against the store rather than over HTTP, because the caller today has
// to hold principal:admin to reach these routes at all — so there is always at
// least one other administrator and the guard correctly never fires. It becomes
// reachable in phase 4, when provisioning gets its own role and the token a
// directory holds stops being an administrator; the end-to-end test belongs
// with that change.
func TestTheLastAdministratorCannotBeDeprovisioned(t *testing.T) {
	ctx := context.Background()
	scim := store.NewSCIMStore(testPool)

	name := scimName(t, "lastadmin")
	created, err := scim.CreateUser(ctx, store.SCIMUserWrite{
		UserName: name, Subject: "user:" + name, DisplayName: "Last Admin", Active: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := testPool.Exec(ctx, `DELETE FROM principals WHERE id = $1`, created.ID)
		assert.NoError(t, err)
	})

	// Nobody else may hold admin, or this person is not the last one.
	var parked []uuid.UUID
	rows, err := testPool.Query(ctx,
		`SELECT principal_id FROM role_bindings WHERE role = 'admin' AND principal_id <> $1`, created.ID)
	require.NoError(t, err)
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		parked = append(parked, id)
	}
	rows.Close()
	_, err = testPool.Exec(ctx,
		`DELETE FROM role_bindings WHERE role = 'admin' AND principal_id <> $1`, created.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, id := range parked {
			_, err := testPool.Exec(ctx, `
				INSERT INTO role_bindings (principal_id, role, scope, source)
				VALUES ($1, 'admin', '*', 'local') ON CONFLICT DO NOTHING
			`, id)
			assert.NoError(t, err)
		}
	})

	_, err = testPool.Exec(ctx, `
		INSERT INTO role_bindings (principal_id, role, scope, source) VALUES ($1, 'admin', '*', 'idp')
	`, created.ID)
	require.NoError(t, err)

	others, err := scim.CountOtherEnabledAdmins(ctx, created.SCIMID)
	require.NoError(t, err)
	assert.Zero(t, others, "this person is now the only way into the store")

	admin, err := scim.HasAdminRole(ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, admin, "which is what makes deactivating them the refusable case")
}

// ---------------------------------------------------------------------------
// Replacing
// ---------------------------------------------------------------------------

func TestPutStatesTheWholeResource(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "renamed")
	created := c.createUser(name, name, "Before")

	resp, body := c.do(http.MethodPut, "/scim/v2/Users/"+created["id"].(string), fmt.Sprintf(`{
	  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
	  "userName": %q,
	  "displayName": "After",
	  "active": true
	}`, name))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "After", body["displayName"])
}

// The subject is what evidence already filed names its author by. A directory
// tidying somebody's display name is not a reason to rewrite attribution on
// records they wrote last year.
func TestPutLeavesTheSubjectAlone(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "stable")
	created := c.createUser(name, name, "Stable")

	_, _ = c.do(http.MethodPut, "/scim/v2/Users/"+created["id"].(string), fmt.Sprintf(`{
	  "userName": %q, "displayName": "Renamed", "active": true
	}`, name))

	var subject string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT subject FROM principals WHERE user_name = $1`, name).Scan(&subject))
	assert.Equal(t, "user:"+name, subject)
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

func TestProvisioningNeedsAuthority(t *testing.T) {
	ts := setupRBACServer(t, nil)
	contributor := issueKey(t, rbacSubject(t, "contributor"), "contributor")

	for _, tc := range []struct {
		name, key string
		want      int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"a contributor", contributor, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/scim/v2/Users", nil)
			require.NoError(t, err)
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer "+tc.key)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tc.want, resp.StatusCode,
				"a provisioning endpoint must not be open to whoever finds it")
		})
	}
}

// urlQuery escapes a filter for the query string. Written out rather than
// building a url.Values, so the test reads as the request a client sends.
func urlQuery(raw string) string {
	return url.QueryEscape(raw)
}

func timeHourFromNow() time.Time {
	return time.Now().Add(time.Hour)
}

func assertDisabled(t *testing.T, userName string, want bool) {
	t.Helper()
	var disabled bool
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT disabled_at IS NOT NULL FROM principals WHERE user_name = $1`,
		userName).Scan(&disabled))
	assert.Equal(t, want, disabled)
}
