package stream

import "fmt"

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

func Fanout(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("FANOUT_%s", siteID),
		Subjects: []string{fmt.Sprintf("fanout.%s.>", siteID)},
	}
}

func Rooms(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("ROOMS_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.user.*.request.room.*.%s.member.>", siteID)},
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
