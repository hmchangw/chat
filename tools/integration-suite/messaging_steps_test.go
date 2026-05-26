package integrationsuite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"
)

// editMessageRequest mirrors history-service's EditMessageRequest.
// The history-service internal models package is not importable from
// the integration suite, so we replicate the JSON contract here.
type editMessageRequest struct {
	MessageID string `json:"messageId"`
	NewMsg    string `json:"newMsg"`
}

// deleteMessageRequest mirrors history-service's DeleteMessageRequest.
type deleteMessageRequest struct {
	MessageID string `json:"messageId"`
}

func registerMessagingSteps(ctx *godog.ScenarioContext) {
	// Existing step (E5).
	ctx.Step(`^"([^"]+)" submits a message with (an empty body|an invalid message id)$`, submitsMessageWith)

	// Step 39 (Part-2): valid message submit via JetStream.
	ctx.Step(`^"([^"]+)" submits a valid message to room "([^"]+)"$`, submitsValidMessageToRoom)

	// Step 40 (Part-2): oversized content submit.
	ctx.Step(`^"([^"]+)" submits a message with content larger than 20 KB to room "([^"]+)"$`, submitsOversizedMessageToRoom)

	// Step 41 (Part-2): thread reply with parent ID but no createdAt.
	ctx.Step(`^"([^"]+)" submits a thread reply to room "([^"]+)" with threadParentMessageId set but no threadParentMessageCreatedAt$`, submitsThreadReplyNoCreatedAt)

	// Step 42 (Part-2): thread reply with malformed parent ID.
	ctx.Step(`^"([^"]+)" submits a thread reply to room "([^"]+)" with a malformed threadParentMessageId$`, submitsThreadReplyMalformedParentID)

	// Step 43 (Part-2): message quoting from a different thread.
	ctx.Step(`^"([^"]+)" submits a message to room "([^"]+)" quoting a message from a different thread$`, submitsMessageQuotingDifferentThread)

	// Step 44 (Part-2): main-room message quoting a thread reply.
	ctx.Step(`^"([^"]+)" submits a main-room message to room "([^"]+)" quoting a thread reply$`, submitsMainRoomMessageQuotingThreadReply)

	// Step 45 (Part-2): message quoting a non-existent message ID.
	ctx.Step(`^"([^"]+)" submits a message to room "([^"]+)" quoting a non-existent message id$`, submitsMessageQuotingNonExistentID)

	// Step 46 (Part-2): message from a non-member.
	ctx.Step(`^"([^"]+)" submits a message to room "([^"]+)" on behalf of a non-member$`, submitsMessageAsNonMember)

	// Step 47 (Part-2): top-level message submit.
	ctx.Step(`^"([^"]+)" submits a top-level message to room "([^"]+)"$`, submitsTopLevelMessageToRoom)

	// Step 48 (Part-2): message to wrong site subject.
	ctx.Step(`^"([^"]+)" publishes a message on a subject for site "([^"]+)" to room "([^"]+)"$`, publishesMessageOnWrongSiteSubject)

	// Step 49 (Part-1): edit message request/reply.
	ctx.Step(`^"([^"]+)" requests to edit message "([^"]*)" in room "([^"]+)" with new content "([^"]+)"$`, requestsToEditMessage)

	// Step 50 (Part-2): edit message with empty content.
	ctx.Step(`^"([^"]+)" requests to edit message "([^"]+)" in room "([^"]+)" with empty content$`, requestsToEditMessageEmptyContent)

	// Step 51 (Part-1): delete message request/reply.
	ctx.Step(`^"([^"]+)" requests to delete message "([^"]+)" in room "([^"]+)"$`, requestsToDeleteMessage)

	// Step 52 (Part-2): channel room with more than N members (MongoDB seeding).
	ctx.Step(`^a channel room "([^"]+)" exists with more than ([0-9]+) members$`, channelRoomExistsWithMoreThanNMembers)

	// Step 53 (Part-2): regular member fixture (MongoDB seeding).
	ctx.Step(`^"([^"]+)" is a regular member of room "([^"]+)"$`, isRegularMemberOfRoom)

	// Step 54 (Part-2): assert accepted reply on decoupled response subject.
	ctx.Step(`^the response is an accepted reply$`, responseIsAnAcceptedReply)

	// Step 55 (Part-2): assert no reply on decoupled response subject.
	ctx.Step(`^no reply is received on the response subject$`, noReplyReceivedOnResponseSubject)
}

// submitsMessageWith is intentionally non-executing — see original comment.
func submitsMessageWith(_ context.Context, actor, condition string) error {
	if creds := suiteWorld.Credentials(actor); creds == nil {
		return fmt.Errorf("messaging: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}
	return fmt.Errorf(
		"messaging: cannot submit %q — message-gatekeeper is reached via a fire-and-forget "+
			"JetStream publish with a decoupled reply; Part-1 has no JetStream/peek primitive "+
			"(see blindspots.md::message-submit-needs-jetstream-primitive)", condition)
}

// ---------------------------------------------------------------------------
// Part-2 stubs — JetStream publish, Cassandra seeding, decoupled reply.
// ---------------------------------------------------------------------------

func submitsValidMessageToRoom(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsOversizedMessageToRoom(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsThreadReplyNoCreatedAt(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsThreadReplyMalformedParentID(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsMessageQuotingDifferentThread(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsMainRoomMessageQuotingThreadReply(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsMessageQuotingNonExistentID(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsMessageAsNonMember(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func submitsTopLevelMessageToRoom(_ context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func publishesMessageOnWrongSiteSubject(_ context.Context, actor, site, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func requestsToEditMessageEmptyContent(_ context.Context, actor, messageID, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func channelRoomExistsWithMoreThanNMembers(_ context.Context, roomName string, n int) error {
	return fmt.Errorf("part-2 primitive missing: mongo-seeding")
}

func isRegularMemberOfRoom(_ context.Context, member, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-seeding")
}

func responseIsAnAcceptedReply(_ context.Context) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func noReplyReceivedOnResponseSubject(_ context.Context) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

// ---------------------------------------------------------------------------
// Step 49 (Part-1): edit message — NATS request/reply.
// ---------------------------------------------------------------------------

func requestsToEditMessage(ctx context.Context, actor, messageID, roomName, newContent string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("messaging: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(editMessageRequest{
		MessageID: messageID,
		NewMsg:    newContent,
	})
	if err != nil {
		return fmt.Errorf("messaging: marshal edit-message request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.edit", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}

// ---------------------------------------------------------------------------
// Step 51 (Part-1): delete message — NATS request/reply.
// ---------------------------------------------------------------------------

func requestsToDeleteMessage(ctx context.Context, actor, messageID, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("messaging: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(deleteMessageRequest{
		MessageID: messageID,
	})
	if err != nil {
		return fmt.Errorf("messaging: marshal delete-message request: %w", err)
	}

	subj := fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.delete", creds.Account, prefixedRoom, site)
	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site, subj, body)
}
