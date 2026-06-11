package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/errcode/errtest"
	pkgoidc "github.com/hmchangw/chat/pkg/oidc"
)

// fakeVerifier implements TokenVerifier (mirrors auth-service's fakeValidator).
type fakeVerifier struct {
	preferredUsername string
	name              string
	expired           bool
	invalid           bool
}

func (f *fakeVerifier) Validate(_ context.Context, _ string) (pkgoidc.Claims, error) {
	if f.expired {
		return pkgoidc.Claims{}, pkgoidc.ErrTokenExpired
	}
	if f.invalid {
		return pkgoidc.Claims{}, fmt.Errorf("oidc token verification failed: bad signature")
	}
	return pkgoidc.Claims{PreferredUsername: f.preferredUsername, Name: f.name}, nil
}

// fakeForwarder captures the forward call and returns a canned upstream reply.
type fakeForwarder struct {
	status  int
	body    []byte
	err     error
	called  bool
	gotURL  string
	gotBody []byte
}

func (f *fakeForwarder) Forward(_ context.Context, authURL string, body []byte) (int, []byte, error) {
	f.called = true
	f.gotURL = authURL
	f.gotBody = body
	if f.err != nil {
		return 0, nil, f.err
	}
	return f.status, f.body, nil
}

func mustUserNKey(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateUser()
	require.NoError(t, err, "create user key")
	pub, err := kp.PublicKey()
	require.NoError(t, err, "public key")
	return pub
}

const upstreamOK = `{"natsJwt":"jwt-from-upstream","user":{"email":"alice@example.com","account":"alice","employeeId":"E001","engName":"Alice","chineseName":"","deptName":"Eng","deptId":"ENG"}}`

// testParams returns a ready-to-mutate params set: alice→site-a, all maps populated.
func testParams(t *testing.T) PortalHandlerParams {
	t.Helper()
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
		{Account: "alice", EmployeeID: "E001", SiteID: "site-a"},
		{Account: "carol", EmployeeID: "E003", SiteID: ""},        // not ready: empty siteId
		{Account: "dave", EmployeeID: "E004", SiteID: "site-zzz"}, // not ready: unconfigured site
		{Account: "erin", EmployeeID: "E005", SiteID: "site-c"},   // not ready: auth URL configured, nats URL missing
	}, nil).AnyTimes()
	dir := newDirMap()
	_, err := dir.Reload(context.Background(), store)
	require.NoError(t, err)

	return PortalHandlerParams{
		Verifier:  &fakeVerifier{preferredUsername: "alice"},
		Store:     store,
		Dir:       dir,
		Forwarder: &fakeForwarder{status: http.StatusOK, body: []byte(upstreamOK)},
		SiteAuthURLs: map[string]string{
			"site-a": "http://auth-a:8080",
			"site-b": "http://auth-b:8080",
			"site-c": "http://auth-c:8080",
		},
		SiteNATSURLs: map[string]string{
			"site-a": "wss://nats-a:9222",
			"site-b": "wss://nats-b:9222",
		},
		SiteFrontendURLs: map[string]string{
			"site-a": "https://chat-a.example.com",
			"site-b": "https://chat-b.example.com",
		},
		AdminToken: "test-admin-token",
		DevMode:    false,
	}
}

func newTestRouter(t *testing.T, p *PortalHandlerParams) *gin.Engine {
	t.Helper()
	h, err := NewPortalHandler(p)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerRoutes(r, h)
	return r
}

