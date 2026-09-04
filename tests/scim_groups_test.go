package tests

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 3 of #146: groups, and the roles they grant.
//
// A group's displayName is looked up in the same role map the login path uses.
// The interesting half is removal: taking somebody out of a group has to take
// the role away, or a promotion is reversible only by deleting the person.

// rolesOf reads what somebody may do, by source, so a test can tell a grant the
// directory made from one an administrator made by hand.
func rolesOf(t *testing.T, userName string) map[string][]string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT rb.source, rb.role
		  FROM role_bindings rb
		  JOIN principals p ON p.id = rb.principal_id
		 WHERE p.user_name = $1
	`, userName)
	require.NoError(t, err)
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var source, role string
		require.NoError(t, rows.Scan(&source, &role))
		out[source] = append(out[source], role)
	}
	for _, roles := range out {
		sort.Strings(roles)
	}
	return out
}

// createGroup provisions a group and cleans it up.
func (c *scimClient) createGroup(displayName string, memberIDs ...string) map[string]any {
	c.t.Helper()
	members := "["
	for i, id := range memberIDs {
		if i > 0 {
			members += ","
		}
		members += fmt.Sprintf(`{"value": %q}`, id)
	}
	members += "]"

	resp, body := c.do(http.MethodPost, "/scim/v2/Groups", fmt.Sprintf(`{
	  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:Group"],
	  "displayName": %q,
	  "externalId": "entra-group-%s",
	  "members": %s
	}`, displayName, displayName, members))
	require.Equal(c.t, http.StatusCreated, resp.StatusCode, "%v", body)
	// By id, not by name: a test that renames a group would otherwise clean up
	// nothing and leave the new name behind for whoever runs next.
	scimID, _ := body["id"].(string)
	c.t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(),
			`DELETE FROM scim_groups WHERE scim_id = $1`, scimID)
		assert.NoError(c.t, err)
	})
	return body
}

// ---------------------------------------------------------------------------
// Membership grants and removes
// ---------------------------------------------------------------------------

func TestGroupMembershipGrantsTheMappedRole(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "member")
	user := c.createUser(name, name, "Member")

	group := c.createGroup("eng-all", user["id"].(string))

	assert.Equal(t, "eng-all", group["displayName"])
	members, _ := group["members"].([]any)
	require.Len(t, members, 1, "the group has to report its own membership back")
	assert.Equal(t, user["id"], members[0].(map[string]any)["value"])

	assert.Equal(t, []string{"contributor"}, rolesOf(t, name)["scim"],
		"membership of a mapped group is what grants the role")
}

// The half that matters. Without it a promotion could only be undone by
// deleting the person.
func TestRemovingSomebodyFromAGroupTakesTheRoleAway(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "demoted")
	user := c.createUser(name, name, "Demoted")
	group := c.createGroup("eng-all", user["id"].(string))
	require.Equal(t, []string{"contributor"}, rolesOf(t, name)["scim"])

	resp, _ := c.do(http.MethodPatch, "/scim/v2/Groups/"+group["id"].(string), fmt.Sprintf(`{
	  "schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
	  "Operations": [{"op": "remove", "path": "members", "value": [{"value": %q}]}]
	}`, user["id"]))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Empty(t, rolesOf(t, name)["scim"], "leaving the group should take the role with it")
}

// The spelling a provisioner most often uses to remove one member: the subject
// is named in the path, not the value.
func TestRemovingByFilterPathIsUnderstood(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "filtered")
	user := c.createUser(name, name, "Filtered")
	group := c.createGroup("eng-all", user["id"].(string))
	require.NotEmpty(t, rolesOf(t, name)["scim"])

	resp, _ := c.do(http.MethodPatch, "/scim/v2/Groups/"+group["id"].(string), fmt.Sprintf(`{
	  "Operations": [{"op": "remove", "path": "members[value eq \"%s\"]"}]
	}`, user["id"]))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Empty(t, rolesOf(t, name)["scim"])
}

func TestAddingSomebodyToAGroupLaterGrantsTheRole(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "joiner")
	user := c.createUser(name, name, "Joiner")
	group := c.createGroup("eng-leads")
	require.Empty(t, rolesOf(t, name)["scim"])

	resp, _ := c.do(http.MethodPatch, "/scim/v2/Groups/"+group["id"].(string), fmt.Sprintf(`{
	  "Operations": [{"op": "add", "path": "members", "value": [{"value": %q}]}]
	}`, user["id"]))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, []string{"admin"}, rolesOf(t, name)["scim"])
}

// The rule that keeps a company directory from handing every employee an
// account that can write.
func TestAnUnmappedGroupGrantsNothing(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "everyone")
	user := c.createUser(name, name, "Everyone Else")

	group := c.createGroup("everyone-in-the-company", user["id"].(string))

	assert.Empty(t, rolesOf(t, name)["scim"],
		"a group nobody has mapped must grant nothing at all")
	// It still has to exist and still has to list its members, or the
	// provisioner concludes its write was lost and repeats it forever.
	members, _ := group["members"].([]any)
	assert.Len(t, members, 1)
}

func TestSomebodyInTwoGroupsHoldsBoth(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "both")
	user := c.createUser(name, name, "Both")

	c.createGroup("eng-all", user["id"].(string))
	c.createGroup("eng-leads", user["id"].(string))

	assert.Equal(t, []string{"admin", "contributor"}, rolesOf(t, name)["scim"])
}

// ---------------------------------------------------------------------------
// What a sync must not disturb
// ---------------------------------------------------------------------------

// A role an administrator granted by hand survives a sync — the same promise
// the login path makes. Otherwise the Access tab would be quietly undone by the
// directory, and a deployment whose groups map to nothing would have no way to
// grant anything at all.
func TestASyncLeavesALocallyGrantedRoleAlone(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "handgranted")
	user := c.createUser(name, name, "Hand Granted")

	var principalID uuid.UUID
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM principals WHERE user_name = $1`, name).Scan(&principalID))
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO role_bindings (principal_id, role, scope, source)
		VALUES ($1, 'ci', '*', 'local')
	`, principalID)
	require.NoError(t, err)

	group := c.createGroup("eng-all", user["id"].(string))
	require.Equal(t, []string{"contributor"}, rolesOf(t, name)["scim"])

	// And a removal, which is the reconciliation most likely to overreach.
	resp, _ := c.do(http.MethodPatch, "/scim/v2/Groups/"+group["id"].(string), fmt.Sprintf(`{
	  "Operations": [{"op": "remove", "path": "members", "value": [{"value": %q}]}]
	}`, user["id"]))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, []string{"ci"}, rolesOf(t, name)["local"],
		"an administrator's grant is not the directory's to withdraw")
	assert.Empty(t, rolesOf(t, name)["scim"])
}

// A login reconciles its own grants against the token it arrived with. If a
// sync wrote into the same source, the two would delete each other's work on
// alternate runs.
func TestASyncLeavesTheLoginsOwnGrantsAlone(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "alsologgedin")
	user := c.createUser(name, name, "Also Logged In")

	var principalID uuid.UUID
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM principals WHERE user_name = $1`, name).Scan(&principalID))
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO role_bindings (principal_id, role, scope, source)
		VALUES ($1, 'viewer', '*', 'idp')
	`, principalID)
	require.NoError(t, err)

	c.createGroup("eng-all", user["id"].(string))

	roles := rolesOf(t, name)
	assert.Equal(t, []string{"viewer"}, roles["idp"], "the login's grant should survive a sync")
	assert.Equal(t, []string{"contributor"}, roles["scim"])
}

// ---------------------------------------------------------------------------
// Renaming, replacing, deleting
// ---------------------------------------------------------------------------

// The name is what the role map is keyed by, so renaming a group changes what
// it grants — to everybody in it, not only to whoever the patch named.
func TestRenamingAGroupChangesWhatItGrants(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "renamed")
	user := c.createUser(name, name, "Renamed Group Member")
	group := c.createGroup("eng-all", user["id"].(string))
	require.Equal(t, []string{"contributor"}, rolesOf(t, name)["scim"])

	resp, _ := c.do(http.MethodPatch, "/scim/v2/Groups/"+group["id"].(string), `{
	  "Operations": [{"op": "replace", "path": "displayName", "value": "eng-leads"}]
	}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, []string{"admin"}, rolesOf(t, name)["scim"])
}

