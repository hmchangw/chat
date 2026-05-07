// Package minioutil provides a small typed wrapper around the MinIO Go
// SDK for storing and retrieving JSON documents. It mirrors the
// pkg/mongoutil and pkg/valkeyutil shape: a connection helper plus a
// typed Bucket[T] for the common JSON-blob workload.
//
// Concurrency: *minio.Client is goroutine-safe (it wraps an http.Client).
// *Bucket[T] is goroutine-safe (it carries no mutable state of its own).
package minioutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Bucket is a typed wrapper that binds a MinIO client to a single bucket
// and a JSON-marshalable payload type T. T has no static constraint;
// JSON marshaling determines suitability at runtime.
//
// *Bucket[T] is goroutine-safe.
type Bucket[T any] struct {
	client *minio.Client
	name   string
}

// NewBucket binds a client to a bucket name. Verifies bucket existence via
// client.BucketExists at construction time so a misconfigured MINIO_BUCKET
// env var fails the service at startup rather than failing every Get/Put
// silently. Does NOT create the bucket — provisioning is owned by ops/IaC.
func NewBucket[T any](ctx context.Context, client *minio.Client, name string) (*Bucket[T], error) {
	exists, err := client.BucketExists(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("minioutil bucket exists check %q: %w", name, err)
	}
	if !exists {
		return nil, fmt.Errorf("minioutil bucket %q does not exist", name)
	}
	return &Bucket[T]{client: client, name: name}, nil
}

// Raw returns the underlying *minio.Client. Mirrors Collection[T].Raw().
// Escape hatch for features the wrapper does not surface: presigned
// URLs, multipart uploads, conditional Put (If-Match/If-None-Match),
// object tagging, versioning, region-specific operations. Callers
// reaching for Raw() are expected to combine it with Name() to scope
// to this bucket.
func (b *Bucket[T]) Raw() *minio.Client {
	return b.client
}

// Name returns the bucket name. Pairs with Raw() so callers can build
// arbitrary minio-go calls scoped to this bucket without needing to
// thread the bucket name separately.
func (b *Bucket[T]) Name() string {
	return b.name
}

// Put marshals v as JSON and stores it under key with
// Content-Type: application/json; charset=utf-8. Existing objects at the
// same key are replaced (S3 native PUT semantics on non-versioned buckets).
func (b *Bucket[T]) Put(ctx context.Context, key string, v T) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("minioutil put %s/%s marshal: %w", b.name, key, err)
	}
	_, err = b.client.PutObject(ctx, b.name, key, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{
		ContentType: "application/json; charset=utf-8",
	})
	if err != nil {
		return fmt.Errorf("minioutil put %s/%s: %w", b.name, key, err)
	}
	return nil
}

// Get fetches the object at key and unmarshals it from JSON into T.
// Returns (nil, nil) when the key does not exist — matches
// Collection.FindOne not-found semantics so callers branch on nil
// rather than catching errors.
//
// Implementation note: minio-go's GetObject is lazy -- no HTTP request is
// issued until the returned *Object is touched. We call Stat() first to
// detect missing keys (HEAD; surfaces NoSuchKey via ErrorResponse), then
// stream the body via json.NewDecoder (separate GET). Two HTTP round
// trips total per Get; acceptable for the small-JSON-blob workload.
func (b *Bucket[T]) Get(ctx context.Context, key string) (*T, error) {
	obj, err := b.client.GetObject(ctx, b.name, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("minioutil get %s/%s: %w", b.name, key, err)
	}
	defer obj.Close()

	if _, err := obj.Stat(); err != nil {
		var minioErr minio.ErrorResponse
		if errors.As(err, &minioErr) && minioErr.Code == "NoSuchKey" {
			return nil, nil
		}
		return nil, fmt.Errorf("minioutil get %s/%s stat: %w", b.name, key, err)
	}

	var v T
	if err := json.NewDecoder(obj).Decode(&v); err != nil {
		return nil, fmt.Errorf("minioutil get %s/%s decode: %w", b.name, key, err)
	}
	return &v, nil
}

