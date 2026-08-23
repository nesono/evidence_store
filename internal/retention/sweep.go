package retention

import (
	"context"
	"time"

	"github.com/nesono/evidence-store/internal/blob"
)

// sweepBatch is how many objects are checked for reachability per query.
const sweepBatch = 500

// SweepBlobs deletes blobs no evidence record references any more.
//
// Blobs are content-addressed and shared, so they cannot be deleted with the
// record that used them — a screenshot pasted into two runs outlives the first.
// Reachability is the only workable rule: an object with no reference row is
// garbage, and retention deleting a record releases its references by cascade.
//
// The grace period is what makes this safe to run against a live store. An
// image is uploaded while the tester is still typing the log, so between the
// upload and the record being filed the object is unreachable and looks exactly
// like garbage. Objects younger than the grace period are therefore left alone.
//
// Returns the number of objects deleted.
func (w *Worker) SweepBlobs(ctx context.Context) (int, error) {
	if w.blobs == nil || w.blobRefs == nil {
		return 0, nil
	}

	cutoff := time.Now().Add(-w.orphanGrace)
	var deleted int
	batch := make([]blob.Digest, 0, sweepBatch)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		referenced, err := w.blobRefs.Referenced(ctx, batch)
		if err != nil {
			return err
		}
		for _, d := range batch {
			if referenced[d] {
				continue
			}
			if err := w.blobs.Delete(ctx, d); err != nil {
				// Something else may have deleted it, or the store may be
				// having a bad day. Neither is a reason to abandon the pass:
				// the object is still unreferenced and comes up again next time.
				w.logger.Warn("failed to delete unreferenced blob", "error", err, "digest", d)
				continue
			}
			deleted++
		}
		batch = batch[:0]
		return nil
	}

	err := w.blobs.List(ctx, func(o blob.Object) error {
		if o.Created.After(cutoff) {
			return nil
		}
		batch = append(batch, o.Digest)
		if len(batch) < sweepBatch {
			return nil
		}
		return flush()
	})
	if err != nil {
		return deleted, err
	}

	return deleted, flush()
}
