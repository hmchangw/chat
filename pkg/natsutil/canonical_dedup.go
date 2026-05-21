// canonical_dedup.go: Nats-Msg-Id composition for the MESSAGES_CANONICAL stream.
// Sits alongside OutboxDedupID — same concern (JetStream dedup keys), shared
// home so the convention is discoverable and uniform across publishers.
package natsutil

import (
	"fmt"

	"github.com/hmchangw/chat/pkg/model"
)

// CanonicalDedupID returns the Nats-Msg-Id for a canonical message event
// published to the MESSAGES_CANONICAL stream. The key shape depends on the
// event type:
//
//   - EventCreated:  "<messageID>"
//   - EventUpdated:  "<messageID>:updated:<editedAtUnixMilli>"
//   - EventDeleted:  "<messageID>:deleted"
//
// The op suffix keeps the three keyspaces disjoint so a .updated/.deleted
// publish can't collide with the same message's earlier .created within the
// stream's dedup window. For .updated the editedAtMs suffix gives each
// distinct edit its own key. Panics on unsupported event types — adding a new
// canonical event type must update this helper.
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
