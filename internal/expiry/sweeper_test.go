package expiry

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDeleter stands in for a store. No database: what is worth testing here is
// the loop and what it does when a target fails, neither of which is a fact
// about Postgres.
type fakeDeleter struct {
	mu      sync.Mutex
	calls   int
	deleted int64
	err     error
}

func (f *fakeDeleter) DeleteExpired(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.deleted, f.err
}

func (f *fakeDeleter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestSweepsEveryTarget(t *testing.T) {
	sessions := &fakeDeleter{deleted: 3}
	requests := &fakeDeleter{deleted: 1}

	New(quiet(), map[string]Deleter{"sessions": sessions, "saml_requests": requests}).
		RunOnce(context.Background())

	assert.Equal(t, 1, sessions.count())
	assert.Equal(t, 1, requests.count())
}

func TestOneFailingTargetDoesNotStopTheOthers(t *testing.T) {
	// Independent tables. A database that will not answer for one is not a
	// reason to skip a second that might.
	broken := &fakeDeleter{err: errors.New("connection refused")}
	working := &fakeDeleter{deleted: 7}

	New(quiet(), map[string]Deleter{"broken": broken, "working": working}).
		RunOnce(context.Background())

	assert.Equal(t, 1, broken.count())
	assert.Equal(t, 1, working.count(), "the working target is still swept")
}

func TestSweepsImmediatelyRatherThanAfterTheFirstInterval(t *testing.T) {
	// A process that restarts more often than the interval would otherwise
	// never sweep at all.
	target := &fakeDeleter{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		New(quiet(), map[string]Deleter{"t": target}).WithInterval(time.Hour).Start(ctx)
		close(done)
	}()

	assert.Eventually(t, func() bool { return target.count() >= 1 }, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return when its context was cancelled")
	}
}

func TestKeepsSweepingOnTheInterval(t *testing.T) {
	target := &fakeDeleter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go New(quiet(), map[string]Deleter{"t": target}).WithInterval(20 * time.Millisecond).Start(ctx)

	require.Eventually(t, func() bool { return target.count() >= 3 }, 2*time.Second, 10*time.Millisecond,
		"expected repeated sweeps, not just the one at startup")
}

func TestTargetsAreSweptInAStableOrder(t *testing.T) {
	// So two runs' logs can be compared.
	s := New(quiet(), map[string]Deleter{"zebra": &fakeDeleter{}, "alpha": &fakeDeleter{}, "middle": &fakeDeleter{}})
	assert.Equal(t, []string{"alpha", "middle", "zebra"}, s.names())
}

func TestNoTargetsIsNotAnError(t *testing.T) {
	assert.NotPanics(t, func() { New(quiet(), nil).RunOnce(context.Background()) })
}
