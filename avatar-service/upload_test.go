package main

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return buf.Bytes()
}

func putReq(path string, body []byte, ct string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	return req
}

func TestUpload_MalformedBotName_400(t *testing.T) {
	r, _, _ := newTestRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/alice", pngBytes(t), "image/png")) // not a bot
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpload_UnknownBot_404(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("", false, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpload_WrongCluster_RejectsWithDomain(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s2", true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "https://avatar-s2")
}

func TestUpload_RejectSVG(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s1", true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", []byte("<svg/>"), "image/svg+xml"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpload_RejectNonImage(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s1", true, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", []byte("not an image"), "image/png"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpload_Success_StoresThenUpserts(t *testing.T) {
	r, store, blobs := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s1", true, nil)
	store.EXPECT().SetBotAvatar(gomock.Any(), gomock.Any()).DoAndReturn(func(_ any, av *model.Avatar) error {
		assert.Equal(t, "bot:helper.bot", av.ID)
		assert.Equal(t, "bot/helper.bot", av.MinioKey)
		assert.Equal(t, "image/png", av.ContentType)
		assert.NotEmpty(t, av.ETag)
		return nil
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	_, ok := blobs.objects["bot/helper.bot"]
	assert.True(t, ok, "object stored before the doc")
}

func TestUpload_BotSiteError_500(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("", false, errors.New("mongo down"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestUpload_PutError_500(t *testing.T) {
	r, store, blobs := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s1", true, nil)
	blobs.putErr = errors.New("minio down")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestUpload_SetBotAvatarError_500(t *testing.T) {
	r, store, _ := newTestRouter(t)
	store.EXPECT().BotSite(gomock.Any(), "helper.bot").Return("s1", true, nil)
	store.EXPECT().SetBotAvatar(gomock.Any(), gomock.Any()).Return(errors.New("mongo down"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, putReq("/avatar/v1/bot/helper.bot", pngBytes(t), "image/png"))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
