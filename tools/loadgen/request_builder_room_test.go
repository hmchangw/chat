package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestBuildRoomRequest(t *testing.T) {
	user := model.User{ID: "u-000001", Account: "user-1", SiteID: "site-local"}
	room := model.Room{ID: "room-000001", SiteID: "site-local"}
	args := roomRequestArgs{
		User: user, Room: room, SiteID: "site-local",
		WriteIDPrefix: "loadgen-",
		MemberAccount: "user-2",
	}

	t.Run("RoomsList", func(t *testing.T) {
		subj, body, err := buildRoomRequest(RoomsListKind, &args)
		require.NoError(t, err)
		assert.Equal(t, "chat.user.user-1.request.rooms.list", subj)
		// Body is empty JSON object.
		assert.True(t, len(body) <= 4, "list body should be empty / minimal; got %s", string(body))
	})

	t.Run("RoomsGet", func(t *testing.T) {
		subj, body, err := buildRoomRequest(RoomsGetKind, &args)
		require.NoError(t, err)
		assert.Equal(t, "chat.user.user-1.request.rooms.get.room-000001", subj)
		assert.True(t, len(body) <= 4, "get body should be empty / minimal; got %s", string(body))
	})

	t.Run("MemberList", func(t *testing.T) {
		subj, body, err := buildRoomRequest(MemberListKind, &args)
		require.NoError(t, err)
		assert.Equal(t, "chat.user.user-1.request.room.room-000001.site-local.member.list", subj)
		var got model.ListRoomMembersRequest
		require.NoError(t, json.Unmarshal(body, &got))
	})

	t.Run("RoomCreate", func(t *testing.T) {
		subj, body, err := buildRoomRequest(RoomCreateKind, &args)
		require.NoError(t, err)
		assert.Equal(t, "chat.user.user-1.request.room.site-local.create", subj)
		var got model.CreateRoomRequest
		require.NoError(t, json.Unmarshal(body, &got))
		assert.True(t, strings.HasPrefix(got.Name, "loadgen-"),
			"generated room name must carry the WriteIDPrefix; got %q", got.Name)
	})

	t.Run("MemberAdd", func(t *testing.T) {
		subj, body, err := buildRoomRequest(MemberAddKind, &args)
		require.NoError(t, err)
		assert.Equal(t, "chat.user.user-1.request.room.room-000001.site-local.member.add", subj)
		var got model.AddMembersRequest
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "room-000001", got.RoomID)
		assert.NotEmpty(t, got.Users, "MemberAdd should include at least one user")
	})

	t.Run("UnknownKindReturnsSentinelError", func(t *testing.T) {
		_, _, err := buildRoomRequest(roomRequestKind(99), &args)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownRoomKind),
			"unknown roomRequestKind must wrap ErrUnknownRoomKind; got %v", err)
	})
}
