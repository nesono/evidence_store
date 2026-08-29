package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FS is a Store backed by a directory tree.
//
// It exists for two reasons: running the server without an object store (a
// `go run` against a local Postgres should not need MinIO), and testing
// everything above the storage layer without a container. Deployments that
// need blobs to outlive one machine's disk use S3.
type FS struct {
	root string
	tmp  string
}

// NewFS returns a store rooted at dir, creating it if needed.
func NewFS(dir string) (*FS, error) {
	// Staging happens inside the root so the rename into place stays on one
	// filesystem, which is what makes it atomic.
	tmp := filepath.Join(dir, "staging")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, fmt.Errorf("create blob directory: %w", err)
	}
	return &FS{root: dir, tmp: tmp}, nil
}

func (s *FS) path(d Digest) string {
	return filepath.Join(s.root, filepath.FromSlash(d.Key()))
}

func (s *FS) Put(ctx context.Context, r io.Reader) (Digest, int64, error) {
	f, d, size, err := stage(r, s.tmp)
	if err != nil {
		return "", 0, err
	}
	name := f.Name()
	// Checked, unlike the other closes here, because this file was written to
	// and is about to be renamed into the store under a digest computed while
	// writing it. A close that reports a failed flush means the bytes on disk
	// are not the bytes that were hashed, and filing them anyway would put
	// content in a content-addressed store that its own name does not describe.
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", 0, fmt.Errorf("finish staging blob: %w", err)
	}
	defer func() { _ = os.Remove(name) }()

	dst := s.path(d)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, fmt.Errorf("create blob directory: %w", err)
	}
	// Rename rather than checking for existence first: identical content
	// produces identical bytes, so overwriting a blob with itself is not a
	// conflict to resolve.
	if err := os.Rename(name, dst); err != nil {
		return "", 0, fmt.Errorf("store blob: %w", err)
	}
	return d, size, nil
}

func (s *FS) Get(ctx context.Context, d Digest) (io.ReadCloser, int64, error) {
	f, err := os.Open(s.path(d))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("open blob: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close() // nothing was written; the stat error is the one to report
		return nil, 0, fmt.Errorf("stat blob: %w", err)
	}
	return f, info.Size(), nil
}

func (s *FS) Stat(ctx context.Context, d Digest) (Object, error) {
	info, err := os.Stat(s.path(d))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Object{}, ErrNotFound
		}
		return Object{}, fmt.Errorf("stat blob: %w", err)
	}
	return Object{Digest: d, Size: info.Size(), Created: info.ModTime()}, nil
}

func (s *FS) Delete(ctx context.Context, d Digest) error {
	if err := os.Remove(s.path(d)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

func (s *FS) List(ctx context.Context, fn func(Object) error) error {
	base := filepath.Join(s.root, algorithm)
	err := filepath.WalkDir(base, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return ctx.Err()
		}
		key := filepath.ToSlash(strings.TrimPrefix(path, s.root+string(filepath.Separator)))
		d, ok := digestFromKey(key)
		if !ok {
			// Not something this store wrote. Leaving it alone is the only safe
			// reading: the sweep deletes what it lists.
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		return fn(Object{Digest: d, Size: info.Size(), Created: info.ModTime()})
	})
	// An empty store has no tree yet, which is not an error to a caller asking
	// what is in it.
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
