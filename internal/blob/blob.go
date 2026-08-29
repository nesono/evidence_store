// Package blob is the content-addressed store for files that hang off a test
// log — today the images a tester pastes in, later the videos (#79).
//
// Blobs are named by the SHA-256 of their bytes, never by where they sit. That
// buys three things this store cares about specifically:
//
//   - Evidence is tamper-evident. The name of a photo attached to a FAIL is a
//     checksum of the photo, so anyone holding the log can verify the image
//     they were served is the image the tester filed.
//   - Moving the data is a copy. Local disk to MinIO to S3 needs no ID
//     remapping and no rewrite of the logs that reference the objects, and a
//     half-finished copy can simply be re-run.
//   - Uploading the same screenshot twice costs one object.
//
// The flip side of dedup is that deletion cannot be ownership-based: a blob
// lives while some log still names it. See the sweep in internal/retention.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// PathPrefix is where blobs are served. It is part of the reference written
// into a test log, so it is also what the log parser and the renderer look for;
// changing it means keeping the old prefix resolving for logs already written.
const PathPrefix = "/api/v1/blobs/"

// algorithm is the only digest algorithm in use. It is spelled out in every
// reference so a second one could be introduced later without ambiguity.
const algorithm = "sha256"

var (
	// ErrNotFound is returned by Get, Stat and Delete for an absent blob.
	ErrNotFound = errors.New("blob not found")

	// ErrUnsupportedMedia is returned when bytes are not one of the media types
	// a log is allowed to embed.
	ErrUnsupportedMedia = errors.New("unsupported media type")
)

// Digest is a content address, spelled "sha256:<64 hex>".
type Digest string

var digestPattern = regexp.MustCompile(`^` + algorithm + `:[0-9a-f]{64}$`)

// DigestOf returns the content address of b.
func DigestOf(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest(algorithm + ":" + hex.EncodeToString(sum[:]))
}

// ParseDigest accepts a digest as it appears in a reference. The pattern is
// strict — lowercase hex, exact length — because a digest that reaches the
// store is used to build an object key.
func ParseDigest(s string) (Digest, error) {
	if !digestPattern.MatchString(s) {
		return "", fmt.Errorf("invalid digest %q: expected %s:<64 hex>", s, algorithm)
	}
	return Digest(s), nil
}

// Hex returns the digest without its algorithm prefix.
func (d Digest) Hex() string { return strings.TrimPrefix(string(d), algorithm+":") }

// Key is the object key for a digest. The two levels of fan-out keep any single
// directory (or S3 list prefix) to a manageable width once there are a lot of
// objects; the full hex is repeated in the leaf so a key is readable on its own.
func (d Digest) Key() string {
	h := d.Hex()
	return algorithm + "/" + h[0:2] + "/" + h[2:4] + "/" + h
}

// digestFromKey reverses Key. Used when listing a store, where the object key
// is all the sweep has to go on.
func digestFromKey(key string) (Digest, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != algorithm {
		return "", false
	}
	d, err := ParseDigest(algorithm + ":" + parts[3])
	if err != nil {
		return "", false
	}
	if parts[1] != parts[3][0:2] || parts[2] != parts[3][2:4] {
		return "", false
	}
	return d, true
}

// mediaExt is the allowlist of what a log may embed, mapped to the canonical
// extension used in references.
//
// It is an allowlist rather than a blocklist because the bytes end up being
// served back to a browser: anything script-capable is the whole problem. SVG
// is markup and is deliberately absent — it is an image only by convention.
var mediaExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// SniffLen is how many leading bytes DetectMedia needs.
const SniffLen = 512

// DetectMedia identifies the media type from the leading bytes of a blob and
// returns it with its canonical extension.
//
// The uploader's own Content-Type never enters into it. A client can claim
// anything, and the type decided here is the type sent back on the way out, so
// it has to be a property of the bytes.
func DetectMedia(head []byte) (mediaType, ext string, err error) {
	mediaType, _, _ = strings.Cut(http.DetectContentType(head), ";")
	ext, ok := mediaExt[mediaType]
	if !ok {
		return "", "", fmt.Errorf("%w: %s", ErrUnsupportedMedia, mediaType)
	}
	return mediaType, ext, nil
}

// Ref is a blob as a test log refers to it: a digest plus the extension that
// tells a renderer which element to build without having to ask the store what
// the bytes are.
type Ref struct {
	Digest Digest
	Ext    string
}

// Path is the reference as it is written into a log and into metadata.
func (r Ref) Path() string {
	if r.Ext == "" {
		return PathPrefix + string(r.Digest)
	}
	return PathPrefix + string(r.Digest) + "." + r.Ext
}

// Object is what a store knows about a blob without reading it.
type Object struct {
	Digest  Digest
	Size    int64
	Created time.Time
}

// Store is the backing store for blob bytes. Implementations are content-
// addressed key/value stores and nothing more: they neither know nor record
// what a blob is for, which is what lets the same store hold a 40 KiB
// screenshot and, later, a 400 MB video.
//
// Put is idempotent. Storing bytes that are already present is a no-op that
// returns the same digest, which is what makes a migration between backends
// re-runnable.
type Store interface {
	Put(ctx context.Context, r io.Reader) (Digest, int64, error)
	Get(ctx context.Context, d Digest) (io.ReadCloser, int64, error)
	Stat(ctx context.Context, d Digest) (Object, error)
	Delete(ctx context.Context, d Digest) error
	// List walks every object in the store. Order is unspecified.
	List(ctx context.Context, fn func(Object) error) error
}

// stage copies r into a temporary file in dir while hashing it, and returns the
// file positioned at its start.
//
// Hashing is why this exists: the name of a blob is not known until the last
// byte has been read, so the bytes have to go somewhere first. A temporary file
// rather than memory is what will let this carry a video without the server
// holding it all at once — the size cap that keeps images small lives in the
// API handler, not here.
func stage(r io.Reader, dir string) (_ *os.File, _ Digest, _ int64, err error) {
	f, err := os.CreateTemp(dir, "staging-*")
	if err != nil {
		return nil, "", 0, fmt.Errorf("create staging file: %w", err)
	}
	// The cleanup closes over the local file rather than a named return so that
	// it still has something to remove on the paths that return a nil file.
	defer func() {
		if err != nil {
			// Best effort on a path that is already failing: the caller is
			// being told why, and a second error about the tidying up would
			// only bury the first.
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		return nil, "", 0, fmt.Errorf("stage blob: %w", err)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, fmt.Errorf("rewind staging file: %w", err)
	}
	return f, Digest(algorithm + ":" + hex.EncodeToString(h.Sum(nil))), size, nil
}
