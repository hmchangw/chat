package stream_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/stream"
)

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
		{"Inbox", stream.Inbox(siteID), "INBOX_site-a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cfg.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.cfg.Name, tt.wantName)
			}
			if tt.wantSubj != "" {
				if len(tt.cfg.Subjects) == 0 || tt.cfg.Subjects[0] != tt.wantSubj {
					t.Errorf("Subjects = %v, want [%q]", tt.cfg.Subjects, tt.wantSubj)
				}
			}
		})
	}
}
