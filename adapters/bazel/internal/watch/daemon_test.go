package watch

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAndReadPID(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".evidence"), 0o755))

	require.NoError(t, WritePID(dir))
	pid := ReadPID(dir)
	assert.Equal(t, os.Getpid(), pid)
}

func TestReadPIDMissingFile(t *testing.T) {
	dir := t.TempDir()
	pid := ReadPID(dir)
	assert.Equal(t, 0, pid)
}

func TestIsRunningCurrentProcess(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".evidence"), 0o755))
	require.NoError(t, WritePID(dir))

	pid, alive := IsRunning(dir)
	assert.True(t, alive)
	assert.Equal(t, os.Getpid(), pid)
}

func TestIsRunningStalePID(t *testing.T) {
	dir := t.TempDir()
	evidenceDir := filepath.Join(dir, ".evidence")
	require.NoError(t, os.MkdirAll(evidenceDir, 0o755))

	// Write a PID that definitely doesn't exist.
	stalePID := 99999999
	require.NoError(t, os.WriteFile(filepath.Join(evidenceDir, "watch.pid"), []byte(strconv.Itoa(stalePID)), 0o644))

	pid, alive := IsRunning(dir)
	assert.False(t, alive)
	assert.Equal(t, stalePID, pid)

	// Stale PID file should be cleaned up.
	assert.Equal(t, 0, ReadPID(dir))
}

func TestRemovePID(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".evidence"), 0o755))
	require.NoError(t, WritePID(dir))

	RemovePID(dir)
	assert.Equal(t, 0, ReadPID(dir))
}
