package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestFormatAddedSingle(t *testing.T) {
	got := formatAddedSingle(
		&model.User{EngName: "Alice", ChineseName: "愛麗絲"},
		&model.User{EngName: "Bob", ChineseName: "鮑勃"},
	)
	assert.Equal(t, "Alice 愛麗絲 added Bob 鮑勃 to the channel", got)
}

func TestFormatAddedMulti(t *testing.T) {
	got := formatAddedMulti(&model.User{EngName: "Alice", ChineseName: "愛麗絲"})
	assert.Equal(t, "Alice 愛麗絲 added members to the channel", got)
}

func TestFormatRemovedUser(t *testing.T) {
	got := formatRemovedUser(&model.User{EngName: "Bob", ChineseName: "鮑勃"})
	assert.Equal(t, "Bob 鮑勃 has been removed from the channel", got)
}

func TestFormatRemovedOrg(t *testing.T) {
	got := formatRemovedOrg("Engineering")
	assert.Equal(t, "Engineering has been removed from the channel", got)
}

func TestFormatLeft(t *testing.T) {
	got := formatLeft(&model.User{EngName: "Bob", ChineseName: "鮑勃"})
	assert.Equal(t, "Bob 鮑勃 left the channel", got)
}

func TestFormatLeft_TrimsEmptyNameSide(t *testing.T) {
	// Spec §2.6: TrimSpace(EngName + " " + ChineseName) — when one side is empty,
	// the result still has no leading/trailing whitespace. Callers must reject
	// fully-empty inputs upstream; this test pins the trim behavior only.
	assert.Equal(t, "Bob left the channel", formatLeft(&model.User{EngName: "Bob"}))
	assert.Equal(t, "鮑勃 left the channel", formatLeft(&model.User{ChineseName: "鮑勃"}))
}

func TestValidateUserNames(t *testing.T) {
	cases := []struct {
		name    string
		user    model.User
		role    string
		roomID  string
		wantErr bool
		wantMsg string
	}{
		{
			name: "both names set",
			user: model.User{Account: "alice", EngName: "Alice", ChineseName: "愛"},
			role: "user", roomID: "r1",
			wantErr: false,
		},
		{
			name: "empty EngName",
			user: model.User{Account: "bob", EngName: "", ChineseName: "鮑"},
			role: "user", roomID: "r1",
			wantErr: true, wantMsg: "user bob missing required name fields (room r1)",
		},
		{
			name: "empty ChineseName",
			user: model.User{Account: "bob", EngName: "Bob", ChineseName: ""},
			role: "user", roomID: "r1",
			wantErr: true, wantMsg: "user bob missing required name fields (room r1)",
		},
		{
			name: "both empty",
			user: model.User{Account: "bob", EngName: "", ChineseName: ""},
			role: "user", roomID: "r1",
			wantErr: true, wantMsg: "user bob missing required name fields (room r1)",
		},
		{
			name: "requester role label propagated",
			user: model.User{Account: "alice", EngName: ""},
			role: "requester", roomID: "r2",
			wantErr: true, wantMsg: "requester alice missing required name fields (room r2)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUserNames(&tc.user, tc.role, tc.roomID)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, errPermanent), "validation failure must be permanent")
			assert.Equal(t, tc.wantMsg, err.Error())
		})
	}
}
