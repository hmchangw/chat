package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestHandler_Connect(t *testing.T) {
	tests := []struct {
		name       string
		body       any
		setupMock  func(*MockHub)
		wantStatus int
	}{
		{
			name: "successful connect",
			body: map[string]string{"sourceURL": "nats://src:4222", "destURL": "nats://dst:4222"},
			setupMock: func(m *MockHub) {
				m.EXPECT().Connect("nats://src:4222", "nats://dst:4222").Return(nil)
				m.EXPECT().Status().Return(ConnectionStatus{
					SourceConnected: true, DestConnected: true,
					SourceURL: "nats://src:4222", DestURL: "nats://dst:4222",
				})
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing sourceURL",
			body:       map[string]string{"sourceURL": "", "destURL": "nats://dst:4222"},
			setupMock:  func(m *MockHub) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing destURL",
			body:       map[string]string{"sourceURL": "nats://src:4222", "destURL": ""},
			setupMock:  func(m *MockHub) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON body",
			body:       "not-json",
			setupMock:  func(m *MockHub) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "connection error",
			body: map[string]string{"sourceURL": "nats://src:4222", "destURL": "nats://dst:4222"},
			setupMock: func(m *MockHub) {
				m.EXPECT().Connect("nats://src:4222", "nats://dst:4222").Return(errors.New("refused"))
			},
			wantStatus: http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := NewMockHub(ctrl)
			tc.setupMock(m)

			h := newHandler(m)

			var body bytes.Buffer
			if s, ok := tc.body.(string); ok {
				body.WriteString(s)
			} else {
				require.NoError(t, json.NewEncoder(&body).Encode(tc.body))
			}

			req := httptest.NewRequest(http.MethodPost, "/api/connect", &body)
			w := httptest.NewRecorder()
			h.connect(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

func TestHandler_Disconnect(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := NewMockHub(ctrl)
	m.EXPECT().Disconnect()

	h := newHandler(m)
	req := httptest.NewRequest(http.MethodPost, "/api/disconnect", nil)
	w := httptest.NewRecorder()
	h.disconnect(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_Status(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := NewMockHub(ctrl)
	m.EXPECT().Status().Return(ConnectionStatus{
		SourceConnected: true, DestConnected: false,
		SourceURL: "nats://src:4222",
	})

	h := newHandler(m)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	h.status(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var got ConnectionStatus
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.True(t, got.SourceConnected)
	assert.False(t, got.DestConnected)
}

func TestHandler_Subscribe(t *testing.T) {
	tests := []struct {
		name       string
		body       any
		setupMock  func(*MockHub)
		wantStatus int
		wantSub    *Subscription
	}{
		{
			name: "successful subscribe",
			body: map[string]string{"subject": "chat.>"},
			setupMock: func(m *MockHub) {
				m.EXPECT().Subscribe("chat.>").Return(Subscription{ID: "sub-1", Subject: "chat.>"}, nil)
			},
			wantStatus: http.StatusCreated,
			wantSub:    &Subscription{ID: "sub-1", Subject: "chat.>"},
		},
		{
			name:       "missing subject",
			body:       map[string]string{"subject": ""},
			setupMock:  func(m *MockHub) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON",
			body:       "bad",
			setupMock:  func(m *MockHub) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "hub subscribe error",
			body: map[string]string{"subject": "chat.>"},
			setupMock: func(m *MockHub) {
				m.EXPECT().Subscribe("chat.>").Return(Subscription{}, errors.New("not connected"))
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := NewMockHub(ctrl)
			tc.setupMock(m)

			h := newHandler(m)

			var body bytes.Buffer
			if s, ok := tc.body.(string); ok {
				body.WriteString(s)
			} else {
				require.NoError(t, json.NewEncoder(&body).Encode(tc.body))
			}

			req := httptest.NewRequest(http.MethodPost, "/api/subscriptions", &body)
			w := httptest.NewRecorder()
			h.subscribe(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantSub != nil {
				var got Subscription
				require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
				assert.Equal(t, *tc.wantSub, got)
			}
		})
	}
}

func TestHandler_Unsubscribe(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		setupMock  func(*MockHub)
		wantStatus int
	}{
		{
			name: "successful unsubscribe",
			id:   "sub-1",
			setupMock: func(m *MockHub) {
				m.EXPECT().Unsubscribe("sub-1").Return(nil)
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "not found",
			id:   "sub-99",
			setupMock: func(m *MockHub) {
				m.EXPECT().Unsubscribe("sub-99").Return(errors.New("not found"))
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := NewMockHub(ctrl)
			tc.setupMock(m)

			h := newHandler(m)

			mux := http.NewServeMux()
			h.registerRoutes(mux)

			req := httptest.NewRequest(http.MethodDelete, "/api/subscriptions/"+tc.id, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

func TestHandler_ListSubscriptions(t *testing.T) {
	tests := []struct {
		name       string
		setupMock  func(*MockHub)
		wantCount  int
		wantStatus int
	}{
		{
			name: "returns subscriptions",
			setupMock: func(m *MockHub) {
				m.EXPECT().Subscriptions().Return([]Subscription{
					{ID: "s1", Subject: "chat.>"},
					{ID: "s2", Subject: "fanout.>"},
				})
			},
			wantCount:  2,
			wantStatus: http.StatusOK,
		},
		{
			name: "empty list",
			setupMock: func(m *MockHub) {
				m.EXPECT().Subscriptions().Return(nil)
			},
			wantCount:  0,
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := NewMockHub(ctrl)
			tc.setupMock(m)

			h := newHandler(m)
			req := httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil)
			w := httptest.NewRecorder()
			h.listSubscriptions(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			var got []Subscription
			require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
			assert.Len(t, got, tc.wantCount)
		})
	}
}

func TestHandler_Publish(t *testing.T) {
	tests := []struct {
		name       string
		body       any
		setupMock  func(*MockHub)
		wantStatus int
	}{
		{
			name: "successful publish",
			body: map[string]string{"subject": "chat.room.123", "payload": `{"msg":"hello"}`},
			setupMock: func(m *MockHub) {
				m.EXPECT().Publish("chat.room.123", `{"msg":"hello"}`).Return(nil)
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing subject",
			body:       map[string]string{"subject": "", "payload": "{}"},
			setupMock:  func(m *MockHub) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON",
			body:       "bad",
			setupMock:  func(m *MockHub) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "publish error",
			body: map[string]string{"subject": "chat.room.123", "payload": "{}"},
			setupMock: func(m *MockHub) {
				m.EXPECT().Publish("chat.room.123", "{}").Return(errors.New("not connected"))
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := NewMockHub(ctrl)
			tc.setupMock(m)

			h := newHandler(m)

			var body bytes.Buffer
			if s, ok := tc.body.(string); ok {
				body.WriteString(s)
			} else {
				require.NoError(t, json.NewEncoder(&body).Encode(tc.body))
			}

			req := httptest.NewRequest(http.MethodPost, "/api/publish", &body)
			w := httptest.NewRecorder()
			h.publish(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

func TestHandler_Events(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := NewMockHub(ctrl)

	// chReady signals that capturedCh has been set by the mock DoAndReturn.
	chReady := make(chan chan<- Message, 1)

	m.EXPECT().RegisterSSEClient(gomock.Any()).DoAndReturn(func(ch chan<- Message) string {
		chReady <- ch
		return "client-1"
	})
	m.EXPECT().UnregisterSSEClient("client-1")

	h := newHandler(m)

	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	req, cancel := newCancelableRequest(t, "/api/events")
	defer cancel()

	go func() {
		// Wait for the SSE handler to call RegisterSSEClient before sending.
		capturedCh := <-chReady
		capturedCh <- Message{ID: "m1", Subject: "chat.>", Payload: "{}", Timestamp: time.Now()}
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	h.events(w, req)

	body := w.Body.String()
	assert.Contains(t, body, "event: connected")
	assert.Contains(t, body, "event: message")
	assert.Contains(t, body, `"subject":"chat.>"`)
}

// flushRecorder wraps httptest.ResponseRecorder and implements http.Flusher.
type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {}

func newCancelableRequest(t *testing.T, path string) (*http.Request, context.CancelFunc) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	ctx, cancel := context.WithCancel(req.Context())
	return req.WithContext(ctx), cancel
}
