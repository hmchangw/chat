//go:build integration

package testutil

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

var (
	minioOnce    sync.Once
	minioClient  *minio.Client
	minioInitErr error
)

func ensureMinIOClient() (*minio.Client, error) {
	minioOnce.Do(func() {
		ctx := context.Background()
		container, err := tcminio.Run(ctx, testimages.MinIO)
		if err != nil {
			minioInitErr = fmt.Errorf("start minio: %w", err)
			return
		}
		// tcminio.MinioContainer.ConnectionString returns "host:port"
		// already (no scheme). No TrimPrefix needed.
		endpoint, err := container.ConnectionString(ctx)
		if err != nil {
			_ = container.Terminate(ctx)
			minioInitErr = fmt.Errorf("get minio endpoint: %w", err)
			return
		}
		c, err := minio.New(endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(container.Username, container.Password, ""),
			Secure: false,
		})
		if err != nil {
			_ = container.Terminate(ctx)
			minioInitErr = fmt.Errorf("connect minio: %w", err)
			return
		}
		minioClient = c
	})
	return minioClient, minioInitErr
}

// MinIO returns a connected MinIO client + a per-test bucket name. The
// bucket is created on entry and removed on t.Cleanup. The bucket name
// is derived from t.Name() with a stable fnv hash so parallel subtests
// can't collide; the prefix lets callers namespace by package
// (e.g. "minioutil"). Bucket names are valid S3 identifiers
// (3-63 chars, lowercase, digits, hyphens only).
//
// Prefix requirements: 3-46 chars, lowercase letters/digits/hyphens
// only, must NOT start or end with a hyphen. The helper does not
// validate -- callers passing invalid prefixes get an InvalidBucketName
// error from MinIO at MakeBucket time.
func MinIO(t *testing.T, prefix string) (*minio.Client, string) {
	t.Helper()
	c, err := ensureMinIOClient()
	if err != nil {
		t.Fatalf("testutil.MinIO: %v", err)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(t.Name()))
	bucket := strings.ToLower(fmt.Sprintf("%s-%x", prefix, h.Sum64()))
	// S3 bucket names are capped at 63 chars; truncate defensively.
	if len(bucket) > 63 {
		bucket = bucket[:63]
	}
	ctx := context.Background()
	if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
		t.Fatalf("testutil.MinIO MakeBucket %q: %v", bucket, err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup. Bucket independence is GUARANTEED by the
		// per-test fnv-hashed name (one test's bucket can't collide with
		// another's even if cleanup fails completely). So a cleanup
		// failure does not affect downstream test correctness -- only
		// resource hygiene -- and we log + continue rather than fail
		// the test post-hoc. Bounded by a 30-second context to avoid
		// blocking test-process exit on a hung MinIO.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for obj := range c.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err != nil {
				t.Logf("list during cleanup of %q: %v", bucket, obj.Err)
				continue
			}
			if err := c.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
				t.Logf("remove %q/%q during cleanup: %v", bucket, obj.Key, err)
			}
		}
		if err := c.RemoveBucket(ctx, bucket); err != nil {
			t.Logf("remove bucket %q during cleanup: %v", bucket, err)
		}
	})
	return c, bucket
}
