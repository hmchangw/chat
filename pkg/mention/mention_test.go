package mention

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		accounts   []string
		mentionAll bool
	}{
		{name: "no mentions", content: "hello world", accounts: nil, mentionAll: false},
		{name: "single mention", content: "hello @bob", accounts: []string{"bob"}, mentionAll: false},
		{name: "multiple mentions", content: "@alice check with @bob", accounts: []string{"alice", "bob"}, mentionAll: false},
		{name: "mention at start", content: "@alice hello", accounts: []string{"alice"}, mentionAll: false},
		{name: "email-style mention", content: "ping @user@domain.com", accounts: []string{"user@domain.com"}, mentionAll: false},
		{name: "quoted reply prefix", content: ">@alice this is quoted", accounts: []string{"alice"}, mentionAll: false},
		{name: "duplicates deduplicated", content: "@bob and @bob again", accounts: []string{"bob"}, mentionAll: false},
		{name: "dots and hyphens", content: "cc @first.last and @my-user", accounts: []string{"first.last", "my-user"}, mentionAll: false},
		{name: "empty content", content: "", accounts: nil, mentionAll: false},
		{name: "trailing period not captured", content: "hey @bob. check this", accounts: []string{"bob"}, mentionAll: false},
		{name: "@all lowercase", content: "hey @all check this", accounts: nil, mentionAll: true},
		{name: "@All uppercase", content: "attention @All everyone", accounts: nil, mentionAll: true},
		{name: "@here lowercase", content: "look @here please", accounts: nil, mentionAll: true},
		{name: "@HERE uppercase", content: "look @HERE please", accounts: nil, mentionAll: true},
		{name: "@all and individual", content: "@All and @alice", accounts: []string{"alice"}, mentionAll: true},
		{name: "case-insensitive dedup", content: "@alice @Alice", accounts: []string{"alice"}, mentionAll: false},
		{name: "mixed case lowered", content: "hey @BOB", accounts: []string{"bob"}, mentionAll: false},
		{name: "partial email@all not detected", content: "email@all.com", accounts: []string{"all.com"}, mentionAll: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.content)
			assert.Equal(t, tt.accounts, result.Accounts)
			assert.Equal(t, tt.mentionAll, result.MentionAll)
		})
	}
}