// defaultListCap matches S3's per-request page cap; choosing this value
// means a List call with no explicit cap fits in exactly one round trip
// against a bucket with <= 1000 keys at the prefix.
const defaultListCap = 1000

// List returns up to maxKeys keys whose names start with prefix, in S3
// lexicographic order. maxKeys=0 defaults to defaultListCap (1000) to
// prevent unbounded scans on misuse. Pass math.MaxInt to drain a bucket.
// WARNING: the result is held entirely in memory; at ~50 bytes/key that's
// ~50MB per million keys. For unbounded scans on large buckets, prefer
// minio-go's iterator API via Bucket.Raw().
//
// Listing is always recursive (no S3 delimiter), so all keys under prefix
// are returned regardless of "/" boundaries.
//
// Implementation note: minio-go's ListObjects spawns a goroutine that
// fills the returned channel. context.WithCancel + defer cancel() is
// load-bearing -- breaking out of the range loop without cancelling
// leaks that goroutine.
func (b *Bucket[T]) List(ctx context.Context, prefix string, maxKeys int) ([]string, error) {
	if maxKeys <= 0 {
		maxKeys = defaultListCap
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	keys := make([]string, 0)
	for obj := range b.client.ListObjects(ctx, b.name, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
		MaxKeys:   maxKeys, // server-side per-page hint; minio-go clamps to 1000
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("minioutil list %s prefix=%q: %w", b.name, prefix, obj.Err)
		}
		keys = append(keys, obj.Key)
		if len(keys) >= maxKeys {
			break
		}
	}
	return keys, nil
}

// Delete removes the object at key. Idempotent on non-versioned buckets
// -- returns nil if the key does not exist (S3 / MinIO native semantics:
// DELETE returns 204 regardless of prior existence). Versioning is out of
// scope; on a versioned bucket this creates a delete-marker rather than
// performing a true delete, and subsequent reads see the marker.
func (b *Bucket[T]) Delete(ctx context.Context, key string) error {
	if err := b.client.RemoveObject(ctx, b.name, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("minioutil delete %s/%s: %w", b.name, key, err)
	}
	return nil
}

// Connect constructs a MinIO client. Unlike mongoutil.Connect /
// valkeyutil.Connect, it does NOT issue a connectivity probe at
// construction time. The reason is least-privilege IAM: a probe via
// ListBuckets requires s3:ListAllMyBuckets (an account-wide permission),
// while real production deployments scope credentials to the one bucket
// the service uses (s3:ListBucket on that bucket only). Probing here
// would force callers to grant broader IAM than they need.
//
// NewBucket carries the actual fail-fast probe via client.BucketExists,
// which only requires bucket-scoped s3:ListBucket. So the standard
// startup wiring is:
//
//	client, err := minioutil.Connect(ctx, endpoint, useSSL, key, secret)
//	if err != nil { ... }
//	bucket, err := minioutil.NewBucket[T](ctx, client, cfg.Bucket)
//	if err != nil { ... } // <-- this is where misconfig surfaces
//
// The endpoint must be host:port or hostname WITHOUT a scheme.
// Examples: "localhost:9000", "minio.example.com",
// "s3.us-east-1.amazonaws.com". Do NOT include "http://" or "https://".
// The useSSL parameter controls the scheme internally.
//
// Region defaults to "us-east-1" -- irrelevant for MinIO, may matter for
// AWS S3 in non-us-east-1 regions; if needed, add a region option in a
// follow-up. Custom TLS (custom CA, mTLS, skip-verify) is not
// configurable via Connect; for those cases callers can construct
// *minio.Client directly using minio.New with a custom Transport.
func Connect(_ context.Context, endpoint string, useSSL bool, accessKey, secretKey string) (*minio.Client, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minioutil connect: %w", err)
	}
	slog.Info("connected to MinIO", "endpoint", endpoint, "useSSL", useSSL)
	return client, nil
}
