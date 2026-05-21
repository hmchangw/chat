package natsutil

import (
	"fmt"

	"github.com/hmchangw/chat/pkg/model"
)

// CanonicalDedupID returns the Nats-Msg-Id for a MessageEvent published to
// MESSAGES_CANONICAL. The op suffix keeps created/updated/deleted keyspaces
// disjoint within the stream's dedup window; the editedAtMs suffix on
// updated gives each distinct edit its own key.
//
//   - EventCreated: "<messageID>"
//   - EventUpdated: "<messageID>:updated:<editedAtUnixMilli>"
//   - EventDeleted: "<messageID>:deleted"
//
// Panics on unsupported event types.
func CanonicalDedupID(evt *model.MessageEvent) string {
	switch evt.Event {
	case model.EventCreated:
		return evt.Message.ID
	case model.EventUpdated:
		return fmt.Sprintf("%s:%s:%d", evt.Message.ID, evt.Event, evt.Message.EditedAt.UnixMilli())
	case model.EventDeleted:
		return evt.Message.ID + ":" + string(evt.Event)
	default:
		panic(fmt.Sprintf("natsutil.CanonicalDedupID: unsupported event type %q", evt.Event))
	}
}
