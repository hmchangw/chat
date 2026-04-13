package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

const (
	sessionCookie  = "mchat-session"
	requestTimeout = 5 * time.Second
)

type handler struct {
	cfg        config
	nc         *nats.Conn
	sm         *sessionManager
	httpClient *http.Client
}

func newHandler(cfg config, nc *nats.Conn, sm *sessionManager) *handler {
	return &handler{
		cfg: cfg,
		nc:  nc,
		sm:  sm,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// healthz returns 200 OK.
func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// login authenticates via Keycloak password grant, then calls auth-service.
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	// Step 1: Keycloak password grant.
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		h.cfg.KeycloakURL, h.cfg.KeycloakRealm)

	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {h.cfg.KeycloakClientID},
		"username":   {req.Username},
		"password":   {req.Password},
	}

	tokenResp, err := h.httpClient.PostForm(tokenURL, form)
	if err != nil {
		slog.Error("keycloak token request", "error", err)
		writeError(w, http.StatusBadGateway, "failed to reach Keycloak")
		return
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		slog.Warn("keycloak auth failed", "status", tokenResp.StatusCode, "body", string(body))
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	var tokenData struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		slog.Error("decode keycloak response", "error", err)
		writeError(w, http.StatusBadGateway, "invalid Keycloak response")
		return
	}

	// Step 2: Generate ephemeral NKey pair.
	kp, err := nkeys.CreateUser()
	if err != nil {
		slog.Error("create nkey", "error", err)
		writeError(w, http.StatusInternalServerError, "key generation failed")
		return
	}
	pubKey, err := kp.PublicKey()
	if err != nil {
		slog.Error("get nkey public key", "error", err)
		writeError(w, http.StatusInternalServerError, "key generation failed")
		return
	}

	// Step 3: Call auth-service.
	authReqBody, _ := json.Marshal(map[string]string{
		"ssoToken":      tokenData.AccessToken,
		"natsPublicKey": pubKey,
	})

	authURL := h.cfg.AuthServiceURL + "/auth"
	authReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, authURL, strings.NewReader(string(authReqBody)))
	if err != nil {
		slog.Error("build auth request", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	authReq.Header.Set("Content-Type", "application/json")

	authResp, err := h.httpClient.Do(authReq)
	if err != nil {
		slog.Error("auth-service request", "error", err)
		writeError(w, http.StatusBadGateway, "failed to reach auth service")
		return
	}
	defer authResp.Body.Close()

	if authResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(authResp.Body)
		slog.Warn("auth-service failed", "status", authResp.StatusCode, "body", string(body))
		writeError(w, http.StatusUnauthorized, "auth service rejected token")
		return
	}

	var authData struct {
		NATSJWT string   `json:"natsJwt"`
		User    userInfo `json:"user"`
	}
	if err := json.NewDecoder(authResp.Body).Decode(&authData); err != nil {
		slog.Error("decode auth response", "error", err)
		writeError(w, http.StatusBadGateway, "invalid auth service response")
		return
	}

	// Step 4: Create session with NATS subscription.
	sess, err := h.sm.create(h.nc, authData.User.Account, authData.User)
	if err != nil {
		slog.Error("create session", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sess.id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"user":      authData.User,
		"sessionId": sess.id,
	})
}

// logout destroys the session.
func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	sess := h.getSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	h.sm.destroy(sess.id)

	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// listRooms returns the user's rooms via NATS request/reply.
