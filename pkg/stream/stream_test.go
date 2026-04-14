package stream_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/stream"
)

// TestStreamConfigs covers single-subject streams where one pattern is the
// full story. Multi-subject streams (currently just Inbox) get their own
// dedicated test so both patterns are asserted explicitly.
func TestStreamConfigs(t *testing.T) {
	siteID := "site-a"

	tests := []struct {
		name     string
		cfg      stream.Config
		wantName string
		wantSubj string
	}{
		{"Messages", stream.Messages(siteID), "MESSAGES_site-a", "chat.user.*.room.*.site-a.msg.>"},
		{"MessagesCanonical", stream.MessagesCanonical(siteID), "MESSAGES_CANONICAL_site-a", "chat.msg.canonical.site-a.>"},
		{"Rooms", stream.Rooms(siteID), "ROOMS_site-a", "chat.room.canonical.site-a.>"},
		{"Outbox", stream.Outbox(siteID), "OUTBOX_site-a", "outbox.site-a.>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cfg.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.cfg.Name, tt.wantName)
			}
			if len(tt.cfg.Subjects) != 1 || tt.cfg.Subjects[0] != tt.wantSubj {
				t.Errorf("Subjects = %v, want [%q]", tt.cfg.Subjects, tt.wantSubj)
			}
		})
	}
}

func TestInboxConfig(t *testing.T) {
	cfg := stream.Inbox("site-a")

	if cfg.Name != "INBOX_site-a" {
		t.Errorf("Name = %q, want %q", cfg.Name, "INBOX_site-a")
	}
	// Two non-overlapping patterns: local (`*`) and federated (`aggregate.>`).
	want := []string{
		"chat.inbox.site-a.*",
		"chat.inbox.site-a.aggregate.>",
	}
	if len(cfg.Subjects) != len(want) {
		t.Fatalf("Subjects len = %d, want %d: %v", len(cfg.Subjects), len(want), cfg.Subjects)
	}
	for i, w := range want {
		if cfg.Subjects[i] != w {
			t.Errorf("Subjects[%d] = %q, want %q", i, cfg.Subjects[i], w)
		}
	}
}
