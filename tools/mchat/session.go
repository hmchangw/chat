package main

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// userInfo mirrors the auth-service response user object.
type userInfo struct {
	Email       string `json:"email"`
	Account     string `json:"account"`
	EmployeeID  string `json:"employeeId"`
	EngName     string `json:"engName"`
	ChineseName string `json:"chineseName"`
}

// sseEvent is a typed event pushed to SSE clients.
type sseEvent struct {
	Type string `json:"type"` // "room_event", "subscription_update", "notification", etc.
	Data []byte `json:"data"` // raw JSON payload
}

// session holds per-user state.
type session struct {
	mu         sync.RWMutex
	id         string
	account    string
	userInfo   userInfo
	sub        *nats.Subscription
	sseClients map[string]chan<- sseEvent // clientID → channel
}

func (s *session) addSSEClient(ch chan<- sseEvent) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	clientID := uuid.New().String()
	s.sseClients[clientID] = ch
	return clientID
}

func (s *session) removeSSEClient(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sseClients, clientID)
}

func (s *session) broadcast(evt sseEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.sseClients {
		select {
		case ch <- evt:
		default:
			// drop if client is slow
		}
	}
}

// sessionManager manages all active sessions.
type sessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*session // sessionID → session
}

func newSessionManager() *sessionManager {
	return &sessionManager{sessions: make(map[string]*session)}
}

func (sm *sessionManager) create(nc *nats.Conn, account string, info userInfo) (*session, error) {
	sess := &session{
		id:         uuid.New().String(),
		account:    account,
		userInfo:   info,
		sseClients: make(map[string]chan<- sseEvent),
	}

	// Subscribe to all events for this user.
	subj := "chat.user." + account + ".>"
	sub, err := nc.Subscribe(subj, func(msg *nats.Msg) {
		sess.broadcast(sseEvent{
			Type: classifySubject(msg.Subject),
			Data: msg.Data,
		})
	})
	if err != nil {
		return nil, err
	}
	sess.sub = sub

	sm.mu.Lock()
	sm.sessions[sess.id] = sess
	sm.mu.Unlock()

	slog.Info("session created", "sessionID", sess.id, "account", account, "subject", subj)
	return sess, nil
}

func (sm *sessionManager) get(sessionID string) *session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[sessionID]
}

func (sm *sessionManager) destroy(sessionID string) {
	sm.mu.Lock()
	sess, ok := sm.sessions[sessionID]
	if ok {
		delete(sm.sessions, sessionID)
	}
	sm.mu.Unlock()

	if ok && sess.sub != nil {
		if err := sess.sub.Unsubscribe(); err != nil {
			slog.Error("unsubscribe session", "error", err, "sessionID", sessionID)
		}
	}
}

func (sm *sessionManager) closeAll() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for id, sess := range sm.sessions {
		if sess.sub != nil {
			if err := sess.sub.Unsubscribe(); err != nil {
				slog.Error("unsubscribe session", "error", err, "sessionID", id)
			}
		}
	}
	sm.sessions = make(map[string]*session)
}

// classifySubject maps a NATS subject to an SSE event type.
func classifySubject(subj string) string {
	// Check more specific patterns first.
	switch {
	case strings.Contains(subj, ".event.room.metadata"):
		return "room_metadata_update"
	case strings.Contains(subj, ".event.subscription"):
		return "subscription_update"
	case strings.Contains(subj, ".event.room"):
		return "room_event"
	case strings.Contains(subj, ".notification"):
		return "notification"
	case strings.Contains(subj, ".response."):
		return "response"
	default:
		return "message"
	}
}
