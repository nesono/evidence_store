package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is a Store backed by any S3-compatible object store. MinIO is what
// docker-compose runs locally and what the tests exercise; the same code talks
// to S3 in a deployment, which is the point of choosing this client.
type S3 struct {
	client *minio.Client
	bucket string
}

// S3Config describes how to reach the object store.
type S3Config struct {
	Endpoint  string // host:port, no scheme
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Region    string
}

// NewS3 connects to the object store and ensures the bucket exists.
//
// Creating the bucket here rather than requiring an operator to pre-create it
// keeps `docker compose up` a single step; on a real deployment the credentials
// usually cannot create buckets and the call is a no-op against one that is
// already there.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to object store: %w", err)
	}

	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %q: %w", cfg.Bucket, err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("create bucket %q: %w", cfg.Bucket, err)
		}
	}

	return &S3{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3) Put(ctx context.Context, r io.Reader) (Digest, int64, error) {
	// The object cannot be named until every byte has been hashed, so the
	// upload is staged locally first. It also gives the client a file with a
	// known size, which is what lets it choose a single PUT over a multipart
	// upload for the small objects an image usually is.
	f, d, size, err := stage(r, os.TempDir())
	if err != nil {
		return "", 0, err
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()

	// Content-Type is deliberately not set. The type is a function of the bytes
	// and is sniffed on the way out, so recording it here would only create a
	// second answer that could disagree with the first.
	_, err = s.client.PutObject(ctx, s.bucket, d.Key(), f, size, minio.PutObjectOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("store blob: %w", err)
	}
	return d, size, nil
}

func (s *S3) Get(ctx context.Context, d Digest) (io.ReadCloser, int64, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, d.Key(), minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("open blob: %w", err)
	}
	// GetObject is lazy: a missing object only surfaces on the first read or
	// stat, so the error has to be translated here rather than above.
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, s3Error(err, "stat blob")
	}
	return obj, info.Size, nil
}

func (s *S3) Stat(ctx context.Context, d Digest) (Object, error) {
	info, err := s.client.StatObject(ctx, s.bucket, d.Key(), minio.StatObjectOptions{})
	if err != nil {
		return Object{}, s3Error(err, "stat blob")
	}
	return Object{Digest: d, Size: info.Size, Created: info.LastModified}, nil
}

func (s *S3) Delete(ctx context.Context, d Digest) error {
	// A delete of an absent key succeeds on S3, so absence is established first
	// to keep the interface's contract the same across backends.
	if _, err := s.Stat(ctx, d); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, d.Key(), minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

func (s *S3) List(ctx context.Context, fn func(Object) error) error {
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    algorithm + "/",
		Recursive: true,
	}) {
		if obj.Err != nil {
			return fmt.Errorf("list blobs: %w", obj.Err)
		}
		d, ok := digestFromKey(obj.Key)
		if !ok {
			// Something else's object in the same bucket. The sweep deletes what
			// this reports, so anything unrecognised is left alone.
			continue
		}
		if err := fn(Object{Digest: d, Size: obj.Size, Created: obj.LastModified}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func s3Error(err error, what string) error {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) && (resp.Code == "NoSuchKey" || resp.StatusCode == 404) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", what, err)
}
