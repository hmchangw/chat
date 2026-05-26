package integrationsuite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/subject"
)

// threadMessagesRequest mirrors history-service's
// internal/models.GetThreadMessagesRequest (that package is internal and
// cannot be imported; the JSON contract is the boundary the suite tests).
type threadMessagesRequest struct {
	ThreadMessageID string `json:"threadMessageId"`
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit"`
}

// loadHistoryRequest mirrors history-service's LoadHistoryRequest.
type loadHistoryRequest struct {
	Limit int `json:"limit"`
}

// loadNextMessagesRequest mirrors history-service's LoadNextMessagesRequest.
type loadNextMessagesRequest struct {
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

// loadSurroundingMessagesRequest mirrors history-service's LoadSurroundingMessagesRequest.
type loadSurroundingMessagesRequest struct {
	MessageID string `json:"messageId"`
	Limit     int    `json:"limit"`
}

// getMessageByIDRequest mirrors history-service's GetMessageByIDRequest.
type getMessageByIDRequest struct {
	MessageID string `json:"messageId"`
}

// getThreadParentMessagesRequest mirrors history-service's GetThreadParentMessagesRequest.
type getThreadParentMessagesRequest struct {
	Filter string `json:"filter"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func registerHistorySteps(ctx *godog.ScenarioContext) {
	// Existing steps (E6, E7).
	ctx.Step(`^"([^"]+)" requests thread messages with no thread message id in room "([^"]+)"$`, requestsThreadNoID)
	ctx.Step(`^"([^"]+)" requests thread messages for an unknown message in room "([^"]+)"$`, requestsThreadUnknownMessage)

	// Step 64: load history.
	ctx.Step(`^"([^"]+)" requests history for room "([^"]+)"$`, requestsHistoryForRoom)

	// Step 65: load next messages (no cursor).
	ctx.Step(`^"([^"]+)" requests next messages in room "([^"]+)"$`, requestsNextMessagesInRoom)

	// Step 66: load next messages with cursor.
	ctx.Step(`^"([^"]+)" requests next messages in room "([^"]+)" with cursor "([^"]+)"$`, requestsNextMessagesInRoomWithCursor)

	// Step 67: surrounding messages with no message ID.
	ctx.Step(`^"([^"]+)" requests surrounding messages with no message id in room "([^"]+)"$`, requestsSurroundingMessagesNoMessageID)

	// Step 68: surrounding messages for unknown message.
	ctx.Step(`^"([^"]+)" requests surrounding messages for unknown message in room "([^"]+)"$`, requestsSurroundingMessagesUnknownMessage)

	// Step 69: get message by id with empty id.
	ctx.Step(`^"([^"]+)" requests message by id "" in room "([^"]+)"$`, requestsMessageByEmptyID)

	// Step 70: get message by unknown id.
	ctx.Step(`^"([^"]+)" requests message by unknown id in room "([^"]+)"$`, requestsMessageByUnknownID)

	// Step 71: edit unknown message with empty content.
	ctx.Step(`^"([^"]+)" edits unknown message in room "([^"]+)" with empty content$`, editsUnknownMessageEmptyContent)

	// Step 72: delete message with empty id.
	ctx.Step(`^"([^"]+)" deletes message "" in room "([^"]+)"$`, deletesMessageEmptyID)

	// Step 73: delete unknown message.
	ctx.Step(`^"([^"]+)" deletes unknown message in room "([^"]+)"$`, deletesUnknownMessage)

	// Step 74: get thread parent messages with filter.
	ctx.Step(`^"([^"]+)" requests thread parent messages in room "([^"]+)" with filter "([^"]+)"$`, requestsThreadParentMessagesWithFilter)

	// Step 75: get thread parent messages (default/empty filter).
	ctx.Step(`^"([^"]+)" requests thread parent messages in room "([^"]+)"$`, requestsThreadParentMessages)

	// Step 49 (alternative phrasing used in history-service.feature):
	// "edits message" (without "requests to"). Handles the empty-messageID variant
	// observed in history-service.feature lines 173 and 209.
	ctx.Step(`^"([^"]+)" edits message "([^"]*)" in room "([^"]+)" with new content "([^"]+)"$`, editsMessageInRoom)
}

// ---------------------------------------------------------------------------
// Existing steps (E6, E7).
// ---------------------------------------------------------------------------

func requestsThreadNoID(ctx context.Context, actor, roomName string) error {
	return threadMessagesFor(ctx, actor, roomName, "")
}

func requestsThreadUnknownMessage(ctx context.Context, actor, roomName string) error {
	return threadMessagesFor(ctx, actor, roomName, idgen.GenerateMessageID())
}

