// Deleting rows that have already stopped meaning anything.
//
// A login session and a half-finished SAML handshake both carry an expiry, and
// both are checked against it on every use — so an expired row is already inert
// and nothing goes wrong by leaving it. Nothing except the table growing for as
// long as the deployment lives.
//
// This runs separately from the retention worker, and unconditionally, because
// the two answer different questions. Whether to delete old *evidence* is a
// policy an operator decides and may reasonably answer "never"; whether to
// delete a spent token is not a policy at all. Hanging this off the retention
// worker would mean a deployment with no retention configured — the default —
// still leaked.
package expiry

import (
	"context"
	"log/slog"
	"time"
)

// Interval between sweeps.
//
// Not configurable, deliberately. Sessions last hours and SAML requests
// minutes, so anything under a day is equivalent, and an hourly DELETE against
// an indexed timestamp is not a cost worth a knob to tune. One less environment
// variable is worth more than the flexibility.
const Interval = time.Hour

// Deleter is one table's worth of expiry. Both SessionStore and
// SAMLRequestStore already satisfy it.
type Deleter interface {
	DeleteExpired(ctx context.Context) (int64, error)
}

// Sweeper deletes what has expired, on a ticker, until its context is done.
type Sweeper struct {
	targets  map[string]Deleter
	interval time.Duration
	logger   *slog.Logger
}

// New builds a sweeper over named targets. The names appear in the logs, which
// is the only reason they exist: "swept 400 sessions" is actionable and "swept
// 400 rows" is not.
func New(logger *slog.Logger, targets map[string]Deleter) *Sweeper {
	return &Sweeper{targets: targets, interval: Interval, logger: logger}
}

// WithInterval overrides the sweep interval. For tests; nothing in the server
// calls it.
func (s *Sweeper) WithInterval(d time.Duration) *Sweeper {
	s.interval = d
	return s
}

// Start sweeps immediately and then on the interval. Blocks until ctx is done.
func (s *Sweeper) Start(ctx context.Context) {
	s.logger.Info("expiry sweeper started", "interval", s.interval, "targets", len(s.targets))
	s.RunOnce(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("expiry sweeper stopped")
			return
		case <-ticker.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce sweeps every target once.
//
// One failing target does not stop the others: they are independent tables, and
// a database that will not answer for one is not a reason to skip a second that
// might. Errors are logged rather than returned, because there is no caller in
// a position to do anything else with them.
func (s *Sweeper) RunOnce(ctx context.Context) {
	for _, name := range s.names() {
		deleted, err := s.targets[name].DeleteExpired(ctx)
		switch {
		case err != nil:
			s.logger.Error("expiry sweep failed", "target", name, "error", err)
		case deleted > 0:
			// Silent when there was nothing to do, which is most sweeps. A log
			// line every hour saying nothing happened trains people to skip it.
			s.logger.Info("expiry sweep completed", "target", name, "deleted", deleted)
		}
	}
}

// names returns the targets in a stable order, so the logs of two runs can be
// compared and a test can rely on what it reads.
func (s *Sweeper) names() []string {
	names := make([]string, 0, len(s.targets))
	for name := range s.targets {
		names = append(names, name)
	}
	// Small and fixed — two entries in the server — so an insertion sort's
	// worth of code is more than enough and pulls in nothing.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}
