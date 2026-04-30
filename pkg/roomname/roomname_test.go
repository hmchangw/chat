package roomname

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/model"
)

func TestTruncateRunes(t *testing.T) {
	tests := map[string]struct {
		in   string
		max  int
		want string
	}{
		"shorter than max":     {"hello", 100, "hello"},
		"truncated ascii":      {"hello", 3, "hel"},
		"truncated multibyte":  {"你好世界", 2, "你好"},
		"exact length":         {"你好世界", 4, "你好世界"},
		"longer than max":      {"你好世界", 100, "你好世界"},
		"empty":                {"", 5, ""},
		"mixed ascii/chinese":  {"hello世界", 6, "hello世"},
		"single rune at limit": {"a", 1, "a"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := TruncateRunes(tc.in, tc.max)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, utf8.RuneCountInString(got), tc.max)
		})
	}
}

func TestStripAccount(t *testing.T) {
	tests := map[string]struct {
		in      []string
		account string
		want    []string
	}{
		"present":     {[]string{"alice", "bob"}, "bob", []string{"alice"}},
		"absent":      {[]string{"alice", "carol"}, "bob", []string{"alice", "carol"}},
		"all matches": {[]string{"alice", "alice"}, "alice", []string{}},
		"empty input": {[]string{}, "alice", []string{}},
		"order kept":  {[]string{"a", "b", "c", "b"}, "b", []string{"a", "c"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := StripAccount(tc.in, tc.account)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestComposeAutoName(t *testing.T) {
	tests := map[string]struct {
		users    []string
		orgs     []string
		channels []model.ChannelRef
		want     string
	}{
		"users only":    {[]string{"alice", "bob"}, nil, nil, "alice, bob"},
		"users + orgs":  {[]string{"alice"}, []string{"org-fx"}, nil, "alice, org-fx"},
		"all three":     {[]string{"alice"}, []string{"org-fx"}, []model.ChannelRef{{RoomID: "r0"}}, "alice, org-fx, r0"},
		"channels only": {nil, nil, []model.ChannelRef{{RoomID: "r0"}, {RoomID: "r1"}}, "r0, r1"},
		"all empty":     {nil, nil, nil, ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := ComposeAutoName(tc.users, tc.orgs, tc.channels)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestComposeAutoNameTruncates(t *testing.T) {
	users := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		users = append(users, "userwithlongaccountname")
	}
	got := ComposeAutoName(users, nil, nil)
	assert.Equal(t, MaxNameRunes, utf8.RuneCountInString(got))
	assert.True(t, strings.HasPrefix(got, "userwithlongaccountname"))
}
