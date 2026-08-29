package tests

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Tests: migrations
// ---------------------------------------------------------------------------

// Every down migration is a rollback somebody will run at three in the morning,
// which is a poor time to discover that it drops a table another one still
// references — or that it never worked at all.
//
// This gets a database of its own. Tearing the shared schema down and back up
// underneath the rest of the suite would test the migrations by breaking
// everything else.
func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Not parameterisable, and safe: the name is a constant, not input.
	const scratchDB = "evidence_migration_roundtrip"
	_, err := testPool.Exec(ctx, `DROP DATABASE IF EXISTS `+scratchDB)
	require.NoError(t, err)
	_, err = testPool.Exec(ctx, `CREATE DATABASE `+scratchDB)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+scratchDB+` WITH (FORCE)`)
		assert.NoError(t, err)
	})

	scratchURL := strings.Replace(testDatabaseURL, "/evidence_test?", "/"+scratchDB+"?", 1)
	require.Contains(t, scratchURL, scratchDB, "failed to point the connection string at the scratch database")

	m, err := migrate.New("file://"+testMigrationsPath, scratchURL)
	require.NoError(t, err)
	defer func() { _, _ = m.Close() }()

	require.NoError(t, m.Up(), "up on an empty database")
	require.NoError(t, m.Down(), "down from the head")
	// The one that catches a down migration that only half undid its up: the
	// second time through, every CREATE runs against what the rollback left.
	require.NoError(t, m.Up(), "up again on what the rollback left behind")

	version, dirty, err := m.Version()
	require.NoError(t, err)
	assert.False(t, dirty)

	// Counted rather than hardcoded: a number written here is a number every
	// future migration has to remember to bump, and the failure when somebody
	// forgets says nothing about what is actually wrong.
	ups, err := filepath.Glob(filepath.Join(testMigrationsPath, "*.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, ups)
	assert.Equal(t, uint(len(ups)), version, "the round trip should end at the latest migration")
}

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