func postSession(r *gin.Engine, body, origin string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/nats-jwt", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestHandleSessionNATSJWT_Success(t *testing.T) {
	p := testParams(t)
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, &p)
	userPub := mustUserNKey(t)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+userPub+`"}`, "https://chat-a.example.com")
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		NATSJWT string          `json:"natsJwt"`
		NATSURL string          `json:"natsUrl"`
		User    json.RawMessage `json:"user"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "jwt-from-upstream", resp.NATSJWT)
	assert.Equal(t, "wss://nats-a:9222", resp.NATSURL)
	assert.Contains(t, string(resp.User), `"account":"alice"`)

	// Forwarded to the home site's auth-service with the prod body shape.
	assert.Equal(t, "http://auth-a:8080", fwd.gotURL)
	assert.JSONEq(t, `{"ssoToken":"tok","natsPublicKey":"`+userPub+`"}`, string(fwd.gotBody))
}

func TestHandleSessionNATSJWT_AccountFallsBackToName(t *testing.T) {
	p := testParams(t)
	p.Verifier = &fakeVerifier{preferredUsername: "", name: "alice"}
	r := newTestRouter(t, &p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleSessionNATSJWT_BlankAccountRejected(t *testing.T) {
	p := testParams(t)
	p.Verifier = &fakeVerifier{}
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, &p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthInvalidToken)
	assert.False(t, fwd.called)
}

func TestHandleSessionNATSJWT_TokenErrors(t *testing.T) {
	cases := []struct {
		name       string
		verifier   *fakeVerifier
		wantReason errcode.Reason
	}{
		{"expired token", &fakeVerifier{expired: true}, errcode.AuthTokenExpired},
		{"invalid token", &fakeVerifier{invalid: true}, errcode.AuthInvalidToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			p.Verifier = tc.verifier
			r := newTestRouter(t, &p)
			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeUnauthenticated)
			errtest.AssertReason(t, w.Body.Bytes(), tc.wantReason)
		})
	}
}

func TestHandleSessionNATSJWT_BadInput(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantReason errcode.Reason
	}{
		{"missing ssoToken", `{"natsPublicKey":"UWHATEVER"}`, errcode.AuthMissingFields},
		{"missing natsPublicKey", `{"ssoToken":"tok"}`, errcode.AuthMissingFields},
		{"empty body", `{}`, errcode.AuthMissingFields},
		{"invalid nkey", `{"ssoToken":"tok","natsPublicKey":"NOT-A-KEY"}`, errcode.AuthInvalidNKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			r := newTestRouter(t, &p)
			w := postSession(r, tc.body, "")
			assert.Equal(t, http.StatusBadRequest, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeBadRequest)
			errtest.AssertReason(t, w.Body.Bytes(), tc.wantReason)
		})
	}
}

func TestHandleSessionNATSJWT_NotReady(t *testing.T) {
	cases := []struct {
		name    string
		account string // which account the verifier yields
	}{
		{"no directory record", "mallory"},
		{"empty siteId", "carol"},
		{"siteId not configured", "dave"},
		{"siteId missing nats url", "erin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			p.Verifier = &fakeVerifier{preferredUsername: tc.account}
			fwd := p.Forwarder.(*fakeForwarder)
			r := newTestRouter(t, &p)

			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
			assert.Equal(t, http.StatusForbidden, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeForbidden)
			errtest.AssertReason(t, w.Body.Bytes(), errcode.PortalAccountNotReady)
			assert.False(t, fwd.called, "gate must stop the request before any forward")
		})
	}
}

func TestHandleSessionNATSJWT_CrossSiteRedirect(t *testing.T) {
	p := testParams(t) // alice's home is site-a
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, &p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "https://chat-b.example.com")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"redirectTo":"https://chat-a.example.com"}`, w.Body.String())
	assert.False(t, fwd.called, "redirect must not mint")
}

func TestHandleSessionNATSJWT_NoHomeFrontendSkipsRedirect(t *testing.T) {
	p := testParams(t)                                                             // alice's home is site-a
	p.SiteFrontendURLs = map[string]string{"site-b": "https://chat-b.example.com"} // home has no frontend entry
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, &p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "https://chat-b.example.com")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, fwd.called, "no home frontend configured → proceed, don't redirect")
	assert.NotContains(t, w.Body.String(), "redirectTo")
}

func TestHandleSessionNATSJWT_OriginVariantsProceed(t *testing.T) {
	cases := []struct{ name, origin string }{
		{"same-site origin", "https://chat-a.example.com"},
		{"unknown origin", "https://not-a-frontend.example.com"},
		{"no origin", ""},
		{"malformed origin", "not a url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			fwd := p.Forwarder.(*fakeForwarder)
			r := newTestRouter(t, &p)
			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, tc.origin)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.True(t, fwd.called)
			assert.NotContains(t, w.Body.String(), "redirectTo")
		})
	}
}

func TestHandleSessionNATSJWT_UpstreamErrorEnvelopeRelayed(t *testing.T) {
	p := testParams(t)
	p.Forwarder = &fakeForwarder{
		status: http.StatusUnauthorized,
		body:   []byte(`{"error":"SSO token has expired, please re-login","code":"unauthenticated","reason":"sso_token_expired"}`),
	}
	r := newTestRouter(t, &p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeUnauthenticated)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthTokenExpired)
}

func TestHandleSessionNATSJWT_UpstreamFailuresCollapseToInternal(t *testing.T) {
	cases := []struct {
		name string
		fwd  *fakeForwarder
	}{
		{"transport error", &fakeForwarder{err: fmt.Errorf("dial tcp: connection refused")}},
		{"non-envelope error body", &fakeForwarder{status: http.StatusBadGateway, body: []byte("oops")}},
		{"200 missing natsJwt", &fakeForwarder{status: http.StatusOK, body: []byte(`{"user":{}}`)}},
		{"200 garbage body", &fakeForwarder{status: http.StatusOK, body: []byte("not json")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams(t)
			p.Forwarder = tc.fwd
			r := newTestRouter(t, &p)
			w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
			require.Equal(t, http.StatusInternalServerError, w.Code)
			errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeInternal)
			assert.NotContains(t, w.Body.String(), "connection refused", "cause must never reach the client")
		})
	}
}