func (h *handler) listRooms(w http.ResponseWriter, r *http.Request) {
	sess := h.getSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	subj := subject.RoomsList(sess.account)
	msg, err := h.nc.Request(subj, nil, requestTimeout)
	if err != nil {
		slog.Error("list rooms request", "error", err, "account", sess.account)
		writeError(w, http.StatusBadGateway, "failed to list rooms")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(msg.Data) //nolint:errcheck
}

// createRoom creates a new room via NATS request/reply.
func (h *handler) createRoom(w http.ResponseWriter, r *http.Request) {
	sess := h.getSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	var req struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		Members []string `json:"members,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	roomType := model.RoomType(req.Type)
	if roomType == "" {
		roomType = model.RoomTypeGroup
	}

	createReq := model.CreateRoomRequest{
		Name:             req.Name,
		Type:             roomType,
		CreatedBy:        sess.userInfo.EngName,
		CreatedByAccount: sess.account,
		SiteID:           h.cfg.SiteID,
		Members:          req.Members,
	}

	data, err := json.Marshal(createReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal error")
		return
	}

	subj := subject.RoomsCreate(sess.account)
	msg, err := h.nc.Request(subj, data, requestTimeout)
	if err != nil {
		slog.Error("create room request", "error", err, "account", sess.account)
		writeError(w, http.StatusBadGateway, "failed to create room")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(msg.Data) //nolint:errcheck
}

// inviteMember invites a user to a room via NATS request/reply.
func (h *handler) inviteMember(w http.ResponseWriter, r *http.Request) {
	sess := h.getSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	roomID := r.PathValue("id")
	if roomID == "" {
		writeError(w, http.StatusBadRequest, "room ID required")
		return
	}

	var req struct {
		InviteeAccount string `json:"inviteeAccount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	inviteReq := model.InviteMemberRequest{
		InviteeAccount: req.InviteeAccount,
		RoomID:         roomID,
		SiteID:         h.cfg.SiteID,
		Timestamp:      time.Now().UTC().UnixMilli(),
	}

	data, err := json.Marshal(inviteReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal error")
		return
	}

	subj := subject.MemberInvite(sess.account, roomID, h.cfg.SiteID)
	msg, err := h.nc.Request(subj, data, requestTimeout)
	if err != nil {
		slog.Error("invite member request", "error", err, "account", sess.account, "roomID", roomID)
		writeError(w, http.StatusBadGateway, "failed to invite member")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(msg.Data) //nolint:errcheck
}

// sendMessage publishes a message to the MESSAGES stream and waits for gatekeeper reply.
func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	sess := h.getSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	roomID := r.PathValue("id")
	if roomID == "" {
		writeError(w, http.StatusBadRequest, "room ID required")
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	msgID := uuid.New().String()
	requestID := uuid.New().String()

	sendReq := model.SendMessageRequest{
		ID:        msgID,
		Content:   req.Content,
		RequestID: requestID,
	}

	data, err := json.Marshal(sendReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal error")
		return
	}

	// Subscribe to response subject before publishing.
	respSubj := subject.UserResponse(sess.account, requestID)
	respCh := make(chan *nats.Msg, 1)
	sub, err := h.nc.ChanSubscribe(respSubj, respCh)
	if err != nil {
		slog.Error("subscribe response", "error", err, "subject", respSubj)
		writeError(w, http.StatusInternalServerError, "failed to setup response listener")
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	// Publish to MESSAGES stream subject.
	pubSubj := subject.MsgSend(sess.account, roomID, h.cfg.SiteID)
	if err := h.nc.Publish(pubSubj, data); err != nil {
		slog.Error("publish message", "error", err, "subject", pubSubj)
		writeError(w, http.StatusBadGateway, "failed to publish message")
		return
	}

	// Wait for gatekeeper reply.
	select {
	case msg := <-respCh:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(msg.Data) //nolint:errcheck
	case <-time.After(requestTimeout):
		writeError(w, http.StatusGatewayTimeout, "message processing timed out")
	case <-r.Context().Done():
		writeError(w, http.StatusRequestTimeout, "request cancelled")
	}
}

// events streams SSE events to the client.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	sess := h.getSession(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan sseEvent, 64)
	clientID := sess.addSSEClient(ch)
	defer sess.removeSSEClient(clientID)

	for {
		select {
		case evt := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, evt.Data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// status returns current connection info.
func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	sess := h.getSession(r)

	resp := map[string]any{
		"natsConnected": h.nc.IsConnected(),
	}
	if sess != nil {
		resp["loggedIn"] = true
		resp["user"] = sess.userInfo
	} else {
		resp["loggedIn"] = false
	}

	writeJSON(w, http.StatusOK, resp)
}

// getSession extracts the session from the request cookie.
func (h *handler) getSession(r *http.Request) *session {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	return h.sm.get(cookie.Value)
}

// writeJSON marshals v as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
