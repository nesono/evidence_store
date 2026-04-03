package retention

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/store"
)

const defaultBatchSize = 1000

// Worker runs retention rules on a schedule.
type Worker struct {
	evaluator *Evaluator
	evidence  *store.EvidenceStore
	inherit   *store.InheritanceStore
	interval  time.Duration
	logger    *slog.Logger
}

// NewWorker creates a new retention worker.
func NewWorker(cfg *Config, evidence *store.EvidenceStore, inherit *store.InheritanceStore, logger *slog.Logger) (*Worker, error) {
	eval, err := NewEvaluator(cfg)
	if err != nil {
		return nil, err
	}
	return &Worker{
		evaluator: eval,
		evidence:  evidence,
		inherit:   inherit,
		interval:  cfg.Interval,
		logger:    logger,
	}, nil
}

// Start runs the retention worker on a ticker. Blocks until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	w.logger.Info("retention worker started", "interval", w.interval)

	// Run once immediately on start.
	deleted, err := w.RunOnce(ctx)
	if err != nil {
		w.logger.Error("retention run failed", "error", err)
	} else {
		w.logger.Info("retention run completed", "deleted", deleted)
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("retention worker stopped")
			return
		case <-ticker.C:
			deleted, err := w.RunOnce(ctx)
			if err != nil {
				w.logger.Error("retention run failed", "error", err)
			} else {
				w.logger.Info("retention run completed", "deleted", deleted)
			}
		}
	}
}

// RunOnce executes a single retention pass. Returns the total number of deleted records.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	// Build the inheritance exemption set.
	inheritedRefs, err := w.inherit.AllSourceRefs(ctx)
	if err != nil {
		return 0, err
	}

	now := time.Now()
	var totalDeleted int

	err = w.evidence.ScanAll(ctx, defaultBatchSize, func(batch []model.Evidence) error {
		var toDelete []uuid.UUID

		for i := range batch {
			ev := &batch[i]

			if isPinned(ev) {
				continue
			}

			if isInheritanceProtected(ev, inheritedRefs) {
				continue
			}

			maxAge := w.evaluator.MaxAge(ev)
			if maxAge < 0 {
				// No rule matched — don't delete.
				continue
			}
			if maxAge == 0 {
				// max_age=0 means keep forever.
				continue
			}

			age := now.Sub(ev.FinishedAt)
			if age >= maxAge {
				toDelete = append(toDelete, ev.ID)
			}
		}

		if len(toDelete) > 0 {
			deleted, err := w.evidence.DeleteBatch(ctx, toDelete)
			if err != nil {
				return err
			}
			totalDeleted += int(deleted)
		}

		return nil
	})

	return totalDeleted, err
}

// isPinned checks if an evidence record has metadata.retain = true.
func isPinned(ev *model.Evidence) bool {
	if ev.Metadata == nil {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(ev.Metadata, &m); err != nil {
		return false
	}
	retainRaw, ok := m["retain"]
	if !ok {
		return false
	}
	var retain bool
	if err := json.Unmarshal(retainRaw, &retain); err != nil {
		return false
	}
	return retain
}

// isInheritanceProtected checks if the evidence record's rcs_ref is referenced by an inheritance declaration.
func isInheritanceProtected(ev *model.Evidence, inheritedRefs map[string]struct{}) bool {
	_, ok := inheritedRefs[ev.Repo+"\x00"+ev.RCSRef]
	return ok
}
