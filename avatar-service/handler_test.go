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

func TestEndpoint1_UserRedirectToEmployeePhoto(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().EmployeeID(gomock.Any(), "alice").Return("E123", true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/alice", nil))
	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Equal(t, "https://photos.example.com/xxxPhoto/po/E123_120.JPG", w.Header().Get("Location"))
}

func TestEndpoint1_UserNoEmployeeID_ServesDefault(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().EmployeeID(gomock.Any(), "alice").Return("", false, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/alice", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, w.Body.String(), "<svg")
}

func TestEndpoint1_BotLocalCustomImage_Streams(t *testing.T) {
	r, store, blobs := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s1", true, nil)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectBot, "helper.bot").
		Return(&model.Avatar{MinioKey: "bot/helper.bot", ETag: `"e1"`}, true, nil)
	blobs.objects = map[string][]byte{"bot/helper.bot": []byte("PNG")}
	blobs.info = map[string]blobInfo{"bot/helper.bot": {Size: 3, ContentType: "image/png", ETag: `"e1"`}}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
	assert.Equal(t, "PNG", w.Body.String())
}

func TestEndpoint1_BotCustomImage_NotModified(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s1", true, nil)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectBot, "helper.bot").
		Return(&model.Avatar{MinioKey: "bot/helper.bot", ETag: `"e1"`}, true, nil)
	req := httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot", nil)
	req.Header.Set("If-None-Match", `"e1"`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotModified, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestEndpoint1_BotNoRecord_ServesDefault(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("", false, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/svg+xml", w.Header().Get("Content-Type"))
}

func TestEndpoint1_BotRemoteCluster_Redirects(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s2", true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot", nil))
	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Equal(t, "https://avatar-s2/avatar/v1/helper.bot?fwd=1", w.Header().Get("Location"))
}

func TestEndpoint1_BotSiteidHint_SkipsBotSite(t *testing.T) {
	r, store, _ := newTestRouter(t)
	_ = store // no BotSite EXPECT — the hint must skip the lookup
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot?siteid=s2", nil))
	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Equal(t, "https://avatar-s2/avatar/v1/helper.bot?fwd=1", w.Header().Get("Location"))
}

func TestEndpoint1_BotRemoteWithFwd_NoReRedirect(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s2", true, nil)
	store.EXPECT().Avatar(gomock.Any(), model.AvatarSubjectBot, "helper.bot").Return(nil, false, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatar/v1/helper.bot?fwd=1", nil))
	assert.Equal(t, http.StatusOK, w.Code) // served default locally despite remote site
}
