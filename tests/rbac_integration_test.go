package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// setupRBACServer runs a server with database-backed principals switched on.
// Environment keys are passed too, because the posture that matters most is
// the migration one where both sources are live.
func setupRBACServer(t *testing.T, keys []config.APIKey) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		DatabaseURL:     testDatabaseURL,
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
		APIKeys:         keys,
		Auth:            config.Auth{DB: true},
		Blob:            testBlobConfig,
	}
	ts := httptest.NewServer(server.New(cfg, testPool, testBlobStore, nil).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// issueKey creates a principal the way the admin API will in phase 4: mint,
// hash, store, hand back the plaintext once.
func issueKey(t *testing.T, subject string, roles ...string) string {
	t.Helper()
	ctx := context.Background()

	key, err := auth.GenerateKey()
	require.NoError(t, err)

	created, err := store.NewPrincipalStore(testPool).Insert(ctx, model.PrincipalCreate{
		Subject:     subject,
		Kind:        model.PrincipalKindAPIKey,
		DisplayName: subject,
		KeyHash:     auth.HashKey(key),
		Roles:       roles,
	})
	require.NoError(t, err)
	require.NotNil(t, created, "subject %q was already taken", subject)

	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE id = $1`, created.ID)
		assert.NoError(t, err)
	})
	return key
}

func rbacSubject(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("ci:%s-%s", strings.ToLower(t.Name()), suffix)
}

// ---------------------------------------------------------------------------
// Tests: database-backed keys
// ---------------------------------------------------------------------------

// The finer split phase 2 exists to deliver. An environment rw key is
// necessarily ci *and* admin, because it could reach every endpoint before
// there was anything smaller to grant. A database key holds what it was given.
func TestDBKeyHoldsOnlyTheRolesItWasGranted(t *testing.T) {
	ts := setupRBACServer(t, nil)

	viewerSubject, ciSubject, adminSubject :=
		rbacSubject(t, "viewer"), rbacSubject(t, "ci"), rbacSubject(t, "admin")
	viewer := issueKey(t, viewerSubject, "viewer")
	ci := issueKey(t, ciSubject, "ci")
	admin := issueKey(t, adminSubject, "admin")

	// Each caller files under its own name. Only ci may do otherwise, and what
	// this test is about is which endpoints a role reaches — the source rule
	// itself is tests/source_binding_test.go.
	evidence := func(source string) any {
		return makeEvidence("org/rbac_roles", "main", "ref1", "//pkg:test", source, model.ResultPass)
	}
	declaration := func(who string) any {
		return model.InheritanceCreate{
			Repo:          "org/rbac_roles",
			SourceRCSRef:  "src_" + who,
			TargetRCSRef:  "tgt_" + who,
			Scope:         json.RawMessage(`["//pkg:*"]`),
			Justification: "role check",
			CreatedBy:     who,
		}
	}

	for _, tc := range []struct {
		role                              string
		key, subject                      string
		read, writeEvidence, writeInherit int
	}{
		{"viewer", viewer, viewerSubject, http.StatusOK, http.StatusForbidden, http.StatusForbidden},
		// The one that was impossible before: a key that ingests results all
		// day and still cannot rewrite what the store says about untested code.
		{"ci", ci, ciSubject, http.StatusOK, http.StatusCreated, http.StatusForbidden},
		{"admin", admin, adminSubject, http.StatusOK, http.StatusCreated, http.StatusCreated},
	} {
		t.Run(tc.role, func(t *testing.T) {
			bearer := "Bearer " + tc.key

			resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", bearer, nil)
			assert.Equal(t, tc.read, resp.StatusCode, "GET /evidence")
			resp.Body.Close()

			resp = doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", bearer, evidence(tc.subject))
			assert.Equal(t, tc.writeEvidence, resp.StatusCode, "POST /evidence")
			resp.Body.Close()

			resp = doRequest(t, http.MethodPost, ts.URL+"/api/v1/inheritance", bearer, declaration(tc.role))
			assert.Equal(t, tc.writeInherit, resp.StatusCode, "POST /inheritance")
			resp.Body.Close()
		})
	}
}

func TestUnknownDBKeyIsRejected(t *testing.T) {
	ts := setupRBACServer(t, nil)
	issueKey(t, rbacSubject(t, "real"), "admin")

	unminted, err := auth.GenerateKey()
	require.NoError(t, err)

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer "+unminted, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// Revocation is what an environment variable could never do. It has to bite on
// the next request, not the next redeploy.
func TestDisabledPrincipalStopsAuthenticatingImmediately(t *testing.T) {
	ts := setupRBACServer(t, nil)
	subject := rbacSubject(t, "revoked")
	key := issueKey(t, subject, "admin")

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer "+key, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	_, err := testPool.Exec(context.Background(),
		`UPDATE principals SET disabled_at = now() WHERE subject = $1`, subject)
	require.NoError(t, err)

	// Same server, same key, no restart.
	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer "+key, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// An authenticated caller holding nothing is not an authorized one. This is
// what a principal looks like the moment before its first role is granted.
func TestPrincipalWithNoBindingsIsForbiddenEverywhere(t *testing.T) {
	ts := setupRBACServer(t, nil)
	key := issueKey(t, rbacSubject(t, "unbound"))

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer "+key, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

// Switching database authentication on closes the door. An empty principals
// table means nobody may in — the pass-through posture belongs to a store with
// nothing configured at all, and this store has something configured.
func TestDBAuthDoesNotFallOpen(t *testing.T) {
	ts := setupRBACServer(t, nil)

	resp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// The migration path: issue database keys, move pipelines over one at a time,
// clear EVIDENCE_API_KEYS when the last has moved. Both sources have to work
// at once for that, and each authenticator has never heard of the other's keys.
func TestEnvKeysAndDBKeysAreLiveTogether(t *testing.T) {
	ts := setupRBACServer(t, []config.APIKey{{Key: "legacy-rw-key"}})
	dbKey := issueKey(t, rbacSubject(t, "migrating"), "ci")

	// The legacy key keeps everything it had, inheritance included.
	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/inheritance", "Bearer legacy-rw-key", model.InheritanceCreate{
		Repo:          "org/rbac_migration",
		SourceRCSRef:  "src_legacy",
		TargetRCSRef:  "tgt_legacy",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "env keys keep working",
		CreatedBy:     "legacy-rw-key",
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode, "the env key must not be shadowed by the database")
	resp.Body.Close()

	// The database key is unknown to the static authenticator, which is asked
	// first. Its rejection must not be the last word.
	resp = doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "Bearer "+dbKey,
		makeEvidence("org/rbac_migration", "main", "ref1", "//pkg:test", "ci", model.ResultPass))
	assert.Equal(t, http.StatusCreated, resp.StatusCode, "the database key must not be shadowed by the env list")
	resp.Body.Close()

	// A credential neither source knows is still rejected.
	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer neither-of-them", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: the store
// ---------------------------------------------------------------------------

func TestPrincipalLookupReturnsEveryStoreWideRole(t *testing.T) {
	ctx := context.Background()
	ps := store.NewPrincipalStore(testPool)
	key := issueKey(t, rbacSubject(t, "multi"), "ci", "admin")

	got, err := ps.FindByKeyHash(ctx, auth.HashKey(key))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.ElementsMatch(t, []string{"ci", "admin"}, got.Roles)
	assert.False(t, got.Disabled())
}

// An unknown key is an answer, not a failure: it is what keeps a database
// outage distinguishable from a typo.
func TestPrincipalLookupReportsAMissWithoutAnError(t *testing.T) {
	got, err := store.NewPrincipalStore(testPool).FindByKeyHash(context.Background(), auth.HashKey("nothing"))
	require.NoError(t, err)
	assert.Nil(t, got)
}

// Per-repo scoping is reserved and inert. Reading a scoped binding as though it
// were store-wide is the silent widening the column's default exists to
// prevent, so the lookup must ignore it entirely.
func TestScopedBindingsGrantNothingYet(t *testing.T) {
	ctx := context.Background()
	subject := rbacSubject(t, "scoped")
	key := issueKey(t, subject, "viewer")

	_, err := testPool.Exec(ctx, `
		INSERT INTO role_bindings (principal_id, role, scope)
		SELECT id, 'admin', 'repo:org/secret' FROM principals WHERE subject = $1
	`, subject)
	require.NoError(t, err)

	got, err := store.NewPrincipalStore(testPool).FindByKeyHash(ctx, auth.HashKey(key))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []string{"viewer"}, got.Roles, "a scoped grant is not a store-wide one")
}

func TestLastSeenIsRecordedOnUse(t *testing.T) {
	ctx := context.Background()
	subject := rbacSubject(t, "seen")
	key := issueKey(t, subject, "viewer")
	ps := store.NewPrincipalStore(testPool)

	before, err := ps.FindByKeyHash(ctx, auth.HashKey(key))
	require.NoError(t, err)
	require.Nil(t, before.LastSeenAt, "a key that has never been used has never been seen")

	require.NoError(t, ps.TouchLastSeen(ctx, before.ID))

	after, err := ps.FindByKeyHash(ctx, auth.HashKey(key))
	require.NoError(t, err)
	require.NotNil(t, after.LastSeenAt)

	// Throttled: the column answers "is this key still in use", which a
	// minute's resolution answers as well as a write per request would.
	require.NoError(t, ps.TouchLastSeen(ctx, before.ID))
	again, err := ps.FindByKeyHash(ctx, auth.HashKey(key))
	require.NoError(t, err)
	assert.Equal(t, *after.LastSeenAt, *again.LastSeenAt)
}

// A key hash identifies exactly one principal; two rows sharing one would make
// "who is calling" ambiguous, which is the whole thing the table exists to fix.
func TestKeyHashIsUnique(t *testing.T) {
	ctx := context.Background()
	subject := rbacSubject(t, "dup")
	key := issueKey(t, subject, "viewer")

	_, err := store.NewPrincipalStore(testPool).Insert(ctx, model.PrincipalCreate{
		Subject:     subject + "-other",
		Kind:        model.PrincipalKindAPIKey,
		DisplayName: "impostor",
		KeyHash:     auth.HashKey(key),
	})
	assert.Error(t, err)
}

// The table's own CHECK, not the API's: a user principal has no key, and a key
// principal must have one.
func TestPrincipalKindAndKeyMustAgree(t *testing.T) {
	ctx := context.Background()

	_, err := testPool.Exec(ctx,
		`INSERT INTO principals (subject, kind) VALUES ('ci:keyless', 'api_key')`)
	assert.Error(t, err, "an api_key principal with no key authenticates nothing")

	_, err = testPool.Exec(ctx,
		`INSERT INTO principals (subject, kind, key_hash) VALUES ('user:with-key', 'user', 'abc')`)
	assert.Error(t, err, "a human's principal is not a bearer token")
}

func TestRoleBindingRejectsUnknownRoles(t *testing.T) {
	ctx := context.Background()
	subject := rbacSubject(t, "badrole")
	issueKey(t, subject, "viewer")

	_, err := testPool.Exec(ctx, `
		INSERT INTO role_bindings (principal_id, role)
		SELECT id, 'superuser' FROM principals WHERE subject = $1
	`, subject)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Tests: bootstrap
// ---------------------------------------------------------------------------

func TestBootstrapAdminIsSeededOnceAndCanUseTheAPI(t *testing.T) {
	ctx := context.Background()
	ts := setupRBACServer(t, nil)
	subject := "user:bootstrap-" + strings.ToLower(t.Name())
	ps := store.NewPrincipalStore(testPool)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM principals WHERE subject = $1`, subject)
		assert.NoError(t, err)
	})

	key, err := auth.BootstrapAdmin(ctx, ps, subject, quiet)
	require.NoError(t, err)
	require.NotEmpty(t, key)

	// The one thing the bootstrap admin exists to do: reach the endpoints that
	// issuing further credentials will sit behind.
	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/inheritance", "Bearer "+key, model.InheritanceCreate{
		Repo:          "org/rbac_bootstrap",
		SourceRCSRef:  "src_boot",
		TargetRCSRef:  "tgt_boot",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "bootstrap admin is an admin",
		CreatedBy:     subject,
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Restarting must not mint a second key, and must leave the first working.
	again, err := auth.BootstrapAdmin(ctx, ps, subject, quiet)
	require.NoError(t, err)
	assert.Empty(t, again)

	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence", "Bearer "+key, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