func TestHandleSessionNATSJWT_DevMode(t *testing.T) {
	p := testParams(t)
	p.DevMode = true
	p.Verifier = nil // dev mode never verifies
	fwd := p.Forwarder.(*fakeForwarder)
	r := newTestRouter(t, &p)
	userPub := mustUserNKey(t)

	w := postSession(r, `{"account":"alice","natsPublicKey":"`+userPub+`"}`, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"natsUrl":"wss://nats-a:9222"`)
	assert.JSONEq(t, `{"account":"alice","natsPublicKey":"`+userPub+`"}`, string(fwd.gotBody))
}

func TestHandleSessionNATSJWT_DevMode_BadInput(t *testing.T) {
	p := testParams(t)
	p.DevMode = true
	p.Verifier = nil
	r := newTestRouter(t, &p)

	w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusBadRequest, w.Code, "dev mode requires the account-shape body")
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
}

func TestHandleSessionNATSJWT_DevMode_InvalidNKey(t *testing.T) {
	p := testParams(t)
	p.DevMode = true
	p.Verifier = nil
	r := newTestRouter(t, &p)

	w := postSession(r, `{"account":"alice","natsPublicKey":"NOT-A-KEY"}`, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthInvalidNKey)
}

func TestHandleSessionNATSJWT_ProdRejectsAccountOnlyBody(t *testing.T) {
	p := testParams(t)
	r := newTestRouter(t, &p)
	w := postSession(r, `{"account":"alice","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	errtest.AssertReason(t, w.Body.Bytes(), errcode.AuthMissingFields)
}

func TestHandleCacheReload(t *testing.T) {
	reload := func(r *gin.Engine, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/cache/reload", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("wrong token rejected", func(t *testing.T) {
		p := testParams(t)
		r := newTestRouter(t, &p)
		w := reload(r, "wrong")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("missing token rejected", func(t *testing.T) {
		p := testParams(t)
		r := newTestRouter(t, &p)
		w := reload(r, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("reload swaps the map", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockDirectoryStore(ctrl)
		gomock.InOrder(
			store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{}, nil),
			store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
				{Account: "alice", SiteID: "site-a"},
			}, nil),
		)
		p := testParams(t)
		p.Store = store
		p.Dir = newDirMap()
		_, err := p.Dir.Reload(context.Background(), store) // initial empty load
		require.NoError(t, err)
		r := newTestRouter(t, &p)

		// Before reload: alice unknown → 403.
		w := postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
		require.Equal(t, http.StatusForbidden, w.Code)

		w = reload(r, "test-admin-token")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"records":1`)

		// After reload: alice resolves.
		w = postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("reload failure returns 500 and keeps old map", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockDirectoryStore(ctrl)
		gomock.InOrder(
			store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
				{Account: "alice", SiteID: "site-a"},
			}, nil),
			store.EXPECT().LoadAll(gomock.Any()).Return(nil, fmt.Errorf("mongo down")),
		)
		p := testParams(t)
		p.Store = store
		p.Dir = newDirMap()
		_, err := p.Dir.Reload(context.Background(), store)
		require.NoError(t, err)
		r := newTestRouter(t, &p)

		w := reload(r, "test-admin-token")
		require.Equal(t, http.StatusInternalServerError, w.Code)
		errtest.AssertCode(t, w.Body.Bytes(), errcode.CodeInternal)

		// Old map still serves alice.
		w = postSession(r, `{"ssoToken":"tok","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestNewPortalHandler_RejectsBadFrontendURL(t *testing.T) {
	p := testParams(t)
	p.SiteFrontendURLs = map[string]string{"site-a": "no-scheme.example.com"}
	_, err := NewPortalHandler(&p)
	require.Error(t, err)
}

func TestNewPortalHandler_RejectsEmptyAdminToken(t *testing.T) {
	p := testParams(t)
	p.AdminToken = ""
	_, err := NewPortalHandler(&p)
	require.Error(t, err)
}

func TestNewPortalHandler_TrimsSiteMapWhitespace(t *testing.T) {
	p := testParams(t)
	p.SiteAuthURLs = map[string]string{" site-a ": " http://auth-a:8080 "}
	p.SiteNATSURLs = map[string]string{"site-a ": "wss://nats-a:9222 "}
	p.SiteFrontendURLs = map[string]string{" site-a": " https://chat-a.example.com"}
	h, err := NewPortalHandler(&p)
	require.NoError(t, err)
	assert.Equal(t, "http://auth-a:8080", h.siteAuth["site-a"])
	assert.Equal(t, "wss://nats-a:9222", h.siteNATS["site-a"])
	assert.Equal(t, "https://chat-a.example.com", h.siteFrontend["site-a"])
}

func TestHandleHealth(t *testing.T) {
	p := testParams(t)
	r := newTestRouter(t, &p)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
	assert.Contains(t, w.Body.String(), "records")
}

func TestHandleSessionNATSJWT_DevMode_RejectsSubjectMetachars(t *testing.T) {
	p := testParams(t)
	p.DevMode = true
	p.Verifier = nil
	r := newTestRouter(t, &p)

	for _, account := range []string{"evil*", "evil>", "evil account"} {
		w := postSession(r, `{"account":"`+account+`","natsPublicKey":"`+mustUserNKey(t)+`"}`, "")
		assert.Equal(t, http.StatusBadRequest, w.Code, account)
	}
}
