package auth

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

// fakeRegistry is the writing half of the store, with the one behaviour that
// matters to bootstrapping: a taken subject is reported, not raised.
type fakeRegistry struct {
	mu        sync.Mutex
	bySubject map[string]*model.Principal
	// stored is what actually reached the table, so a test can assert the
	// plaintext key did not.
	stored     map[string]model.PrincipalCreate
	findErr    error
	insertErr  error
	insertHook func()
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		bySubject: map[string]*model.Principal{},
		stored:    map[string]model.PrincipalCreate{},
	}
}

func (f *fakeRegistry) FindBySubject(_ context.Context, subject string) (*model.Principal, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bySubject[subject], nil
}

func (f *fakeRegistry) Insert(_ context.Context, in model.PrincipalCreate) (*model.Principal, error) {
	if f.insertErr != nil {
		return nil, f.insertErr
	}
	if f.insertHook != nil {
		f.insertHook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, taken := f.bySubject[in.Subject]; taken {
		return nil, nil
	}
	p := &model.Principal{
		ID:          uuid.New(),
		Subject:     in.Subject,
		Kind:        in.Kind,
		DisplayName: in.DisplayName,
		Roles:       in.Roles,
	}
	f.bySubject[in.Subject] = p
	f.stored[in.Subject] = in
	return p, nil
}

func TestBootstrapAdminSeedsAnAdministratorAndReturnsItsKeyOnce(t *testing.T) {
	registry := newFakeRegistry()

	key, err := BootstrapAdmin(context.Background(), registry, "user:root", quietLogger())
	require.NoError(t, err)
	require.NotEmpty(t, key, "the operator needs a way in")
	assert.True(t, LooksLikeKey(key))

	created := registry.bySubject["user:root"]
	require.NotNil(t, created)
	assert.Equal(t, model.PrincipalKindAPIKey, created.Kind)
	// Identity and role arrive together, so there is no window in which the
	// only key anyone was told about can do nothing.
	assert.Equal(t, []string{string(RoleAdmin)}, created.Roles)
	assert.Nil(t, registry.stored["user:root"].GrantedBy,
		"the server grants on its own authority; there is nobody to credit")

	// Only the digest is stored. This is the single moment the key is readable.
	assert.Equal(t, HashKey(key), registry.stored["user:root"].KeyHash)
	assert.NotContains(t, registry.stored["user:root"].KeyHash, key)

	// Every subsequent start finds it and says nothing.
	again, err := BootstrapAdmin(context.Background(), registry, "user:root", quietLogger())
	require.NoError(t, err)
	assert.Empty(t, again, "a second start must not mint a second key")
}

// Two replicas starting together: the one that loses the insert has nothing to
// report, and must not tell an operator about a key that was never stored.
func TestBootstrapAdminIsSilentWhenAnotherReplicaWinsTheRace(t *testing.T) {
	registry := newFakeRegistry()
	registry.insertHook = func() {
		registry.mu.Lock()
		defer registry.mu.Unlock()
		registry.bySubject["user:root"] = &model.Principal{ID: uuid.New(), Subject: "user:root"}
	}

	key, err := BootstrapAdmin(context.Background(), registry, "user:root", quietLogger())
	require.NoError(t, err)
	assert.Empty(t, key)
	assert.Empty(t, registry.stored, "the winner's row stands; the loser writes nothing")
}

func TestBootstrapAdminReportsFailures(t *testing.T) {
	for name, registry := range map[string]*fakeRegistry{
		"lookup": func() *fakeRegistry {
			r := newFakeRegistry()
			r.findErr = errors.New("connection refused")
			return r
		}(),
		"insert": func() *fakeRegistry {
			r := newFakeRegistry()
			r.insertErr = errors.New("deadlock detected")
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			key, err := BootstrapAdmin(context.Background(), registry, "user:root", quietLogger())
			assert.Error(t, err)
			assert.Empty(t, key)
		})
	}
}
