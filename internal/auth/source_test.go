package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxWith(p *Principal) context.Context {
	return WithPrincipal(context.Background(), p)
}

// The local-development posture: nothing is configured, so there is no identity
// to pin a source to and the field means what it always meant.
func TestBindSourceLeavesAnonymousWritesAlone(t *testing.T) {
	for _, source := range []string{"", "somebody-else", "https://ci/build/1"} {
		got, err := BindSource(context.Background(), source)
		require.NoError(t, err)
		assert.Equal(t, source, got)
	}
}

// A build robot's useful attribution is the build URL, not the robot. That is
// the whole reason ci is a role and not a flag.
func TestBindSourceLetsCIWriteAnySource(t *testing.T) {
	ctx := ctxWith(NewPrincipal("ci:nightly", KindAPIKey, "Nightly", RoleCI))

	got, err := BindSource(ctx, "https://ci.example.com/build/1234")
	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com/build/1234", got)

	// Including one that happens to be a person's name: that is exactly the
	// backfill the permission exists to allow.
	got, err = BindSource(ctx, "user:alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "user:alice@example.com", got)
}

func TestBindSourcePinsAHumanToTheirOwnName(t *testing.T) {
	ctx := ctxWith(NewPrincipal("user:alice@example.com", KindUser, "Alice", RoleContributor))

	got, err := BindSource(ctx, "user:alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "user:alice@example.com", got)

	_, err = BindSource(ctx, "user:bob@example.com")
	assert.ErrorIs(t, err, ErrSourceNotOwned)
	// The message names what was expected, which is the caller's own subject
	// and so gives nothing away.
	assert.Contains(t, err.Error(), "user:alice@example.com")
}

// Saying nothing is not a claim about anybody, so the server makes the true
// one rather than refusing.
func TestBindSourceFillsInAnEmptySource(t *testing.T) {
	ctx := ctxWith(NewPrincipal("user:alice@example.com", KindUser, "Alice", RoleContributor))

	got, err := BindSource(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "user:alice@example.com", got)
}

// admin deliberately does not subsume ci: writing history in another party's
// name is always a deliberate grant, even for an administrator.
func TestBindSourcePinsAnAdminWhoIsNotAlsoCI(t *testing.T) {
	admin := ctxWith(NewPrincipal("user:root", KindUser, "Root", RoleAdmin))
	_, err := BindSource(admin, "ci:nightly")
	assert.ErrorIs(t, err, ErrSourceNotOwned)

	// Holding both is how an administrator backfills somebody else's evidence.
	both := ctxWith(NewPrincipal("user:root", KindUser, "Root", RoleAdmin, RoleCI))
	got, err := BindSource(both, "ci:nightly")
	require.NoError(t, err)
	assert.Equal(t, "ci:nightly", got)
}

// Posture 2 is untouched by any of this: an env rw key is ci, and an env ro key
// cannot write evidence at all.
func TestBindSourceLeavesConfiguredRWKeysAlone(t *testing.T) {
	// The rw mapping itself is pinned in TestStaticKeyRoleMapping; what matters
	// here is that carrying ci is what keeps those keys writing as they did.
	ctx := ctxWith(NewPrincipal("apikey:rw-1", KindAPIKey, "configured rw API key", RoleCI, RoleAdmin))
	got, err := BindSource(ctx, "https://ci.example.com/build/9")
	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com/build/9", got)
}