// PUT states the whole group, so anybody not named in it is on their way out.
func TestReplacingAGroupDropsWhoeverIsNotNamed(t *testing.T) {
	c := newSCIMClient(t)
	staying := scimName(t, "staying")
	leaving := scimName(t, "leaving")
	stays := c.createUser(staying, staying, "Stays")
	goes := c.createUser(leaving, leaving, "Goes")

	group := c.createGroup("eng-all", stays["id"].(string), goes["id"].(string))
	require.NotEmpty(t, rolesOf(t, leaving)["scim"])

	resp, _ := c.do(http.MethodPut, "/scim/v2/Groups/"+group["id"].(string), fmt.Sprintf(`{
	  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:Group"],
	  "displayName": "eng-all",
	  "members": [{"value": %q}]
	}`, stays["id"]))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, []string{"contributor"}, rolesOf(t, staying)["scim"])
	assert.Empty(t, rolesOf(t, leaving)["scim"], "a PUT states the membership, so the rest are out")
}

func TestDeletingAGroupTakesBackWhatItGranted(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "grouptogo")
	user := c.createUser(name, name, "Group To Go")
	group := c.createGroup("eng-all", user["id"].(string))
	require.NotEmpty(t, rolesOf(t, name)["scim"])

	resp, _ := c.do(http.MethodDelete, "/scim/v2/Groups/"+group["id"].(string), "")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	assert.Empty(t, rolesOf(t, name)["scim"], "the access a deleted group granted has to go with it")

	// The person stays. Only their access came from the group.
	var count int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM principals WHERE user_name = $1`, name).Scan(&count))
	assert.Equal(t, 1, count)
}

// ---------------------------------------------------------------------------
// Reading back
// ---------------------------------------------------------------------------

// The query a provisioner makes before creating a group.
func TestFilteringGroupsByDisplayName(t *testing.T) {
	c := newSCIMClient(t)
	c.createGroup("eng-all")
	c.createGroup("eng-leads")

	resp, body := c.do(http.MethodGet,
		`/scim/v2/Groups?filter=`+urlQuery(`displayName eq "eng-leads"`), "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, float64(1), body["totalResults"])
	resources, _ := body["Resources"].([]any)
	require.Len(t, resources, 1)
	assert.Equal(t, "eng-leads", resources[0].(map[string]any)["displayName"])
}

func TestTwoGroupsCannotShareAName(t *testing.T) {
	c := newSCIMClient(t)
	c.createGroup("eng-all")

	resp, body := c.do(http.MethodPost, "/scim/v2/Groups", `{"displayName": "eng-all"}`)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "uniqueness", body["scimType"])
}

// A directory syncing a group before the people in it is ordinary. Failing the
// request would stall the sync on its first group.
func TestAMemberWeHaveNeverHeardOfIsSkipped(t *testing.T) {
	c := newSCIMClient(t)
	name := scimName(t, "known")
	known := c.createUser(name, name, "Known")

	group := c.createGroup("eng-all", known["id"].(string), uuid.NewString())

	members, _ := group["members"].([]any)
	assert.Len(t, members, 1, "the member we know should still have been added")
	assert.Equal(t, []string{"contributor"}, rolesOf(t, name)["scim"])
}

func TestReadingAGroupBackByItsId(t *testing.T) {
	c := newSCIMClient(t)
	group := c.createGroup("eng-all")

	resp, body := c.do(http.MethodGet, "/scim/v2/Groups/"+group["id"].(string), "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "eng-all", body["displayName"])
	assert.Contains(t, body["schemas"], "urn:ietf:params:scim:schemas:core:2.0:Group")

	resp, _ = c.do(http.MethodGet, "/scim/v2/Groups/"+uuid.NewString(), "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
