package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

// fakeLookup stands in for the principals table. The store's own SQL is
// exercised against a real Postgres in tests/rbac_integration_test.go; what is
// worth isolating here is the authenticator's reading of what comes back.
type fakeLookup struct {
	mu        sync.Mutex
	byHash    map[string]*model.Principal
	err       error
	queries   []string
	touches   []uuid.UUID
	touchErr  error
	touchWait chan struct{}
}

func newFakeLookup() *fakeLookup {
	return &fakeLookup{byHash: map[string]*model.Principal{}, touchWait: make(chan struct{}, 16)}
}

func (f *fakeLookup) add(token string, p *model.Principal) *model.Principal {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	f.byHash[HashKey(token)] = p
	return p
}

func (f *fakeLookup) FindByKeyHash(_ context.Context, keyHash string) (*model.Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, keyHash)
	if f.err != nil {
		return nil, f.err
	}
	return f.byHash[keyHash], nil
}

func (f *fakeLookup) TouchLastSeen(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	f.touches = append(f.touches, id)
	err := f.touchErr
	f.mu.Unlock()
	select {
	case f.touchWait <- struct{}{}:
	default:
	}
	return err
}

func (f *fakeLookup) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

func (f *fakeLookup) touchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.touches)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func bearerRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func mintKey(t *testing.T) string {
	t.Helper()
	key, err := GenerateKey()
	require.NoError(t, err)
	return key
}

func TestDBKeyAuthenticatesAndFlattensRoles(t *testing.T) {
	lookup := newFakeLookup()
	key := mintKey(t)
	stored := lookup.add(key, &model.Principal{
		Subject:     "ci:nightly",
		Kind:        model.PrincipalKindAPIKey,
		DisplayName: "Nightly build",
		Roles:       []string{"ci"},
	})

	p, err := NewDBKeyAuthenticator(lookup, quietLogger()).Authenticate(context.Background(), bearerRequest(key))
	require.NoError(t, err)

	assert.Equal(t, stored.ID, p.ID, "the principal should carry its row identity")
	assert.Equal(t, "ci:nightly", p.Subject)
	assert.Equal(t, KindAPIKey, p.Kind)
	assert.True(t, p.Can(PermEvidenceWrite))
	assert.True(t, p.Can(PermSourceAny), "ci is what source:any comes from")
	// The finer split phase 2 exists to deliver: a ci key is not an admin, which
	// an env-var rw key necessarily is.
	assert.False(t, p.Can(PermInheritanceWrite))
	assert.False(t, p.Can(PermPrincipalAdmin))
}

func TestDBKeyGrantsTheUnionOfSeveralRoles(t *testing.T) {
	lookup := newFakeLookup()
	key := mintKey(t)
	lookup.add(key, &model.Principal{
		Subject: "user:root",
		Kind:    model.PrincipalKindUser,
		Roles:   []string{"ci", "admin"},
	})

	p, err := NewDBKeyAuthenticator(lookup, quietLogger()).Authenticate(context.Background(), bearerRequest(key))
	require.NoError(t, err)
	assert.Equal(t, KindUser, p.Kind)
	assert.True(t, p.Can(PermSourceAny), "from ci")
	assert.True(t, p.Can(PermInheritanceWrite), "from admin")
}

// A binding can outlive the constant it names — a downgrade, a half-finished
// rollout. The safe reading is that it grants nothing, not that the request
// fails, and certainly not that it grants everything.
func TestDBKeyIgnoresRolesThisBinaryDoesNotDefine(t *testing.T) {
	lookup := newFakeLookup()
	key := mintKey(t)
	lookup.add(key, &model.Principal{
		Subject: "ci:from-the-future",
		Kind:    model.PrincipalKindAPIKey,
		Roles:   []string{"superuser", "viewer"},
	})

	p, err := NewDBKeyAuthenticator(lookup, quietLogger()).Authenticate(context.Background(), bearerRequest(key))
	require.NoError(t, err)
	assert.Equal(t, []Role{RoleViewer}, p.Roles)
	assert.True(t, p.Can(PermEvidenceRead))
	assert.False(t, p.Can(PermEvidenceWrite))
}

func TestDBKeyPrincipalWithNoBindingsCanDoNothing(t *testing.T) {
	lookup := newFakeLookup()
	key := mintKey(t)
	lookup.add(key, &model.Principal{Subject: "ci:unbound", Kind: model.PrincipalKindAPIKey})

	p, err := NewDBKeyAuthenticator(lookup, quietLogger()).Authenticate(context.Background(), bearerRequest(key))
	require.NoError(t, err)
	// Authenticated is not authorized: the caller is known and holds nothing.
	assert.False(t, p.Can(PermEvidenceRead))
}

