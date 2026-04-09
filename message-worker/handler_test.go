package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessMessage_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	msg := model.Message{
		ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	enc.EXPECT().Encrypt(gomock.Any(), "r1", "hello").Return("enc:v0:cipher", nil)

	expectedMsg := msg
	expectedMsg.Content = "enc:v0:cipher"
	store.EXPECT().SaveMessage(gomock.Any(), expectedMsg).Return(nil)

	h := NewHandler(store, enc)
	data, _ := json.Marshal(evt)
	require.NoError(t, h.processMessage(context.Background(), data))
}

func TestHandler_ProcessMessage_SaveError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	msg := model.Message{
		ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	enc.EXPECT().Encrypt(gomock.Any(), "r1", "hello").Return("enc:v0:cipher", nil)
	store.EXPECT().SaveMessage(gomock.Any(), gomock.Any()).Return(fmt.Errorf("cassandra unavailable"))

	h := NewHandler(store, enc)
	data, _ := json.Marshal(evt)
	err := h.processMessage(context.Background(), data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save message")
}

func TestHandler_ProcessMessage_MalformedJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	h := NewHandler(store, enc)
	err := h.processMessage(context.Background(), []byte("{invalid"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal message event")
}

func TestHandler_ProcessMessage_EncryptError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	enc := NewMockContentEncryptor(ctrl)

	msg := model.Message{
		ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	enc.EXPECT().Encrypt(gomock.Any(), "r1", "hello").Return("", fmt.Errorf("key not found"))

	h := NewHandler(store, enc)
	data, _ := json.Marshal(evt)
	err := h.processMessage(context.Background(), data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypt message content")
}
