package tests

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/expiry"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
)

// The sweeper against the real tables (issue #121).
//
// Each store already has its own DeleteExpired test. What is worth proving here
// is the join: that the stores satisfy the sweeper's interface, that one pass
// clears both tables at once, and — the part that actually regressed — that
// anything is calling them at all.

func TestSweeperClearsBothTablesAndSparesTheLiveOnes(t *testing.T) {
	ctx := context.Background()
	sessions := store.NewSessionStore(testPool)
	requests := store.NewSAMLRequestStore(testPool)

	principal, err := store.NewPrincipalStore(testPool).Insert(ctx, model.PrincipalCreate{
		Subject:     "user:sweep@example.com",
		Kind:        model.PrincipalKindAPIKey,
		DisplayName: "a principal to hang sessions off",
		KeyHash:     auth.HashKey("a-key-for-the-sweep-test"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := testPool.Exec(ctx, `DELETE FROM principals WHERE id = $1`, principal.ID)
		assert.NoError(t, err)
	})

	_, err = sessions.Create(ctx, principal.ID, auth.HashKey("expired-session"), time.Now().Add(-time.Hour), "test", "")
	require.NoError(t, err)
	_, err = sessions.Create(ctx, principal.ID, auth.HashKey("live-session"), time.Now().Add(time.Hour), "test", "")
	require.NoError(t, err)

	require.NoError(t, requests.Remember(ctx, "expired-saml-request", time.Now().Add(-time.Minute)))
	require.NoError(t, requests.Remember(ctx, "live-saml-request", time.Now().Add(time.Minute)))
	t.Cleanup(func() {
		_, err := testPool.Exec(ctx, `DELETE FROM saml_requests WHERE id = ANY($1)`,
			[]string{"expired-saml-request", "live-saml-request"})
		assert.NoError(t, err)
	})

	expiry.New(slog.New(slog.DiscardHandler), map[string]expiry.Deleter{
		"sessions":      sessions,
		"saml_requests": requests,
	}).RunOnce(ctx)

	assert.Equal(t, 1, countSessions(t, principal.ID),
		"the expired session should be gone and the live one left alone")

	pending, err := requests.Pending(ctx)
	require.NoError(t, err)
	assert.NotContains(t, pending, "expired-saml-request")
	assert.Contains(t, pending, "live-saml-request",
		"a handshake still in flight is not rubbish to collect")
}

// A sweep with nothing to delete must not report or fail. Most sweeps are this
// one, and an hourly error would be worse than the rows it was cleaning up.
func TestSweepingAnAlreadyCleanTableIsQuiet(t *testing.T) {
	ctx := context.Background()
	sweeper := expiry.New(slog.New(slog.DiscardHandler), map[string]expiry.Deleter{
		"sessions":      store.NewSessionStore(testPool),
		"saml_requests": store.NewSAMLRequestStore(testPool),
	})

	assert.NotPanics(t, func() {
		sweeper.RunOnce(ctx)
		sweeper.RunOnce(ctx)
	})
}

func countSessions(t *testing.T, principalID any) int {
	t.Helper()
	var n int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM sessions WHERE principal_id = $1`, principalID).Scan(&n))
	return n
}