func TestDBKeyRejectsUnknownKey(t *testing.T) {
	lookup := newFakeLookup()
	_, err := NewDBKeyAuthenticator(lookup, quietLogger()).
		Authenticate(context.Background(), bearerRequest(mintKey(t)))
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

// Revocation is the point of the table. It has to bite on the next request,
// not the next redeploy.
func TestDBKeyRejectsDisabledPrincipal(t *testing.T) {
	lookup := newFakeLookup()
	key := mintKey(t)
	disabled := time.Now().Add(-time.Hour)
	lookup.add(key, &model.Principal{
		Subject:    "ci:revoked",
		Kind:       model.PrincipalKindAPIKey,
		Roles:      []string{"admin"},
		DisabledAt: &disabled,
	})

	_, err := NewDBKeyAuthenticator(lookup, quietLogger()).Authenticate(context.Background(), bearerRequest(key))
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.Zero(t, lookup.touchCount(), "a rejected key is not a sighting")
}

func TestDBKeyReportsNoCredentialsWithoutABearerToken(t *testing.T) {
	a := NewDBKeyAuthenticator(newFakeLookup(), quietLogger())
	_, err := a.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.ErrorIs(t, err, ErrNoCredentials)
}

// A legacy env-var key reaches the same header. It is not this scheme's, so it
// costs no query and does not count as a wrong key — the static authenticator
// alongside is the one that knows it.
func TestDBKeySkipsCredentialsThatAreNotItsOwn(t *testing.T) {
	lookup := newFakeLookup()
	a := NewDBKeyAuthenticator(lookup, quietLogger())

	_, err := a.Authenticate(context.Background(), bearerRequest("legacy-rw-secret"))
	assert.ErrorIs(t, err, ErrNoCredentials)
	assert.Zero(t, lookup.queryCount(), "an unrecognisable token should not reach the database")
}

// A database outage is not a wrong password. Reporting it as one sends every
// CI pipeline in the building rotating credentials that were fine.
func TestDBKeyDistinguishesAnOutageFromABadKey(t *testing.T) {
	lookup := newFakeLookup()
	lookup.err = errors.New("connection refused")

	_, err := NewDBKeyAuthenticator(lookup, quietLogger()).
		Authenticate(context.Background(), bearerRequest(mintKey(t)))
	assert.ErrorIs(t, err, ErrAuthUnavailable)
	assert.NotErrorIs(t, err, ErrInvalidCredentials)
}

func TestDBKeyRecordsTheSightingOnceInAWhile(t *testing.T) {
	lookup := newFakeLookup()
	key := mintKey(t)
	lookup.add(key, &model.Principal{Subject: "ci:busy", Kind: model.PrincipalKindAPIKey, Roles: []string{"ci"}})
	a := NewDBKeyAuthenticator(lookup, quietLogger())

	for range 20 {
		_, err := a.Authenticate(context.Background(), bearerRequest(key))
		require.NoError(t, err)
	}

	// The write is detached from the request, so wait for the one that was let
	// through rather than racing it.
	select {
	case <-lookup.touchWait:
	case <-time.After(2 * time.Second):
		t.Fatal("expected last_seen_at to be recorded at least once")
	}
	assert.LessOrEqual(t, lookup.touchCount(), 1,
		"a write per request would cost more than the column is worth")
}

// Bookkeeping must never be able to fail a request that authenticated.
func TestDBKeyAuthenticatesEvenWhenTheSightingCannotBeRecorded(t *testing.T) {
	lookup := newFakeLookup()
	lookup.touchErr = errors.New("read-only replica")
	key := mintKey(t)
	lookup.add(key, &model.Principal{Subject: "ci:one", Kind: model.PrincipalKindAPIKey, Roles: []string{"viewer"}})

	p, err := NewDBKeyAuthenticator(lookup, quietLogger()).Authenticate(context.Background(), bearerRequest(key))
	require.NoError(t, err)
	assert.Equal(t, "ci:one", p.Subject)
}

// The request's context is cancelled the moment the response is written, which
// is typically before the detached write has run.
func TestDBKeyRecordsTheSightingAfterTheRequestIsOver(t *testing.T) {
	lookup := newFakeLookup()
	key := mintKey(t)
	lookup.add(key, &model.Principal{Subject: "ci:brief", Kind: model.PrincipalKindAPIKey, Roles: []string{"viewer"}})

	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDBKeyAuthenticator(lookup, quietLogger()).Authenticate(ctx, bearerRequest(key))
	require.NoError(t, err)
	cancel()

	select {
	case <-lookup.touchWait:
	case <-time.After(2 * time.Second):
		t.Fatal("the sighting should outlive the request that produced it")
	}
}

func TestParseKindTreatsTheUnknownAsAMachine(t *testing.T) {
	assert.Equal(t, KindUser, ParseKind("user"))
	assert.Equal(t, KindAPIKey, ParseKind("api_key"))
	assert.Equal(t, KindAPIKey, ParseKind("service-account"), "an unknown caller is not a person")
	assert.Equal(t, KindAPIKey, ParseKind(""))
}
