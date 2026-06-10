package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func newTestRouter(t *testing.T) (*gin.Engine, *MockavatarStore, *fakeBlobStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctrl := gomock.NewController(t)
	store := NewMockavatarStore(ctrl)
	blobs := &fakeBlobStore{}
	h := newHandler(store, blobs, &config{
		SiteID:               "s1",
		EmployeePhotoBaseURL: "https://photos.example.com",
		CacheMaxAgeSeconds:   3600,
		AvatarBucket:         "avatars",
		ClusterDomains:       map[string]string{"s2": "https://avatar-s2"},
	})
	r := gin.New()
	registerRoutes(r, h)
	registerUploadRoutes(r, h)
	return r, store, blobs
}

// fakeBlobStore is an in-memory blobStore for handler tests.
type fakeBlobStore struct {
	objects map[string][]byte
	info    map[string]blobInfo
	putErr  error
}

func (f *fakeBlobStore) Get(_ context.Context, key string) (io.ReadCloser, blobInfo, error) {
	b, ok := f.objects[key]
	if !ok {
		return nil, blobInfo{}, errBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), f.info[key], nil
}

func (f *fakeBlobStore) Put(_ context.Context, key string, r io.Reader, _ int64, ct string) (string, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	if f.objects == nil {
		f.objects = map[string][]byte{}
		f.info = map[string]blobInfo{}
	}
	b, _ := io.ReadAll(r)
	f.objects[key] = b
	f.info[key] = blobInfo{Size: int64(len(b)), ContentType: ct, ETag: "etag-" + key}
	return "etag-" + key, nil
}

func TestHandleHealth(t *testing.T) {
	r, _, _ := newTestRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

// silence unused import until later tasks use it
var _ = model.AvatarSubjectBot
