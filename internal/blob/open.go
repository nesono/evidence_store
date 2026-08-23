package blob

import (
	"context"
	"fmt"
)

// Options selects and configures a backing store.
type Options struct {
	// Backend is "fs" or "s3".
	Backend string
	// Path is the directory the fs backend stores blobs in.
	Path string
	S3   S3Config
}

// Open returns the configured store.
//
// The default is the filesystem so that a checkout runs without an object
// store; a deployment that wants blobs to outlive one machine's disk sets the
// backend to s3, which is what docker-compose does with MinIO.
func Open(ctx context.Context, opts Options) (Store, error) {
	switch opts.Backend {
	case "", "fs":
		return NewFS(opts.Path)
	case "s3":
		if opts.S3.Endpoint == "" || opts.S3.Bucket == "" {
			return nil, fmt.Errorf("blob backend s3 needs an endpoint and a bucket")
		}
		return NewS3(ctx, opts.S3)
	default:
		return nil, fmt.Errorf("unknown blob backend %q: expected fs or s3", opts.Backend)
	}
}
