package tests

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The identity rule, from docs/scim-provisioning-plan.md.
//
// A provisioned user and that same human's later login have to land on one
// principal. If they do not, everybody ends up as two rows: one holding their
// evidence, and one that is the only thing the provisioner can deactivate —
// which would make deprovisioning look like it worked while the account that
// matters stayed live.
//
// They cannot be matched on the obvious key. A login is identified by
// "<issuer>|<sub>", Entra's sub is pairwise and invented at first login, and so
// SCIM has never seen it. The provisioned row therefore starts unclaimed, and
// the first login that recognises itself in one takes it.

// provisionPrincipal writes the row a SCIM provisioner would have written:
// named, unclaimed, and belonging to nobody's login yet.
func provisionPrincipal(t *testing.T, subject, userName string) uuid.UUID {
	t.Helper()
	scimID := "scim-" + uuid.NewString()
	var id uuid.UUID
	require.NoError(t, testPool.QueryRow(context.Background(), `
		INSERT INTO principals (subject, kind, display_name, scim_id, scim_external_id, user_name)
		VALUES ($1, 'user', $2, $3, $4, $5)
		RETURNING id
	`, subject, "Provisioned Person", scimID, "entra-object-id", userName).Scan(&id))
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE id = $1`, id)
		assert.NoError(t, err)
	})
	return id
}

func principalCount(t *testing.T, subject string) int {
	t.Helper()
	var n int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM principals WHERE subject = $1`, subject).Scan(&n))
	return n
}

// ---------------------------------------------------------------------------
// What a login may claim
// ---------------------------------------------------------------------------

func TestFirstLoginClaimsTheRowProvisionedForThem(t *testing.T) {
	idp := newMockIdP(t)
	idp.email = "provisioned@example.com"
	idp.groups = []string{"eng-all"}
	dropPrincipal(t, "%idp-subject-001")

	provisioned := provisionPrincipal(t, "user:provisioned@example.com", "provisioned@example.com")

	base, client := ssoServer(t, idp, map[string]string{"eng-all": "contributor"})
	logIn(t, base, client).Body.Close()

	me := meOf(t, base, client)
	require.True(t, me.Authenticated)
	assert.Equal(t, "user:provisioned@example.com", me.Subject)
	assert.Equal(t, 1, principalCount(t, "user:provisioned@example.com"),
		"the login should have taken the provisioned row, not made a second one")

	var id uuid.UUID
	var externalID *string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id, external_id FROM principals WHERE subject = $1`,
		"user:provisioned@example.com").Scan(&id, &externalID))
	assert.Equal(t, provisioned, id, "it should be the same row, not a replacement")
	require.NotNil(t, externalID)
	assert.Contains(t, *externalID, "idp-subject-001",
		"claiming is what writes the login's own name into the row")
}

// A directory often knows somebody by a UPN that is not the address in their
// token. Matching on the subject alone would miss the row and provision them
// twice.
func TestALoginClaimsARowKnownByADifferentLoginName(t *testing.T) {
	idp := newMockIdP(t)
	idp.email = "carol@example.com"
	idp.preferredUsername = "carol@corp.onmicrosoft.com"
	dropPrincipal(t, "%idp-subject-001")

	provisioned := provisionPrincipal(t, "user:carol@corp.onmicrosoft.com", "carol@corp.onmicrosoft.com")

	base, client := ssoServer(t, idp, nil)
	logIn(t, base, client).Body.Close()
	require.True(t, meOf(t, base, client).Authenticated)

	var id uuid.UUID
	var subject string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id, subject FROM principals WHERE user_name = $1`,
		"carol@corp.onmicrosoft.com").Scan(&id, &subject))
	assert.Equal(t, provisioned, id)
	assert.Equal(t, "user:carol@example.com", subject,
		"the subject follows the token, which is what a reader of the evidence will see")
}

