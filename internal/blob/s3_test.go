package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newS3 brings up an S3-compatible gateway (versitygw over a plain directory)
// and returns a store pointed at a fresh bucket.
//
// The suite skips rather than fails when no container runtime is reachable, so
// that `go test ./...` still works on a machine without Docker; the Bazel target
// carries requires-docker, which is what makes CI run it for real.
func newS3(t *testing.T) *S3 {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "versity/versitygw:v1.11.0",
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"ROOT_ACCESS_KEY": "evidence",
				"ROOT_SECRET_KEY": "evidence-secret",
				"VGW_BACKEND":     "posix",
				"VGW_BACKEND_ARG": "/tmp",
				"VGW_PORT":        ":9000",
				"VGW_HEALTH":      "/health",
			},
			// The server binds the port before it will answer on it, so waiting
			// for a listening socket returns while the first request still gets
			// a connection reset. Its own health endpoint is the thing worth
			// waiting for. Same class of bug as the double bind the Postgres
			// harness works around in tests/integration_test.go, and it made
			// this suite fail on CI about one run in ten.
			WaitingFor: wait.ForHTTP("/health").
				WithPort("9000/tcp").
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no container runtime available: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "9000")
	require.NoError(t, err)

	cfg := S3Config{
		Endpoint:  fmt.Sprintf("%s:%s", host, port.Port()),
		Bucket:    "evidence-blobs",
		AccessKey: "evidence",
		SecretKey: "evidence-secret",
	}

	// The health probe above is necessary and not sufficient: with MinIO, the
	// previous backend here, it answered before the object layer behind it was
	// registered, so the first S3 call could still come back "Server not
	// initialized yet, please try again". That is what this suite hit on CI — a
	// bucket check failing a few milliseconds after the container reported
	// itself ready.
	//
	// Reaching for a better endpoint is how the probe above was arrived at the
	// first time, and it moved the race rather than closing it. So this waits
	// on the operation the test actually needs instead of on a proxy for it:
	// the bucket check, retried until the server can serve it.
	//
	// Any error is retried, not a matched message, because a readiness race
	// does not promise to announce itself the same way twice. The cost is that
	// a genuine misconfiguration — a wrong key, an unreachable endpoint — takes
	// until the deadline to report the error it would otherwise report at once.
	// In a harness that already spends longer than this starting a container,
	// that is the cheaper of the two mistakes.
	var s *S3
	deadline := time.Now().Add(30 * time.Second)
	for {
		s, err = NewS3(ctx, cfg)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
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

func TestS3SeekReadsOnlyRequestedPosition(t *testing.T) {
	s := newS3(t)
	body := bytes.Repeat([]byte("0123456789"), 10000)
	d, _, err := s.Put(context.Background(), bytes.NewReader(body))
	require.NoError(t, err)
	rc, size, err := s.Get(context.Background(), d)
	require.NoError(t, err)
	defer rc.Close()
	assert.Equal(t, int64(len(body)), size)
	_, err = rc.Seek(90001, io.SeekStart)
	require.NoError(t, err)
	got := make([]byte, 5)
	_, err = io.ReadFull(rc, got)
	require.NoError(t, err)
	assert.Equal(t, []byte("12345"), got)
	_, err = rc.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = io.ReadFull(rc, got)
	require.NoError(t, err)
	assert.Equal(t, []byte("01234"), got)
}