func threadMessagesFor(ctx context.Context, actor, roomName, threadMessageID string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(threadMessagesRequest{ThreadMessageID: threadMessageID})
	if err != nil {
		return fmt.Errorf("history: marshal thread-messages request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MsgThread(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 64: load history.
// ---------------------------------------------------------------------------

func requestsHistoryForRoom(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(loadHistoryRequest{})
	if err != nil {
		return fmt.Errorf("history: marshal load-history request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.history", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}

// ---------------------------------------------------------------------------
// Step 65: load next messages.
// ---------------------------------------------------------------------------

func requestsNextMessagesInRoom(ctx context.Context, actor, roomName string) error {
	return requestsNextMessagesInRoomWithCursor(ctx, actor, roomName, "")
}

// ---------------------------------------------------------------------------
// Step 66: load next messages with cursor.
// ---------------------------------------------------------------------------

func requestsNextMessagesInRoomWithCursor(ctx context.Context, actor, roomName, cursor string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(loadNextMessagesRequest{Cursor: cursor})
	if err != nil {
		return fmt.Errorf("history: marshal load-next-messages request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.next", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}

// ---------------------------------------------------------------------------
// Step 67: surrounding messages with no message ID.
// ---------------------------------------------------------------------------

func requestsSurroundingMessagesNoMessageID(ctx context.Context, actor, roomName string) error {
	return requestsSurroundingMessages(ctx, actor, roomName, "")
}

// ---------------------------------------------------------------------------
// Step 68: surrounding messages for unknown message.
// ---------------------------------------------------------------------------

func requestsSurroundingMessagesUnknownMessage(ctx context.Context, actor, roomName string) error {
	return requestsSurroundingMessages(ctx, actor, roomName, idgen.GenerateMessageID())
}

func requestsSurroundingMessages(ctx context.Context, actor, roomName, messageID string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(loadSurroundingMessagesRequest{MessageID: messageID})
	if err != nil {
		return fmt.Errorf("history: marshal surrounding-messages request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.surrounding", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}

// ---------------------------------------------------------------------------
// Step 69: get message by empty ID.
// ---------------------------------------------------------------------------

func requestsMessageByEmptyID(ctx context.Context, actor, roomName string) error {
	return requestsMessageByIDValue(ctx, actor, roomName, "")
}

// ---------------------------------------------------------------------------
// Step 70: get message by unknown ID.
// ---------------------------------------------------------------------------

func requestsMessageByUnknownID(ctx context.Context, actor, roomName string) error {
	return requestsMessageByIDValue(ctx, actor, roomName, idgen.GenerateMessageID())
}

func requestsMessageByIDValue(ctx context.Context, actor, roomName, messageID string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(getMessageByIDRequest{MessageID: messageID})
	if err != nil {
		return fmt.Errorf("history: marshal get-message-by-id request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MsgGet(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 71: edit unknown message with empty content.
// ---------------------------------------------------------------------------

func editsUnknownMessageEmptyContent(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(editMessageRequest{
		MessageID: idgen.GenerateMessageID(),
		NewMsg:    "",
	})
	if err != nil {
		return fmt.Errorf("history: marshal edit-unknown-empty-content request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.edit", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}

// ---------------------------------------------------------------------------
// Step 72: delete message with empty ID.
// ---------------------------------------------------------------------------

func deletesMessageEmptyID(ctx context.Context, actor, roomName string) error {
	return deletesMessageByID(ctx, actor, roomName, "")
}

// ---------------------------------------------------------------------------
// Step 73: delete unknown message.
// ---------------------------------------------------------------------------

func deletesUnknownMessage(ctx context.Context, actor, roomName string) error {
	return deletesMessageByID(ctx, actor, roomName, idgen.GenerateMessageID())
}

func deletesMessageByID(ctx context.Context, actor, roomName, messageID string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(deleteMessageRequest{MessageID: messageID})
	if err != nil {
		return fmt.Errorf("history: marshal delete-message request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.delete", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}

// ---------------------------------------------------------------------------
// Step 74: get thread parent messages with filter.
// ---------------------------------------------------------------------------

func requestsThreadParentMessagesWithFilter(ctx context.Context, actor, roomName, filter string) error {
	return requestsThreadParentMessagesImpl(ctx, actor, roomName, filter)
}

// ---------------------------------------------------------------------------
// Step 75: get thread parent messages (empty filter).
// ---------------------------------------------------------------------------

func requestsThreadParentMessages(ctx context.Context, actor, roomName string) error {
	return requestsThreadParentMessagesImpl(ctx, actor, roomName, "")
}

// editsMessageInRoom covers the alternative phrasing "edits message X in room Y
// with new content Z" used in history-service.feature (same wire path as
// requestsToEditMessage in messaging_steps_test.go).
func editsMessageInRoom(ctx context.Context, actor, messageID, roomName, newContent string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(editMessageRequest{
		MessageID: messageID,
		NewMsg:    newContent,
	})
	if err != nil {
		return fmt.Errorf("history: marshal edit-message request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.edit", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}

func requestsThreadParentMessagesImpl(ctx context.Context, actor, roomName, filter string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("history: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(getThreadParentMessagesRequest{Filter: filter})
	if err != nil {
		return fmt.Errorf("history: marshal get-thread-parent-messages request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.thread.parent", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}
