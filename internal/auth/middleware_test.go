package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/config"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r.Context()); ok {
			w.Header().Set("X-Auth-Subject", p.Subject)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// serve runs a request through Authenticate, and through Require when perm is
// non-empty, which is the shape every route in server.go has.
func serve(t *testing.T, authenticator Authenticator, perm Permission, method, token string) *httptest.ResponseRecorder {
	t.Helper()
	var handler http.Handler = okHandler()
	if perm != "" {
		handler = Require(perm)(handler)
	}
	handler = Authenticate(authenticator)(handler)

	req := httptest.NewRequest(method, "/api/v1/evidence", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Roles and permissions
// ---------------------------------------------------------------------------

func TestRolePermissions(t *testing.T) {
	all := []Permission{
		PermEvidenceRead, PermEvidenceWrite, PermAnalyticsRead,
		PermBlobRead, PermBlobWrite, PermInheritanceRead, PermInheritanceWrite,
		PermSourceAny, PermPrincipalAdmin, PermRetentionAdmin,
	}

	granted := map[Role][]Permission{
		RoleViewer: {PermEvidenceRead, PermAnalyticsRead, PermBlobRead, PermInheritanceRead},
		RoleContributor: {
			PermEvidenceRead, PermAnalyticsRead, PermBlobRead, PermInheritanceRead,
			PermEvidenceWrite, PermBlobWrite,
		},
		RoleCI: {
			PermEvidenceRead, PermAnalyticsRead, PermBlobRead, PermInheritanceRead,
			PermEvidenceWrite, PermBlobWrite, PermSourceAny,
		},
		RoleAdmin: {
			PermEvidenceRead, PermAnalyticsRead, PermBlobRead, PermInheritanceRead,
			PermEvidenceWrite, PermBlobWrite,
			PermInheritanceWrite, PermPrincipalAdmin, PermRetentionAdmin,
		},
	}

	for role, perms := range granted {
		want := make(map[Permission]bool, len(perms))
		for _, p := range perms {
			want[p] = true
		}
		for _, p := range all {
			assert.Equal(t, want[p], role.Grants(p), "role %s permission %s", role, p)
		}
	}
}

// The store's whole reason for having ci and admin as separate roles is that
// neither one contains the other.
func TestAdminDoesNotSubsumeCI(t *testing.T) {
	assert.False(t, RoleAdmin.Grants(PermSourceAny))
	assert.False(t, RoleCI.Grants(PermInheritanceWrite))

	both := NewPrincipal("user:root", KindUser, "", RoleAdmin, RoleCI)
	assert.True(t, both.Can(PermSourceAny))
	assert.True(t, both.Can(PermInheritanceWrite))
}

func TestParseRole(t *testing.T) {
	for _, name := range []string{"viewer", "contributor", "ci", "admin"} {
		r, ok := ParseRole(name)
		assert.True(t, ok, "role %q should parse", name)
		assert.Equal(t, Role(name), r)
	}
	_, ok := ParseRole("superuser")
	assert.False(t, ok)
}

// An unknown role grants nothing, so a stale binding cannot widen access.
func TestUnknownRoleGrantsNothing(t *testing.T) {
	p := NewPrincipal("user:ghost", KindUser, "", Role("superuser"))
	assert.False(t, p.Can(PermEvidenceRead))
	assert.True(t, p.HasRole(Role("superuser")))
}

// ---------------------------------------------------------------------------
// Authenticate
// ---------------------------------------------------------------------------

func TestAuthenticateNoKeysPassesThrough(t *testing.T) {
	// With no keys configured, both reads and writes pass through
	// unidentified. This is the default local-development posture.
	authenticator := NewStaticKeyAuthenticator(nil)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := serve(t, authenticator, PermInheritanceWrite, method, "")
		assert.Equal(t, http.StatusOK, rec.Code, "method %s should pass through", method)
		assert.Empty(t, rec.Header().Get("X-Auth-Subject"))
	}
}

func TestAuthenticateMissingHeader(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{{Key: "secret"}})
	rec := serve(t, authenticator, PermEvidenceRead, http.MethodGet, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticateInvalidKey(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{{Key: "secret"}})
	rec := serve(t, authenticator, PermEvidenceRead, http.MethodGet, "wrong-key")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticateNonBearerScheme(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{{Key: "secret"}})

	handler := Authenticate(authenticator)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticateBearerCaseInsensitive(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{{Key: "secret"}})

	handler := Authenticate(authenticator)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// Authenticate answers who, not whether: a key with no permission for the
// route still reaches Require, which is what makes the 401/403 split possible.
func TestAuthenticateDoesNotAuthorize(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{{Key: "ro-key", ReadOnly: true}})

	handler := Authenticate(authenticator)(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evidence", nil)
	req.Header.Set("Authorization", "Bearer ro-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "apikey:ro-1", rec.Header().Get("X-Auth-Subject"))
}

// ---------------------------------------------------------------------------
// Static key compatibility mapping
// ---------------------------------------------------------------------------

func TestStaticKeyRoleMapping(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{
		{Key: "rw-key", ReadOnly: false},
		{Key: "ro-key", ReadOnly: true},
	})

	tests := []struct {
		token string
		roles []Role
	}{
		{"rw-key", []Role{RoleCI, RoleAdmin}},
		{"ro-key", []Role{RoleViewer}},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		p, err := authenticator.Authenticate(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, tc.roles, p.Roles, "token %s", tc.token)
		assert.Equal(t, KindAPIKey, p.Kind)
	}
}

// Every permission an rw key could exercise before this change it still can,
// including posting an inheritance declaration.
func TestStaticRWKeyKeepsEveryEndpoint(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{{Key: "rw-key"}})

	for _, perm := range []Permission{
		PermEvidenceRead, PermEvidenceWrite, PermAnalyticsRead,
		PermBlobRead, PermBlobWrite, PermInheritanceRead, PermInheritanceWrite,
	} {
		rec := serve(t, authenticator, perm, http.MethodPost, "rw-key")
		assert.Equal(t, http.StatusOK, rec.Code, "rw key should hold %s", perm)
	}
}

func TestStaticROKeyIsViewer(t *testing.T) {
	authenticator := NewStaticKeyAuthenticator([]config.APIKey{{Key: "ro-key", ReadOnly: true}})

	rec := serve(t, authenticator, PermEvidenceRead, http.MethodGet, "ro-key")
	assert.Equal(t, http.StatusOK, rec.Code)

	for _, perm := range []Permission{PermEvidenceWrite, PermBlobWrite, PermInheritanceWrite} {
		rec := serve(t, authenticator, perm, http.MethodPost, "ro-key")
		assert.Equal(t, http.StatusForbidden, rec.Code, "ro key should not hold %s", perm)
	}
}

// ---------------------------------------------------------------------------
// Require
// ---------------------------------------------------------------------------

func TestRequireUnauthenticatedIs401(t *testing.T) {
	// No principal and no pass-through marker: Require fails closed rather
	// than assuming an absent Authenticate meant an open route.
	handler := Require(PermEvidenceRead)(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/evidence", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireUnauthorizedIs403(t *testing.T) {
	handler := Require(PermInheritanceWrite)(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inheritance", nil)
	req = req.WithContext(WithPrincipal(req.Context(),
		NewPrincipal("user:alice", KindUser, "Alice", RoleContributor)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireAllowsPermittedPrincipal(t *testing.T) {
	handler := Require(PermInheritanceWrite)(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inheritance", nil)
	req = req.WithContext(WithPrincipal(req.Context(),
		NewPrincipal("user:root", KindUser, "Root", RoleAdmin)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ---------------------------------------------------------------------------
// Chain
// ---------------------------------------------------------------------------

type stubAuthenticator struct {
	principal *Principal
	err       error
	calls     *int
}

func (s stubAuthenticator) Authenticate(context.Context, *http.Request) (*Principal, error) {
	*s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.principal, nil
}

func TestChainSkipsAuthenticatorsWithNothingToRead(t *testing.T) {
	var firstCalls, secondCalls int
	want := NewPrincipal("user:alice", KindUser, "Alice", RoleViewer)
	chain := Chain{
		stubAuthenticator{err: ErrNoCredentials, calls: &firstCalls},
		stubAuthenticator{principal: want, calls: &secondCalls},
	}

	got, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 1, firstCalls)
	assert.Equal(t, 1, secondCalls)
}

// A presented-but-wrong credential is an answer, not an absence: the chain
// stops rather than letting a later scheme accept the same request.
func TestChainStopsOnInvalidCredentials(t *testing.T) {
	var firstCalls, secondCalls int
	chain := Chain{
		stubAuthenticator{err: ErrInvalidCredentials, calls: &firstCalls},
		stubAuthenticator{principal: NewPrincipal("user:alice", KindUser, "", RoleAdmin), calls: &secondCalls},
	}

	_, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.Equal(t, 0, secondCalls)
}

func TestChainIsDisabledOnlyWhenEveryMemberIs(t *testing.T) {
	var calls int
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	allOff := Chain{
		stubAuthenticator{err: ErrAuthDisabled, calls: &calls},
		stubAuthenticator{err: ErrAuthDisabled, calls: &calls},
	}
	_, err := allOff.Authenticate(context.Background(), req)
	assert.ErrorIs(t, err, ErrAuthDisabled)

	// One configured scheme is enough to close the door on an anonymous
	// request, even though the other scheme is switched off.
	oneOn := Chain{
		stubAuthenticator{err: ErrAuthDisabled, calls: &calls},
		stubAuthenticator{err: ErrNoCredentials, calls: &calls},
	}
	_, err = oneOn.Authenticate(context.Background(), req)
	assert.ErrorIs(t, err, ErrNoCredentials)
}