// Claiming happens once. After it, the login is an ordinary one matched on its
// external id — so a rename still follows the sub rather than the address.
func TestClaimingHappensOnlyOnce(t *testing.T) {
	idp := newMockIdP(t)
	idp.email = "moving@example.com"
	dropPrincipal(t, "%idp-subject-001")
	provisioned := provisionPrincipal(t, "user:moving@example.com", "moving@example.com")

	base, client := ssoServer(t, idp, nil)
	logIn(t, base, client).Body.Close()
	require.True(t, meOf(t, base, client).Authenticated)

	// Same person, new address. A second claim is impossible now, so this has
	// to be recognised by its sub and correct the subject in place.
	idp.email = "moved@example.com"
	_, client2 := ssoServer(t, idp, nil)
	logIn(t, base, client2).Body.Close()

	var id uuid.UUID
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM principals WHERE subject = $1`, "user:moved@example.com").Scan(&id))
	assert.Equal(t, provisioned, id, "a rename should stay one principal")
	assert.Zero(t, principalCount(t, "user:moving@example.com"),
		"the old address should not be left behind as a second principal")
}

// The provisioner disabling somebody is the whole point of provisioning, so a
// login must land on that row and be refused — not sidestep it by creating a
// second, enabled principal for the person just shut out.
func TestALoginCannotEscapeADisabledProvisionedRow(t *testing.T) {
	idp := newMockIdP(t)
	idp.email = "departed@example.com"
	dropPrincipal(t, "%idp-subject-001")

	id := provisionPrincipal(t, "user:departed@example.com", "departed@example.com")
	_, err := testPool.Exec(context.Background(),
		`UPDATE principals SET disabled_at = now() WHERE id = $1`, id)
	require.NoError(t, err)

	base, client := ssoServer(t, idp, nil)
	resp := logIn(t, base, client)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, statusOf(t, base, client, "/api/v1/me"),
		"somebody the directory has deactivated should not get in")
	assert.Equal(t, 1, principalCount(t, "user:departed@example.com"),
		"and should certainly not get a second, enabled account out of trying")
}

// ---------------------------------------------------------------------------
// What a login may not claim
// ---------------------------------------------------------------------------

// A row already claimed belongs to whoever claimed it. Someone who inherits a
// departed colleague's address must not inherit their history with it.
func TestASecondPersonCannotTakeAClaimedRow(t *testing.T) {
	first := newMockIdP(t)
	first.email = "shared@example.com"
	dropPrincipal(t, "%idp-subject-001")
	provisioned := provisionPrincipal(t, "user:shared@example.com", "shared@example.com")

	base, client := ssoServer(t, first, nil)
	logIn(t, base, client).Body.Close()
	require.True(t, meOf(t, base, client).Authenticated)

	// A different human at the same provider, handed the same address.
	second := newMockIdP(t)
	second.subject = "idp-subject-999"
	second.email = "shared@example.com"
	dropPrincipal(t, "%idp-subject-999")

	base2, client2 := ssoServer(t, second, nil)
	resp := logIn(t, base2, client2)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, statusOf(t, base2, client2, "/api/v1/me"),
		"the store should refuse rather than guess these are the same person")

	var externalID string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT external_id FROM principals WHERE id = $1`, provisioned).Scan(&externalID))
	assert.Contains(t, externalID, "idp-subject-001",
		"the row should still belong to whoever claimed it first")
}

// An API key that happens to be named after a person is not that person. This
// is the existing ErrSubjectTaken rule, and claiming must not open a way round
// it: a key with evidence:write could otherwise be handed a human's roles.
func TestALoginCannotClaimAnAPIKey(t *testing.T) {
	idp := newMockIdP(t)
	idp.email = "robot@example.com"
	dropPrincipal(t, "%idp-subject-001")

	var keyID uuid.UUID
	require.NoError(t, testPool.QueryRow(context.Background(), `
		INSERT INTO principals (subject, kind, display_name, key_hash)
		VALUES ($1, 'api_key', 'A key named after a person', $2)
		RETURNING id
	`, "user:robot@example.com", fmt.Sprintf("hash-%d", time.Now().UnixNano())).Scan(&keyID))
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE id = $1`, keyID)
		assert.NoError(t, err)
	})

	base, client := ssoServer(t, idp, nil)
	resp := logIn(t, base, client)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, statusOf(t, base, client, "/api/v1/me"))

	var kind string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT kind FROM principals WHERE id = $1`, keyID).Scan(&kind))
	assert.Equal(t, "api_key", kind, "the key should not have been turned into a person")
}

// A principal created by an earlier login — no scim_id — is not a provisioned
// row and is not up for grabs. Only what a provisioner created may be claimed,
// which is what keeps claiming to exactly one event per person.
func TestALoginCannotClaimAnotherLoginsPrincipal(t *testing.T) {
	idp := newMockIdP(t)
	idp.email = "jit@example.com"
	dropPrincipal(t, "%idp-subject-001")

	base, client := ssoServer(t, idp, nil)
	logIn(t, base, client).Body.Close()
	require.True(t, meOf(t, base, client).Authenticated)

	var scimID *string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT scim_id FROM principals WHERE subject = $1`, "user:jit@example.com").Scan(&scimID))
	require.Nil(t, scimID, "a login creates an unprovisioned principal")

	// Someone else at the provider, arriving at the same address.
	other := newMockIdP(t)
	other.subject = "idp-subject-888"
	other.email = "jit@example.com"
	dropPrincipal(t, "%idp-subject-888")

	base2, client2 := ssoServer(t, other, nil)
	resp := logIn(t, base2, client2)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, statusOf(t, base2, client2, "/api/v1/me"))
	assert.Equal(t, 1, principalCount(t, "user:jit@example.com"))
}
