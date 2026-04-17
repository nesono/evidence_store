package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotLoadEmpty(t *testing.T) {
	dir := t.TempDir()
	snap, err := LoadSnapshot(dir)
	require.NoError(t, err)
	assert.NotNil(t, snap.Files)
	assert.Empty(t, snap.Files)
}

func TestSnapshotSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".evidence"), 0o755))

	snap := &Snapshot{
		UploadedAt: time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC),
		Files: map[string]time.Time{
			"/path/to/test.xml": time.Date(2026, 4, 15, 9, 59, 0, 0, time.UTC),
		},
	}
	require.NoError(t, snap.Save(dir))

	loaded, err := LoadSnapshot(dir)
	require.NoError(t, err)
	assert.Equal(t, snap.UploadedAt.Unix(), loaded.UploadedAt.Unix())
	assert.Len(t, loaded.Files, 1)
}

func TestSnapshotChangedFiles(t *testing.T) {
	dir := t.TempDir()

	// Create two test.xml files.
	file1 := filepath.Join(dir, "test1.xml")
	file2 := filepath.Join(dir, "test2.xml")
	require.NoError(t, os.WriteFile(file1, []byte("<xml/>"), 0o644))
	require.NoError(t, os.WriteFile(file2, []byte("<xml/>"), 0o644))

	info1, _ := os.Stat(file1)
	info2, _ := os.Stat(file2)

	// Snapshot knows about file1 with its current mtime.
	snap := &Snapshot{
		Files: map[string]time.Time{
			file1: info1.ModTime(),
		},
	}

	// file2 is new, file1 is unchanged.
	changed, err := snap.ChangedFiles([]string{file1, file2})
	require.NoError(t, err)
	assert.Len(t, changed, 1)
	assert.Equal(t, file2, changed[0])

	// After touching file1, both should show as changed.
	time.Sleep(10 * time.Millisecond) // ensure mtime difference
	require.NoError(t, os.WriteFile(file1, []byte("<xml>updated</xml>"), 0o644))
	info1after, _ := os.Stat(file1)
	_ = info2 // not changed

	// Re-check: file1 now has newer mtime, file2 still unknown.
	changed, err = snap.ChangedFiles([]string{file1, file2})
	require.NoError(t, err)
	assert.Len(t, changed, 2)
	_ = info1after
}

func TestSnapshotRecord(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "test.xml")
	require.NoError(t, os.WriteFile(file1, []byte("<xml/>"), 0o644))

	snap := &Snapshot{Files: make(map[string]time.Time)}
	snap.Record([]string{file1})

	assert.Contains(t, snap.Files, file1)
	assert.False(t, snap.UploadedAt.IsZero())
}

func TestSnapshotCorruptedFileStartsFresh(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".evidence"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".evidence", "last-upload.json"), []byte("not json"), 0o644))

	snap, err := LoadSnapshot(dir)
	require.NoError(t, err)
	assert.NotNil(t, snap.Files)
	assert.Empty(t, snap.Files)
}
