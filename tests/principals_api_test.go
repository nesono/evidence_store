package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type issuedPrincipal struct {
	Principal model.Principal `json:"principal"`
	APIKey    string          `json:"api_key"`
}

// adminServer returns a server with database authentication on and an
// administrator to drive the API with. A second admin is seeded alongside so
// that the last-administrator guard is not what every other test trips over.
func adminServer(t *testing.T) (url, adminKey string) {
	t.Helper()
	ts := setupRBACServer(t, nil)
	issueKey(t, rbacSubject(t, "standby-admin"), "admin")
	return ts.URL, issueKey(t, rbacSubject(t, "acting-admin"), "admin")
}

// rawBody is for asserting on what a response does *not* contain, which
// decoding into a struct would quietly hide.
func rawBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// createPrincipal posts a new identity and cleans it up afterwards.
func createPrincipal(t *testing.T, url, adminKey, subject string, roles ...string) issuedPrincipal {
	t.Helper()
	if roles == nil {
		roles = []string{}
	}
	resp := doRequest(t, http.MethodPost, url+"/api/v1/principals", "Bearer "+adminKey, map[string]any{
		"subject":      subject,
		"display_name": "created by " + t.Name(),
		"roles":        roles,
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	issued := decodeJSON[issuedPrincipal](t, resp)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE id = $1`, issued.Principal.ID)
		assert.NoError(t, err)
	})
	return issued
}

// ---------------------------------------------------------------------------
// Tests: issuing credentials
// ---------------------------------------------------------------------------

// The whole point of the phase: an administrator can hand somebody a working
// key without touching psql, and the key works immediately.
func TestIssuedKeyWorksAndCarriesOnlyItsRoles(t *testing.T) {
	url, adminKey := adminServer(t)
	subject := rbacSubject(t, "new-hire")

	issued := createPrincipal(t, url, adminKey, subject, "contributor")
	require.NotEmpty(t, issued.APIKey)
	assert.Equal(t, subject, issued.Principal.Subject)
	assert.Equal(t, model.PrincipalKindAPIKey, issued.Principal.Kind)
	assert.Equal(t, []string{"contributor"}, issued.Principal.Roles)
	assert.False(t, issued.Principal.Disabled())

	// It can write evidence...
	resp := doRequest(t, http.MethodPost, url+"/api/v1/evidence", "Bearer "+issued.APIKey,
		makeEvidence("org/principals_api", "main", "r1", "//pkg:test", subject, model.ResultPass))
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ...and nothing a contributor may not.
	resp = doRequest(t, http.MethodGet, url+"/api/v1/principals", "Bearer "+issued.APIKey, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// The key is shown once because only its digest is stored. Listing must never
// leak it, or storing a digest was pointless.
func TestListingNeverCarriesTheKey(t *testing.T) {
	url, adminKey := adminServer(t)
	issued := createPrincipal(t, url, adminKey, rbacSubject(t, "listed"), "viewer")

	resp := doRequest(t, http.MethodGet, url+"/api/v1/principals", "Bearer "+adminKey, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := rawBody(t, resp)
	assert.NotContains(t, body, issued.APIKey, "the plaintext key must never appear again")
	assert.NotContains(t, body, "key_hash", "nor the digest, which is nobody's business")
	assert.Contains(t, body, issued.Principal.Subject)
}

func TestCreateRejectsADuplicateSubject(t *testing.T) {
	url, adminKey := adminServer(t)
	subject := rbacSubject(t, "twice")
	createPrincipal(t, url, adminKey, subject, "viewer")

	resp := doRequest(t, http.MethodPost, url+"/api/v1/principals", "Bearer "+adminKey, map[string]any{
		"subject": subject,
		"roles":   []string{"viewer"},
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
}

func TestCreateRejectsWhatCannotBeGranted(t *testing.T) {
	url, adminKey := adminServer(t)

	for name, body := range map[string]map[string]any{
		"no subject":    {"subject": "", "roles": []string{"viewer"}},
		"unknown role":  {"subject": rbacSubject(t, "badrole"), "roles": []string{"superuser"}},
		"role twice":    {"subject": rbacSubject(t, "duprole"), "roles": []string{"viewer", "viewer"}},
		"a human being": {"subject": rbacSubject(t, "human"), "kind": "user", "roles": []string{"viewer"}},
	} {
		t.Run(name, func(t *testing.T) {
			resp := doRequest(t, http.MethodPost, url+"/api/v1/principals", "Bearer "+adminKey, body)
			defer resp.Body.Close()
			// "a human being" is accepted as api_key regardless of what the
			// client asked for, because the kind is not the client's to choose
			// yet; the rest are refused outright.
			if name == "a human being" {
				assert.Equal(t, http.StatusCreated, resp.StatusCode)
				var issued issuedPrincipal
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&issued))
				assert.Equal(t, model.PrincipalKindAPIKey, issued.Principal.Kind)
				_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE id = $1`, issued.Principal.ID)
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: revoking and restoring
// ---------------------------------------------------------------------------

func TestDisableAndEnableTakeEffectImmediately(t *testing.T) {
	url, adminKey := adminServer(t)
	issued := createPrincipal(t, url, adminKey, rbacSubject(t, "revocable"), "viewer")
	id := issued.Principal.ID.String()

	resp := doRequest(t, http.MethodGet, url+"/api/v1/evidence", "Bearer "+issued.APIKey, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = doRequest(t, http.MethodPost, url+"/api/v1/principals/"+id+"/disable", "Bearer "+adminKey, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	disabled := decodeJSON[model.Principal](t, resp)
	assert.True(t, disabled.Disabled())

	resp = doRequest(t, http.MethodGet, url+"/api/v1/evidence", "Bearer "+issued.APIKey, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "revocation bites on the next request")
	resp.Body.Close()

	// Restoring returns the principal with the roles it already had: undoing a
	// revocation should not mean reissuing a key and re-granting everything.
	resp = doRequest(t, http.MethodPost, url+"/api/v1/principals/"+id+"/enable", "Bearer "+adminKey, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	enabled := decodeJSON[model.Principal](t, resp)
	assert.False(t, enabled.Disabled())
	assert.Equal(t, []string{"viewer"}, enabled.Roles)

	resp = doRequest(t, http.MethodGet, url+"/api/v1/evidence", "Bearer "+issued.APIKey, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the original key still works")
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: roles
// ---------------------------------------------------------------------------

func TestReplaceRolesIsTheSetTheAdminMeant(t *testing.T) {
	url, adminKey := adminServer(t)
	subject := rbacSubject(t, "promoted")
	issued := createPrincipal(t, url, adminKey, subject, "viewer")
	rolesURL := url + "/api/v1/principals/" + issued.Principal.ID.String() + "/roles"

	// Promoted: can now write.
	resp := doRequest(t, http.MethodPut, rolesURL, "Bearer "+adminKey, map[string]any{"roles": []string{"contributor"}})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	updated := decodeJSON[model.Principal](t, resp)
	assert.Equal(t, []string{"contributor"}, updated.Roles, "replaced, not added to")

	resp = doRequest(t, http.MethodPost, url+"/api/v1/evidence", "Bearer "+issued.APIKey,
		makeEvidence("org/principals_roles", "main", "r1", "//pkg:test", subject, model.ResultPass))
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Demoted to nothing: authenticated, and allowed nothing.
	resp = doRequest(t, http.MethodPut, rolesURL, "Bearer "+adminKey, map[string]any{"roles": []string{}})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = doRequest(t, http.MethodGet, url+"/api/v1/evidence", "Bearer "+issued.APIKey, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

func TestReplaceRolesRejectsUnknownRoles(t *testing.T) {
	url, adminKey := adminServer(t)
	issued := createPrincipal(t, url, adminKey, rbacSubject(t, "badpromo"), "viewer")

	resp := doRequest(t, http.MethodPut,
		url+"/api/v1/principals/"+issued.Principal.ID.String()+"/roles", "Bearer "+adminKey,
		map[string]any{"roles": []string{"viewer", "superuser"}})
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: the last administrator
// ---------------------------------------------------------------------------

// Disabling the last enabled admin, or taking admin off them, leaves a
// deployment with no way in but psql. One click in the web UI should not be
// able to do that.
func TestTheLastAdministratorCannotBeLockedOut(t *testing.T) {
	ts := setupRBACServer(t, nil)
	only := issueKey(t, rbacSubject(t, "sole-admin"), "admin")

	// Find our own id the way the UI does.
	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/principals", "Bearer "+only, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	listing := decodeJSON[struct {
		Principals    []model.Principal `json:"principals"`
		AuthDBEnabled bool              `json:"auth_db_enabled"`
	}](t, resp)
	assert.True(t, listing.AuthDBEnabled)

	var self model.Principal
	for _, p := range listing.Principals {
		if p.Subject == rbacSubject(t, "sole-admin") {
			self = p
		}
	}
	require.NotEmpty(t, self.Subject, "the acting admin should be in the listing")
	base := ts.URL + "/api/v1/principals/" + self.ID.String()

	resp = doRequest(t, http.MethodPost, base+"/disable", "Bearer "+only, nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "self-disable would lock everyone out")
	resp.Body.Close()

	resp = doRequest(t, http.MethodPut, base+"/roles", "Bearer "+only, map[string]any{"roles": []string{"viewer"}})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "demoting the last admin is the same lockout")
	resp.Body.Close()

	// Still working, so the guard did not half-apply the change.
	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/principals", "Bearer "+only, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// With a successor in place, standing down is allowed.
	successor := createPrincipal(t, ts.URL, only, rbacSubject(t, "successor"), "admin")
	require.NotEmpty(t, successor.APIKey)

	resp = doRequest(t, http.MethodPut, base+"/roles", "Bearer "+only, map[string]any{"roles": []string{"viewer"}})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: rotation
// ---------------------------------------------------------------------------

// A leaked key is fixed without orphaning the evidence already filed under the
// name that leaked it.
func TestRotateReplacesTheKeyAndKeepsTheIdentity(t *testing.T) {
	url, adminKey := adminServer(t)
	subject := rbacSubject(t, "rotating")
	issued := createPrincipal(t, url, adminKey, subject, "contributor")

	resp := doRequest(t, http.MethodPost,
		url+"/api/v1/principals/"+issued.Principal.ID.String()+"/rotate", "Bearer "+adminKey, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	rotated := decodeJSON[issuedPrincipal](t, resp)

	require.NotEmpty(t, rotated.APIKey)
	assert.NotEqual(t, issued.APIKey, rotated.APIKey)
	assert.Equal(t, issued.Principal.ID, rotated.Principal.ID, "same identity")
	assert.Equal(t, []string{"contributor"}, rotated.Principal.Roles, "same roles")

	resp = doRequest(t, http.MethodGet, url+"/api/v1/evidence", "Bearer "+issued.APIKey, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the old key stops working at once")
	resp.Body.Close()

	resp = doRequest(t, http.MethodGet, url+"/api/v1/evidence", "Bearer "+rotated.APIKey, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: who may administer
// ---------------------------------------------------------------------------

func TestPrincipalsAPIIsAdminOnly(t *testing.T) {
	url, adminKey := adminServer(t)
	contributor := issueKey(t, rbacSubject(t, "nosy"), "contributor")
	victim := createPrincipal(t, url, adminKey, rbacSubject(t, "victim"), "viewer")
	id := victim.Principal.ID.String()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/principals"},
		{http.MethodPost, "/api/v1/principals"},
		{http.MethodPut, "/api/v1/principals/" + id + "/roles"},
		{http.MethodPost, "/api/v1/principals/" + id + "/disable"},
		{http.MethodPost, "/api/v1/principals/" + id + "/enable"},
		{http.MethodPost, "/api/v1/principals/" + id + "/rotate"},
	} {
		resp := doRequest(t, tc.method, url+tc.path, "Bearer "+contributor, map[string]any{})
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s", tc.method, tc.path)
		resp.Body.Close()
	}
}

func TestUnknownPrincipalIsNotFound(t *testing.T) {
	url, adminKey := adminServer(t)

	resp := doRequest(t, http.MethodPost,
		url+"/api/v1/principals/6ba7b810-9dad-11d1-80b4-00c04fd430c8/disable", "Bearer "+adminKey, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	resp = doRequest(t, http.MethodPost, url+"/api/v1/principals/not-a-uuid/disable", "Bearer "+adminKey, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: who am I
// ---------------------------------------------------------------------------

type meBody struct {
	Authenticated bool     `json:"authenticated"`
	Subject       string   `json:"subject"`
	Kind          string   `json:"kind"`
	Roles         []string `json:"roles"`
	Permissions   []string `json:"permissions"`
	AuthDBEnabled bool     `json:"auth_db_enabled"`
}

func TestMeDescribesTheCaller(t *testing.T) {
	ts := setupRBACServer(t, nil)
	subject := rbacSubject(t, "curious")
	key := issueKey(t, subject, "ci")

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/me", "Bearer "+key, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	me := decodeJSON[meBody](t, resp)

	assert.True(t, me.Authenticated)
	assert.Equal(t, subject, me.Subject)
	assert.Equal(t, model.PrincipalKindAPIKey, me.Kind)
	assert.Equal(t, []string{"ci"}, me.Roles)
	assert.Contains(t, me.Permissions, "source:any")
	assert.NotContains(t, me.Permissions, "principal:admin")
	assert.True(t, me.AuthDBEnabled)
}

// A principal holding nothing still deserves to be told that that is what it
// holds — which is why /me asserts no permission of its own.
func TestMeAnswersAPrincipalWithNoRoles(t *testing.T) {
	ts := setupRBACServer(t, nil)
	key := issueKey(t, rbacSubject(t, "nobody"))

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/me", "Bearer "+key, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	me := decodeJSON[meBody](t, resp)

	assert.True(t, me.Authenticated)
	assert.Empty(t, me.Roles)
	assert.Empty(t, me.Permissions)
}

// With nothing configured the store is open, and a client should offer
// everything rather than nothing.
func TestMeOnAnOpenStore(t *testing.T) {
	ts := setupAuthServer(t, nil)
	defer ts.Close()

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/me", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	me := decodeJSON[meBody](t, resp)

	assert.False(t, me.Authenticated)
	assert.False(t, me.AuthDBEnabled)
}

// An environment rw key is an administrator, so it can issue database keys —
// which is exactly how an operator bootstraps the cutover from one to the other.
func TestConfiguredRWKeyCanIssueDatabaseKeys(t *testing.T) {
	ts := setupRBACServer(t, []config.APIKey{{Key: "rw-admin-key"}})
	subject := rbacSubject(t, "cutover")

	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/principals", "Bearer rw-admin-key", map[string]any{
		"subject": subject,
		"roles":   []string{"ci"},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	issued := decodeJSON[issuedPrincipal](t, resp)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE id = $1`, issued.Principal.ID)
		assert.NoError(t, err)
	})

	// granted_by is null: an environment key is a secret, not a row, so there
	// is nobody to credit with the grant.
	var grantedBy *string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT granted_by::text FROM role_bindings WHERE principal_id = $1`, issued.Principal.ID).Scan(&grantedBy))
	assert.Nil(t, grantedBy)

	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer "+issued.APIKey, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// Deleting a principal must not be blocked by the grants it issued. The API
// offers no delete — revocation is a timestamp — but operators do it in psql,
// and before migration 000007 the foreign key refused, naming a constraint
// rather than the administrator in the way.
func TestDeletingAGrantorLeavesTheirGrantsStanding(t *testing.T) {
	ctx := context.Background()
	url, adminKey := adminServer(t)
	granted := createPrincipal(t, url, adminKey, rbacSubject(t, "granted-to"), "viewer")

	var grantor string
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT granted_by::text FROM role_bindings WHERE principal_id = $1`, granted.Principal.ID).Scan(&grantor))

	_, err := testPool.Exec(ctx, `DELETE FROM principals WHERE id = $1::uuid`, grantor)
	require.NoError(t, err, "the grantor should be deletable")

	// The grant survives; only who issued it is gone.
	var roles []string
	var stillGrantedBy *string
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT array_agg(role), max(granted_by::text) FROM role_bindings WHERE principal_id = $1`,
		granted.Principal.ID).Scan(&roles, &stillGrantedBy))
	assert.Equal(t, []string{"viewer"}, roles, "the role must not vanish with the colleague who granted it")
	assert.Nil(t, stillGrantedBy)
}

// An administrator creating a key records who did it, which is what the column
// is for.
func TestGrantsRecordWhoMadeThem(t *testing.T) {
	url, adminKey := adminServer(t)
	issued := createPrincipal(t, url, adminKey, rbacSubject(t, "credited"), "viewer")

	var grantedBy *string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT granted_by::text FROM role_bindings WHERE principal_id = $1`, issued.Principal.ID).Scan(&grantedBy))
	require.NotNil(t, grantedBy)

	var grantorSubject string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT subject FROM principals WHERE id = $1::uuid`, *grantedBy).Scan(&grantorSubject))
	assert.Equal(t, rbacSubject(t, "acting-admin"), grantorSubject)
}
