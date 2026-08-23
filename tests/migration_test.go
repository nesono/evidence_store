package tests

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Tests: schema constraints
// ---------------------------------------------------------------------------

// evidence_type is a closed set, enforced in two places: the API rejects a bad
// value with a message naming the three, and the column refuses to hold one at
// all. The second is what stops the column drifting away from the API through a
// bulk load, a seeding script, or a second writer that skips validation — none
// of which go through internal/validate.
//
// This writes to the table directly rather than through the API, because going
// through the API would only re-test the validator.
func TestEvidenceTypeConstraintRejectsUnknownValues(t *testing.T) {
	ctx := context.Background()

	insert := func(evidenceType string) error {
		_, err := testPool.Exec(ctx, `
			INSERT INTO evidence (repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at)
			VALUES ('org/constraint', 'main', 'abc123', '//pkg:test', $1, 'ci-bot', 'PASS', now())
		`, evidenceType)
		return err
	}

	// The spellings a client is most likely to try: the labels the UI shows, the
	// runners that are metadata.collector's business, and a plausible fourth.
	for _, et := range []string{"bazel", "pytest", "manual", "CI", "Manual Test", "manual-test", "", "smoke"} {
		assert.Error(t, insert(et), "expected the constraint to reject %q", et)
	}

	for _, et := range []string{"ci", "manual_test", "demonstration"} {
		assert.NoError(t, insert(et), "expected %q to be accepted", et)
	}
}
