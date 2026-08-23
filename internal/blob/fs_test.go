package blob

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newFS(t *testing.T) *FS {
	t.Helper()
	s, err := NewFS(t.TempDir())
	require.NoError(t, err)
	return s
}

func put(t *testing.T, s Store, content string) Digest {
	t.Helper()
	d, size, err := s.Put(context.Background(), bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), size)
	assert.Equal(t, DigestOf([]byte(content)), d)
	return d
}

func read(t *testing.T, s Store, d Digest) string {
	t.Helper()
	rc, size, err := s.Get(context.Background(), d)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, int64(len(b)), size)
	return string(b)
}

func TestFSRoundTrip(t *testing.T) {
	s := newFS(t)
	d := put(t, s, "pretend this is a screenshot")
	assert.Equal(t, "pretend this is a screenshot", read(t, s, d))
}

// Storing the same bytes twice is one object under one name. This is what makes
// a backend migration re-runnable and what makes pasting the same screenshot
// into two records cost one blob.
func TestFSPutIsIdempotent(t *testing.T) {
	s := newFS(t)
	first := put(t, s, "same bytes")
	second := put(t, s, "same bytes")
	assert.Equal(t, first, second)

	var objects []Object
	require.NoError(t, s.List(context.Background(), func(o Object) error {
		objects = append(objects, o)
		return nil
	}))
	assert.Len(t, objects, 1)
}

func TestFSGetStatDeleteReportMissingBlobs(t *testing.T) {
	s := newFS(t)
	absent := DigestOf([]byte("never stored"))

	_, _, err := s.Get(context.Background(), absent)
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = s.Stat(context.Background(), absent)
	assert.ErrorIs(t, err, ErrNotFound)

	assert.ErrorIs(t, s.Delete(context.Background(), absent), ErrNotFound)
}

func TestFSDelete(t *testing.T) {
	s := newFS(t)
	d := put(t, s, "temporary")
	require.NoError(t, s.Delete(context.Background(), d))

	_, _, err := s.Get(context.Background(), d)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFSStatReportsSizeAndAge(t *testing.T) {
	s := newFS(t)
	d := put(t, s, "0123456789")

	obj, err := s.Stat(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, d, obj.Digest)
	assert.Equal(t, int64(10), obj.Size)
	// The sweep decides what to delete by age, so a store that cannot say how
	// old an object is would make it unsafe to run.
	assert.WithinDuration(t, time.Now(), obj.Created, time.Minute)
}

func TestFSListReportsEveryStoredBlob(t *testing.T) {
	s := newFS(t)
	want := map[Digest]bool{
		put(t, s, "one"):   true,
		put(t, s, "two"):   true,
		put(t, s, "three"): true,
	}

	got := map[Digest]bool{}
	require.NoError(t, s.List(context.Background(), func(o Object) error {
		got[o.Digest] = true
		return nil
	}))
	assert.Equal(t, want, got)
}

// The sweep deletes what List hands it, so anything the store did not write —
// including its own staging area — must not be reported as a blob.
func TestFSListIgnoresForeignFiles(t *testing.T) {
	s := newFS(t)
	put(t, s, "real")

	require.NoError(t, os.WriteFile(filepath.Join(s.tmp, "staging-leftover"), []byte("x"), 0o644))
	stray := filepath.Join(s.root, algorithm, "ab", "cd", "not-a-digest")
	require.NoError(t, os.MkdirAll(filepath.Dir(stray), 0o755))
	require.NoError(t, os.WriteFile(stray, []byte("x"), 0o644))

	var count int
	require.NoError(t, s.List(context.Background(), func(o Object) error {
		count++
		return nil
	}))
	assert.Equal(t, 1, count)
}

func TestFSListOnEmptyStore(t *testing.T) {
	s := newFS(t)
	require.NoError(t, s.List(context.Background(), func(Object) error {
		t.Fatal("empty store reported an object")
		return nil
	}))
}

// A failed upload must not leave a partial blob behind, and it must not leave
// its staging file behind either.
func TestFSPutCleansUpAfterAFailedRead(t *testing.T) {
	s := newFS(t)
	_, _, err := s.Put(context.Background(), iotest{})
	require.Error(t, err)

	entries, err := os.ReadDir(s.tmp)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

type iotest struct{}

func (iotest) Read([]byte) (int, error) { return 0, assert.AnError }
