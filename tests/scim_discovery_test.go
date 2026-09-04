package tests

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 4 of #146: what a provisioner reads before it provisions, and the role
// its token should hold.
//
// Entra fetches the three discovery endpoints on its Test Connection and takes
// them literally, so the interesting assertions here are the ones about saying
// no: a store claiming support it does not have gets sent requests it cannot
// answer.

// provisionerClient is the directory's own credential — scim:provision and
// nothing else, which is what a token living in someone else's configuration
// for years should be able to do.
func newProvisionerClient(t *testing.T) *scimClient {
	t.Helper()
	key := issueKey(t, rbacSubject(t, "directory"), "provisioner")
	ts := setupSCIMServer(t)
	return &scimClient{base: ts.URL, key: key, t: t}
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

func TestServiceProviderConfigSaysWhatIsActuallySupported(t *testing.T) {
	c := newProvisionerClient(t)
	resp, body := c.do(http.MethodGet, "/scim/v2/ServiceProviderConfig", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Contains(t, body["schemas"],
		"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig")

	supported := func(key string) any {
		section, _ := body[key].(map[string]any)
		require.NotNil(t, section, "%s should be described", key)
		return section["supported"]
	}
	assert.Equal(t, true, supported("patch"), "deactivation arrives as a PATCH")
	assert.Equal(t, true, supported("filter"), "a client filters before it creates")

	// The honest noes. Claiming these would have a client sending requests
	// this store cannot answer.
	assert.Equal(t, false, supported("bulk"))
	assert.Equal(t, false, supported("sort"))
	assert.Equal(t, false, supported("etag"))
	assert.Equal(t, false, supported("changePassword"), "this store holds no passwords")

	schemes, _ := body["authenticationSchemes"].([]any)
	require.NotEmpty(t, schemes, "a client needs telling how to authenticate")
	assert.Equal(t, "oauthbearertoken", schemes[0].(map[string]any)["type"])
}

func TestResourceTypesNamesWhatCanBeProvisioned(t *testing.T) {
	c := newProvisionerClient(t)
	resp, body := c.do(http.MethodGet, "/scim/v2/ResourceTypes", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resources, _ := body["Resources"].([]any)
	require.Len(t, resources, 2)

	endpoints := map[string]string{}
	for _, raw := range resources {
		r := raw.(map[string]any)
		endpoints[r["id"].(string)] = r["endpoint"].(string)
	}
	assert.Equal(t, "/Users", endpoints["User"])
	assert.Equal(t, "/Groups", endpoints["Group"])
}

// The schema is deliberately shorter than the specification's. A client reads
// it to decide what to send, and advertising attributes that are accepted and
// then dropped would have a directory believing it had synchronised something
// this store has no column for.
func TestSchemasDescribeOnlyWhatIsKept(t *testing.T) {
	c := newProvisionerClient(t)
	resp, body := c.do(http.MethodGet, "/scim/v2/Schemas", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resources, _ := body["Resources"].([]any)
	require.Len(t, resources, 2)

	attrs := map[string]map[string]any{}
	for _, raw := range resources {
		schema := raw.(map[string]any)
		if schema["id"] != "urn:ietf:params:scim:schemas:core:2.0:User" {
			continue
		}
		for _, a := range schema["attributes"].([]any) {
			attr := a.(map[string]any)
			attrs[attr["name"].(string)] = attr
		}
	}

	require.Contains(t, attrs, "userName")
	assert.Equal(t, true, attrs["userName"]["required"])
	assert.Equal(t, "server", attrs["userName"]["uniqueness"],
		"a second person with the same login name is refused, so say so")
	assert.Contains(t, attrs, "active", "the attribute the whole protocol turns on")
	assert.Contains(t, attrs, "emails")

	assert.NotContains(t, attrs, "title", "an attribute this store drops must not be advertised")
	assert.NotContains(t, attrs, "password")
}

// ---------------------------------------------------------------------------
// Who may provision
// ---------------------------------------------------------------------------

// The point of the role: a token that sits in another company's configuration
// for years should be able to do exactly one thing.
func TestAProvisionerCanProvisionAndNothingElse(t *testing.T) {
	c := newProvisionerClient(t)

	name := scimName(t, "byprovisioner")
	c.createUser(name, name, "By Provisioner")

	for _, path := range []string{
		"/api/v1/evidence",
		"/api/v1/analytics/summary",
		"/api/v1/principals",
	} {
		req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+c.key)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a directory's token has no business reading %s", path)
	}
}

// An administrator keeps it, so a deployment can drive SCIM by hand before it
// has minted a dedicated token.
func TestAnAdministratorMayStillProvision(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "byadmin")
	c.createUser(name, name, "By Admin")
}

func TestEveryOtherRoleIsRefused(t *testing.T) {
	ts := setupSCIMServer(t)
	for _, role := range []string{"viewer", "contributor", "ci"} {
		t.Run(role, func(t *testing.T) {
			key := issueKey(t, rbacSubject(t, role), role)
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/scim/v2/Users", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+key)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		})
	}
}

// ---------------------------------------------------------------------------
// The guard phase 2 could not reach
// ---------------------------------------------------------------------------

// While provisioning required principal:admin, the caller was always itself an
// administrator, so "this is the last one" could never be true and the guard
// could not fire on the only path that reaches it. With the directory holding
// provisioner instead, it can — and a directory that deactivates the last
// administrator would otherwise leave the deployment with no way in but psql.
func TestAProvisionerCannotDeactivateTheLastAdministrator(t *testing.T) {
	c := newProvisionerClient(t)
	ctx := context.Background()

	name := scimName(t, "onlyadmin")
	user := c.createUser(name, name, "Only Admin")

	var principalID uuid.UUID
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT id FROM principals WHERE user_name = $1`, name).Scan(&principalID))

	// Park everyone else's admin so this person really is the last one.
	var parked []uuid.UUID
	rows, err := testPool.Query(ctx,
		`SELECT principal_id FROM role_bindings WHERE role = 'admin' AND principal_id <> $1`, principalID)
	require.NoError(t, err)
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		parked = append(parked, id)
	}
	rows.Close()
	_, err = testPool.Exec(ctx,
		`DELETE FROM role_bindings WHERE role = 'admin' AND principal_id <> $1`, principalID)
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
		INSERT INTO role_bindings (principal_id, role, scope, source) VALUES ($1, 'admin', '*', 'scim')
	`, principalID)
	require.NoError(t, err)

	resp, body := c.do(http.MethodDelete, "/scim/v2/Users/"+user["id"].(string), "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "mutability", body["scimType"])

	var disabled bool
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT disabled_at IS NOT NULL FROM principals WHERE id = $1`, principalID).Scan(&disabled))
	assert.False(t, disabled, "the last way into the store has to survive the request")
}

// The guard is about the last administrator, not about administrators in
// general: a directory must still be able to deprovision one of several.
func TestOneOfSeveralAdministratorsCanBeDeactivated(t *testing.T) {
	c := newProvisionerClient(t)
	ctx := context.Background()

	name := scimName(t, "oneofmany")
	user := c.createUser(name, name, "One Of Many")

	var principalID uuid.UUID
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT id FROM principals WHERE user_name = $1`, name).Scan(&principalID))
	_, err := testPool.Exec(ctx, `
		INSERT INTO role_bindings (principal_id, role, scope, source) VALUES ($1, 'admin', '*', 'scim')
	`, principalID)
	require.NoError(t, err)

	// Somebody else who can still administer the store.
	issueKey(t, rbacSubject(t, "otheradmin"), "admin")

	resp, _ := c.do(http.MethodDelete, "/scim/v2/Users/"+user["id"].(string), "")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	var disabled bool
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT disabled_at IS NOT NULL FROM principals WHERE id = $1`, principalID).Scan(&disabled))
	assert.True(t, disabled, "leaving is leaving, even for an administrator")
}
