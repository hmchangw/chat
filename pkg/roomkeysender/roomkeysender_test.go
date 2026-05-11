package roomkeysender_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeysender"
)

// mockPublisher captures the subject and data from the last Publish call.
type mockPublisher struct {
	subject string
	data    []byte
	err     error // error to return from Publish
}

func (m *mockPublisher) Publish(subject string, data []byte) error {
	m.subject = subject
	m.data = data
	return m.err
}

// multiPublisher captures all Publish calls for multi-send assertions.
type multiPublisher struct {
	payloads [][]byte
}

func (m *multiPublisher) Publish(_ string, data []byte) error {
	m.payloads = append(m.payloads, append([]byte(nil), data...))
	return nil
}

func TestSender_DoesNotMutateInputTimestamp(t *testing.T) {
	pub := &multiPublisher{}
	s := roomkeysender.NewSender(pub)

	// Pass by value — language semantics guarantee no mutation; test serves as documentation.
	evt := model.RoomKeyEvent{
		RoomID:     "r1",
		Version:    1,
		PublicKey:  []byte("pk"),
		PrivateKey: []byte("sk"),
		Timestamp:  0,
	}
	require.NoError(t, s.Send("alice", evt))
	require.NoError(t, s.Send("bob", evt))

	// Caller's value must not be mutated (by-value semantics guarantee this).
	assert.EqualValues(t, 0, evt.Timestamp, "Send must not mutate caller's Timestamp")

	// Each published payload should carry its own timestamp.
	require.Len(t, pub.payloads, 2)
	var msg1, msg2 model.RoomKeyEvent
	require.NoError(t, json.Unmarshal(pub.payloads[0], &msg1))
	require.NoError(t, json.Unmarshal(pub.payloads[1], &msg2))
	assert.Greater(t, msg1.Timestamp, int64(0))
	assert.Greater(t, msg2.Timestamp, int64(0))
}

func TestSender_Send(t *testing.T) {
	pub65 := make([]byte, 65)
	pub65[0] = 0x04
	for i := 1; i < 65; i++ {
		pub65[i] = byte(i)
	}
	priv32 := make([]byte, 32)
	for i := range priv32 {
		priv32[i] = byte(i + 100)
	}

	tests := []struct {
		name       string
		account    string
		evt        model.RoomKeyEvent
		publishErr error
		wantSubj   string
		wantErr    string
	}{
		{
			name:    "valid send",
			account: "alice",
			evt: model.RoomKeyEvent{
				RoomID:     "room-1",
				Version:    0,
				PublicKey:  pub65,
				PrivateKey: priv32,
			},
			wantSubj: "chat.user.alice.event.room.key",
		},
		{
			name:    "different user produces different subject",
			account: "bob",
			evt: model.RoomKeyEvent{
				RoomID:     "room-2",
				Version:    1,
				PublicKey:  []byte{0x04, 0x01},
				PrivateKey: []byte{0x0a},
			},
			wantSubj: "chat.user.bob.event.room.key",
		},
		{
			name:    "publish error is wrapped and returned",
			account: "carol",
			evt: model.RoomKeyEvent{
				RoomID:     "room-3",
				Version:    2,
				PublicKey:  []byte{0x04},
				PrivateKey: []byte{0x01},
			},
			publishErr: errors.New("connection lost"),
			wantErr:    "publish room key event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &mockPublisher{err: tt.publishErr}
			sender := roomkeysender.NewSender(pub)

			err := sender.Send(tt.account, tt.evt)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.ErrorIs(t, err, tt.publishErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSubj, pub.subject)

			// Verify payload round-trips correctly.
			var got model.RoomKeyEvent
			require.NoError(t, json.Unmarshal(pub.data, &got))
			assert.Equal(t, tt.evt.RoomID, got.RoomID)
			assert.Equal(t, tt.evt.Version, got.Version)
			assert.Equal(t, tt.evt.PublicKey, got.PublicKey)
			assert.Equal(t, tt.evt.PrivateKey, got.PrivateKey)
			assert.Greater(t, got.Timestamp, int64(0))
		})
	}
}
