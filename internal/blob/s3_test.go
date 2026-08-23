package blob

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newS3 brings up MinIO and returns a store pointed at a fresh bucket.
//
// The suite skips rather than fails when no container runtime is reachable, so
// that `go test ./...` still works on a machine without Docker; the Bazel target
// carries requires-docker, which is what makes CI run it for real.
func newS3(t *testing.T) *S3 {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "minio/minio:RELEASE.2025-04-22T22-12-26Z",
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     "minioadmin",
				"MINIO_ROOT_PASSWORD": "minioadmin",
			},
			Cmd:        []string{"server", "/data"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no container runtime available: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "9000")
	require.NoError(t, err)

	s, err := NewS3(ctx, S3Config{
		Endpoint:  fmt.Sprintf("%s:%s", host, port.Port()),
		Bucket:    "evidence-blobs",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
	})
	require.NoError(t, err)
	return s
}

// The S3 backend has to behave exactly as the filesystem one does, because
// which is in use is a deployment choice that nothing above the storage layer
// is aware of.
func TestS3RoundTrip(t *testing.T) {
	s := newS3(t)
	d := put(t, s, "pretend this is a screenshot")
	assert.Equal(t, "pretend this is a screenshot", read(t, s, d))
}

func TestS3PutIsIdempotent(t *testing.T) {
	s := newS3(t)
	assert.Equal(t, put(t, s, "same bytes"), put(t, s, "same bytes"))

	var count int
	require.NoError(t, s.List(context.Background(), func(Object) error {
		count++
		return nil
	}))
	assert.Equal(t, 1, count)
}

func TestS3ReportsMissingBlobs(t *testing.T) {
	s := newS3(t)
	absent := DigestOf([]byte("never stored"))

	_, _, err := s.Get(context.Background(), absent)
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = s.Stat(context.Background(), absent)
	assert.ErrorIs(t, err, ErrNotFound)

	// S3 deletes absent keys happily; the store's contract says otherwise, and
	// the sweep's accounting depends on the contract.
	assert.ErrorIs(t, s.Delete(context.Background(), absent), ErrNotFound)
}

func TestS3DeleteAndList(t *testing.T) {
	s := newS3(t)
	keep := put(t, s, "keep me")
	drop := put(t, s, "drop me")

	require.NoError(t, s.Delete(context.Background(), drop))

	got := map[Digest]bool{}
	require.NoError(t, s.List(context.Background(), func(o Object) error {
		got[o.Digest] = true
		return nil
	}))
	assert.Equal(t, map[Digest]bool{keep: true}, got)
}

func TestS3StatReportsSizeAndAge(t *testing.T) {
	s := newS3(t)
	d, _, err := s.Put(context.Background(), bytes.NewReader([]byte("0123456789")))
	require.NoError(t, err)

	obj, err := s.Stat(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, int64(10), obj.Size)
	assert.WithinDuration(t, time.Now(), obj.Created, time.Minute)
}
