package stream

import (
	"fmt"

	"github.com/hmchangw/chat/pkg/subject"
)

// Config holds the JetStream stream configuration parameters.
type Config struct {
	Name     string
	Subjects []string
}

func Messages(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("MESSAGES_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.user.*.room.*.%s.msg.>", siteID)},
	}
}

func MessagesCanonical(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("MESSAGES_CANONICAL_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.msg.canonical.%s.>", siteID)},
	}
}

func Rooms(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("ROOMS_%s", siteID),
		Subjects: []string{subject.RoomCanonicalWildcard(siteID)},
	}
}

func Outbox(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("OUTBOX_%s", siteID),
		Subjects: []string{fmt.Sprintf("outbox.%s.>", siteID)},
	}
}

// Inbox uses JetStream Sources from other sites' OUTBOX streams (no local subjects).
func Inbox(siteID string) Config {
	return Config{
		Name: fmt.Sprintf("INBOX_%s", siteID),
	}
}
