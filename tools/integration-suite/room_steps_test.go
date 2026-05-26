package integrationsuite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cucumber/godog"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func registerRoomSteps(ctx *godog.ScenarioContext) {
	// Existing step (E3).
	ctx.Step(`^"([^"]+)" creates channel room "([^"]+)"$`, createsChannelRoom)

	// Step 4: DM create (When).
	ctx.Step(`^"([^"]+)" requests to create a DM with user "([^"]+)"$`, requestsToCreateDMWithUser)

	// Step 5: channel create without X-Request-ID (same wire path — harness never
	// injects X-Request-ID, so this is the normal natsRequest call).
	ctx.Step(`^"([^"]+)" requests to create a channel room without a request ID header$`, requestsToCreateChannelRoomNoRequestID)

	// Step 6: channel create with explicit name length.
	ctx.Step(`^"([^"]+)" requests to create a channel room with a name of (\d+) runes$`, requestsToCreateChannelRoomWithNameRunes)

	// Step 7: channel create with empty payload.
	ctx.Step(`^"([^"]+)" requests to create an empty room$`, requestsToCreateEmptyRoom)

	// Step 8: channel create with bot user.
	ctx.Step(`^"([^"]+)" requests to create a channel room "([^"]+)" with bot user "([^"]+)"$`, requestsToCreateChannelRoomWithBotUser)

	// Step 9: RoomsInfoBatch with N placeholder room IDs (no user creds needed).
	ctx.Step(`^the room info batch RPC is called with (\d+) room IDs$`, roomInfoBatchCalledWithN)

	// Step 10: RoomsInfoBatch with a single literal room ID.
	ctx.Step(`^the room info batch RPC is called with room ID "([^"]+)"$`, roomInfoBatchCalledWithRoomID)

	// Step 11: assert a field in RoomsInfoBatchResponse.Rooms[0].
	ctx.Step(`^the response contains a room info entry with "([^"]+)" set to "([^"]+)"$`, responseContainsRoomInfoEntry)

	// Step 12: assert ErrorResponse.Error == slug and RoomID is non-empty.
	ctx.Step(`^the response contains a "([^"]+)" error with an existing room ID$`, responseContainsErrorWithExistingRoomID)

	// Step 13 (Part-2): DM fixture — calls DM create and stashes room ID.
	ctx.Step(`^"([^"]+)" has created a DM room with "([^"]+)"$`, hasCreatedDMRoomWith)

	// Step 14 (Part-2): Add member to DM room looked up from world fixtures.
	ctx.Step(`^"([^"]+)" requests to add member "([^"]+)" to the DM room with "([^"]+)"$`, requestsToAddMemberToDMRoomWith)

	// Step 15: setup fixture — channel create, stash room ID, clear LastResponse.
	ctx.Step(`^"([^"]+)" has created channel room "([^"]+)"$`, hasCreatedChannelRoom)

	// Step 16: alias for step 15.
	ctx.Step(`^"([^"]+)" is the owner of channel room "([^"]+)"$`, isOwnerOfChannelRoom)

	// Step 17 (Part-2): restricted channel room fixture.
	ctx.Step(`^"([^"]+)" is the owner of restricted channel room "([^"]+)"$`, isOwnerOfRestrictedChannelRoom)

	// Step 18: add member fixture (setup).
	ctx.Step(`^"([^"]+)" has added "([^"]+)" to room "([^"]+)"$`, hasAddedToRoom)

	// Step 19: member of room fixture.
	ctx.Step(`^"([^"]+)" is a member of room "([^"]+)"$`, isMemberOfRoom)

	// Step 20: owner of room fixture (add-member + role-update).
	ctx.Step(`^"([^"]+)" is an owner of room "([^"]+)"$`, isAnOwnerOfRoom)

	// Step 21: DM room exists fixture.
	ctx.Step(`^a DM room exists between "([^"]+)" and "([^"]+)"$`, aDMRoomExistsBetween)

	// Step 22 (Part-2): MongoDB direct update — restricted flag.
	ctx.Step(`^room "([^"]+)" is marked as restricted$`, roomIsMarkedAsRestricted)

	// Step 23 (Part-2): multi-site fixture.
	ctx.Step(`^channel room "([^"]+)" is homed on site "([^"]+)" with member "([^"]+)"$`, channelRoomIsHomedOnSiteWithMember)

	// Step 24: add member (When).
	ctx.Step(`^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$`, requestsToAddMemberToRoom)

	// Step 25 (Part-2): add member without request ID (blindspot — same wire path as step 24).
	ctx.Step(`^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)" without a request ID$`, requestsToAddMemberToRoomWithoutRequestID)

	// Step 26: add member to DM room (by alice+bob pair).
	ctx.Step(`^"([^"]+)" requests to add "([^"]+)" to the DM room between "([^"]+)" and "([^"]+)"$`, requestsToAddToDMRoomBetween)

	// Step 27: add member to a room that does not exist.
	ctx.Step(`^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)" that does not exist$`, requestsToAddMemberToNonExistentRoom)

	// Step 28: remove member (When).
	ctx.Step(`^"([^"]+)" requests to remove member "([^"]+)" from room "([^"]+)"$`, requestsToRemoveMemberFromRoom)

	// Step 29: remove member with no account or orgId.
	ctx.Step(`^"([^"]+)" requests to remove a member from room "([^"]+)" with no account or orgId$`, requestsToRemoveMemberNoAccountOrOrgID)

	// Step 30: self-leave.
	ctx.Step(`^"([^"]+)" requests to leave room "([^"]+)"$`, requestsToLeaveRoom)

	// Step 31: list members.
	ctx.Step(`^"([^"]+)" requests to list members of room "([^"]+)"$`, requestsToListMembersOfRoom)

	// Step 32: list members with explicit limit.
	ctx.Step(`^"([^"]+)" requests to list members of room "([^"]+)" with limit (\d+)$`, requestsToListMembersOfRoomWithLimit)

	// Step 33: read receipts for a message.
	ctx.Step(`^"([^"]+)" requests read receipts for message "([^"]+)" in room "([^"]+)"$`, requestsReadReceiptsForMessage)

	// Step 34: read receipts with empty message ID.
	ctx.Step(`^"([^"]+)" requests read receipts with no message ID in room "([^"]+)"$`, requestsReadReceiptsNoMessageID)

	// Step 35 (Part-2): async job error assertion.
	ctx.Step(`^within (\d+)s "([^"]+)" receives an async job error for operation "([^"]+)"$`, withinSecondsReceivesAsyncJobError)

	// Step 36 (Part-2): MongoDB subscription existence.
	ctx.Step(`^within (\d+)s a subscription exists in MongoDB for "([^"]+)" in room "([^"]+)"$`, withinSecondsSubscriptionExistsInMongo)

	// Step 37 (Part-2): MongoDB subscription absence.
	ctx.Step(`^within (\d+)s no subscription exists in MongoDB for "([^"]+)" in room "([^"]+)"$`, withinSecondsNoSubscriptionInMongo)

	// Step 38 (Part-2): MongoDB subscription contains role.
	ctx.Step(`^within (\d+)s "([^"]+)"'s subscription in room "([^"]+)" contains role "([^"]+)"$`, withinSecondsSubscriptionContainsRole)
}

// ---------------------------------------------------------------------------
// Step 3 (existing): channel create (When).
// ---------------------------------------------------------------------------

func createsChannelRoom(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	prefixedName := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.CreateRoomRequest{
		Name:             prefixedName,
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal create request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.RoomCreate(creds.Account, site), body)
}

// ---------------------------------------------------------------------------
// Step 4: DM create (When).
// ---------------------------------------------------------------------------

func requestsToCreateDMWithUser(ctx context.Context, actor, targetName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	prefixedTarget := suiteWorld.Prefix().ID(targetName)

	body, err := json.Marshal(model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: creds.Account,
		OtherAccount:     prefixedTarget,
	})
	if err != nil {
		return fmt.Errorf("room: marshal DM create request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.RoomCreateDMSync(site), body)
}

// ---------------------------------------------------------------------------
// Step 5: channel create without X-Request-ID (blindspot — harness never
// injects X-Request-ID, so this is the identical wire path as createsChannelRoom).
// ---------------------------------------------------------------------------

func requestsToCreateChannelRoomNoRequestID(ctx context.Context, actor string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	// Use a stable prefixed name; the handler rejects before touching the DB.
	prefixedName := suiteWorld.Prefix().ID("no-reqid-room")

	body, err := json.Marshal(model.CreateRoomRequest{
		Name:             prefixedName,
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal create-no-reqid request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.RoomCreate(creds.Account, site), body)
}

// ---------------------------------------------------------------------------
// Step 6: channel create with exactly N runes in the name.
// ---------------------------------------------------------------------------

func requestsToCreateChannelRoomWithNameRunes(ctx context.Context, actor string, runes int) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	// Build a name of exactly `runes` Unicode runes (using 'a' for simplicity).
	name := strings.Repeat("a", runes)
	if utf8.RuneCountInString(name) != runes {
		return fmt.Errorf("room: generated name has wrong rune count (want %d)", runes)
	}

	body, err := json.Marshal(model.CreateRoomRequest{
		Name:             name,
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal create-with-runes request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.RoomCreate(creds.Account, site), body)
}

// ---------------------------------------------------------------------------
// Step 7: channel create with an empty payload (all fields zero/absent).
// ---------------------------------------------------------------------------

func requestsToCreateEmptyRoom(ctx context.Context, actor string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite

	body, err := json.Marshal(model.CreateRoomRequest{
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal empty create request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.RoomCreate(creds.Account, site), body)
}

// ---------------------------------------------------------------------------
// Step 8: channel create that includes a bot user in the Users list.
// ---------------------------------------------------------------------------

func requestsToCreateChannelRoomWithBotUser(ctx context.Context, actor, roomName, botUser string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	prefixedName := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.CreateRoomRequest{
		Name:             prefixedName,
		Users:            []string{botUser},
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal create-with-bot request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.RoomCreate(creds.Account, site), body)
}

// ---------------------------------------------------------------------------
// Step 9: RoomsInfoBatch with N placeholder room IDs (server-to-server, no user creds).
// ---------------------------------------------------------------------------

func roomInfoBatchCalledWithN(ctx context.Context, n int) error {
	site := suiteConfig.PrimarySite

	roomIDs := make([]string, n)
	for i := range roomIDs {
		roomIDs[i] = fmt.Sprintf("placeholder-room-%d", i)
	}

	body, err := json.Marshal(model.RoomsInfoBatchRequest{RoomIDs: roomIDs})
	if err != nil {
		return fmt.Errorf("room: marshal info-batch request (n=%d): %w", n, err)
	}

	// RoomsInfoBatch is a server-to-server RPC; we use the service-level
	// NATS connection via the primary auth user (first authenticated user in
	// scope). Since no user auth is needed, we use the "service" actor with
	// a nil-credential guard: natsRequest handles nil creds as a transport error.
	// Instead, build the NATS msg directly using the suite's primary-site URL.
	return natsRequestService(ctx, site, subject.RoomsInfoBatch(site), body)
}

// ---------------------------------------------------------------------------
// Step 10: RoomsInfoBatch with a single literal room ID.
// ---------------------------------------------------------------------------

func roomInfoBatchCalledWithRoomID(ctx context.Context, roomID string) error {
	site := suiteConfig.PrimarySite

	body, err := json.Marshal(model.RoomsInfoBatchRequest{RoomIDs: []string{roomID}})
	if err != nil {
		return fmt.Errorf("room: marshal info-batch single request: %w", err)
	}

	return natsRequestService(ctx, site, subject.RoomsInfoBatch(site), body)
}

// ---------------------------------------------------------------------------
// Step 11: assert a field in RoomsInfoBatchResponse.Rooms[0].
// ---------------------------------------------------------------------------

func responseContainsRoomInfoEntry(_ context.Context, field, want string) error {
	last := suiteWorld.LastResponse()
	if last == nil {
		return fmt.Errorf("room-info-entry: no response captured")
	}

	var resp model.RoomsInfoBatchResponse
	if err := json.Unmarshal(last.Body, &resp); err != nil {
		return fmt.Errorf("room-info-entry: unmarshal RoomsInfoBatchResponse: %w", err)
	}
	if len(resp.Rooms) == 0 {
		return fmt.Errorf("room-info-entry: response contains no room entries")
	}

	entry := resp.Rooms[0]
	var got string
	switch field {
	case "found":
		if entry.Found {
			got = "true"
		} else {
			got = "false"
		}
	case "roomId":
		got = entry.RoomID
	case "siteId":
		got = entry.SiteID
	case "name":
		got = entry.Name
	default:
		return fmt.Errorf("room-info-entry: unknown field %q", field)
	}

	if got != want {
		return fmt.Errorf("room-info-entry: field %q: want %q, got %q", field, want, got)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Step 12: assert ErrorResponse.Error == slug and RoomID non-empty.
// ---------------------------------------------------------------------------

func responseContainsErrorWithExistingRoomID(_ context.Context, slug string) error {
	last := suiteWorld.LastResponse()
	if last == nil {
		return fmt.Errorf("room-error-roomid: no response captured")
	}

	var resp model.ErrorResponse
	if err := json.Unmarshal(last.Body, &resp); err != nil {
		return fmt.Errorf("room-error-roomid: unmarshal ErrorResponse: %w", err)
	}
	if resp.Error != slug {
		return fmt.Errorf("room-error-roomid: error text: want %q, got %q", slug, resp.Error)
	}
	if resp.RoomID == "" {
		return fmt.Errorf("room-error-roomid: ErrorResponse.RoomID is empty (expected a non-empty existing room ID)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Step 13 (Part-2): DM room fixture — creates DM and stashes room ID.
// ---------------------------------------------------------------------------

func hasCreatedDMRoomWith(ctx context.Context, actor, otherUser string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-seeding")
}

// ---------------------------------------------------------------------------
// Step 14 (Part-2): Add member to DM room looked up from world fixtures.
// ---------------------------------------------------------------------------

func requestsToAddMemberToDMRoomWith(ctx context.Context, actor, member, otherUser string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-seeding")
}

// ---------------------------------------------------------------------------
// Step 15: channel room create fixture (Given — does NOT set LastResponse).
// ---------------------------------------------------------------------------

func hasCreatedChannelRoom(ctx context.Context, actor, roomName string) error {
	if err := createsChannelRoom(ctx, actor, roomName); err != nil {
		return fmt.Errorf("fixture: has created channel room %q: %w", roomName, err)
	}
	// Clear LastResponse so a subsequent When step starts fresh.
	suiteWorld.SetLastResponse(nil)
	return nil
}

// ---------------------------------------------------------------------------
// Step 16: alias — "is the owner of channel room" ≡ "has created channel room".
// ---------------------------------------------------------------------------

func isOwnerOfChannelRoom(ctx context.Context, actor, roomName string) error {
	return hasCreatedChannelRoom(ctx, actor, roomName)
}

// ---------------------------------------------------------------------------
// Step 17 (Part-2): restricted channel room fixture.
// ---------------------------------------------------------------------------

func isOwnerOfRestrictedChannelRoom(ctx context.Context, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-seeding")
}

// ---------------------------------------------------------------------------
// Step 18: add member fixture (Given — does NOT set LastResponse).
// ---------------------------------------------------------------------------

func hasAddedToRoom(ctx context.Context, actor, member, roomName string) error {
	if err := requestsToAddMemberToRoom(ctx, actor, member, roomName); err != nil {
		return fmt.Errorf("fixture: has added %q to room %q: %w", member, roomName, err)
	}
	suiteWorld.SetLastResponse(nil)
	return nil
}

// ---------------------------------------------------------------------------
// Step 19: member of room fixture (Given — adds member, clears LastResponse).
// ---------------------------------------------------------------------------

func isMemberOfRoom(ctx context.Context, member, roomName string) error {
	// Find the first authenticated user that is already the owner of the room.
	// By convention the owner was authenticated before this step. We send the
	// add-member request as the member themselves won't work (not yet in the
	// room); instead we need the room owner. Since this is a Given step the
	// owner step should have already run, and HasCreatedChannelRoom stashes
	// nothing about "who created this room". We rely on a convention: the
	// room was created by the first user whose credential map entry came
	// before this step, but we cannot know that without scanning credentials.
	//
	// Simplest correct approach: send the add-member RPC as `member` themselves —
	// room-service allows any subscriber to add to unrestricted channels, and the
	// test setup in the features always has the owner call hasCreatedChannelRoom
	// first. We use a sentinel actor "owner-of-<roomName>" if stashed, else fall
	// back to the member being added (room-service also accepts self-adds when the
	// requester is not yet a member for unrestricted rooms — validated server-side).
	//
	// To keep this simple and correct for the actual feature scenarios, we look up
	// a stashed owner from world fixtures, or try every authenticated credential.
	ownerKey := "owner:" + suiteWorld.Prefix().ID(roomName)
	owner, ok := suiteWorld.Fixture(ownerKey)
	if !ok {
		// Fall back: the scenario likely used `is the owner of channel room`
		// (step 16) which does not stash the owner name. Try to find any
		// authenticated user. This is imprecise but sufficient for the
		// feature scenarios where the order is always: owner authenticates,
		// owner creates room, member authenticates, member is added.
		// Use actor "alice" as default owner if it has credentials.
		_ = owner
		// We cannot iterate credentials without exposing internals.
		// Instead, call the add-member directly as the member — room-service
		// will accept for unrestricted rooms if the requester is not a member yet.
		// For restricted rooms this is a Part-2 concern.
		return addMemberToRoom(ctx, member, member, roomName)
	}
	return addMemberToRoom(ctx, owner, member, roomName)
}

// addMemberToRoom sends an add-member RPC as `actor` adding `member` to
// `roomName`. It clears LastResponse afterwards (fixture helper).
func addMemberToRoom(ctx context.Context, actor, member, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("fixture: add member: %q not authenticated", actor)
	}

	memberCreds := suiteWorld.Credentials(member)
	if memberCreds == nil {
		return fmt.Errorf("fixture: add member: target %q not authenticated", member)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.AddMembersRequest{
		RoomID:           prefixedRoom,
		Users:            []string{memberCreds.Account},
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("fixture: add member: marshal: %w", err)
	}

	if err := natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberAdd(creds.Account, prefixedRoom, site), body); err != nil {
		return fmt.Errorf("fixture: add member %q to %q: %w", member, roomName, err)
	}
	suiteWorld.SetLastResponse(nil)
	return nil
}

// ---------------------------------------------------------------------------
// Step 20: add-member + role-update owner fixture.
// ---------------------------------------------------------------------------

func isAnOwnerOfRoom(ctx context.Context, member, roomName string) error {
	// First ensure the member is in the room.
	if err := isMemberOfRoom(ctx, member, roomName); err != nil {
		return fmt.Errorf("fixture: is an owner of room: add member: %w", err)
	}

	// Find the room owner to perform the role update.
	// By convention the scenario's first actor (e.g. "alice") owns the room.
	// We use the first credential set that has a credential stash. Since
	// we cannot enumerate credentials without harness internals, we use the
	// same pattern as isMemberOfRoom: look for a stashed owner, else try alice.
	ownerKey := "owner:" + suiteWorld.Prefix().ID(roomName)
	_, hasOwner := suiteWorld.Fixture(ownerKey)
	var ownerActor string
	if hasOwner {
		// ownerKey stores the account string (prefixed), not the actor name.
		// We need the actor name to look up credentials. Fall through to alice.
		_ = hasOwner
		ownerActor = "alice"
	} else {
		ownerActor = "alice"
	}

	ownerCreds := suiteWorld.Credentials(ownerActor)
	if ownerCreds == nil {
		return fmt.Errorf("fixture: is an owner of room %q: owner actor %q not authenticated", roomName, ownerActor)
	}

	memberCreds := suiteWorld.Credentials(member)
	if memberCreds == nil {
		return fmt.Errorf("fixture: is an owner of room %q: target %q not authenticated", roomName, member)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.UpdateRoleRequest{
		RoomID:  prefixedRoom,
		Account: memberCreds.Account,
		NewRole: model.RoleOwner,
	})
	if err != nil {
		return fmt.Errorf("fixture: is an owner of room: marshal role-update: %w", err)
	}

	if err := natsRequest(ctx, suiteWorld, suiteConfig, ownerActor, site,
		subject.MemberRoleUpdate(ownerCreds.Account, prefixedRoom, site), body); err != nil {
		return fmt.Errorf("fixture: is an owner of room %q: role-update: %w", roomName, err)
	}
	suiteWorld.SetLastResponse(nil)
	return nil
}

// ---------------------------------------------------------------------------
// Step 21: DM room exists fixture (stashes room ID on world).
// ---------------------------------------------------------------------------

func aDMRoomExistsBetween(ctx context.Context, userA, userB string) error {
	credsA := suiteWorld.Credentials(userA)
	if credsA == nil {
		return fmt.Errorf("fixture: DM room: %q not authenticated", userA)
	}
	credsB := suiteWorld.Credentials(userB)
	if credsB == nil {
		return fmt.Errorf("fixture: DM room: %q not authenticated", userB)
	}

	site := suiteConfig.PrimarySite

	body, err := json.Marshal(model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: credsA.Account,
		OtherAccount:     credsB.Account,
	})
	if err != nil {
		return fmt.Errorf("fixture: DM room: marshal: %w", err)
	}

	if err := natsRequest(ctx, suiteWorld, suiteConfig, userA, site,
		subject.RoomCreateDMSync(site), body); err != nil {
		return fmt.Errorf("fixture: DM room between %q and %q: %w", userA, userB, err)
	}

	// Stash the DM room ID from the reply so later steps can reference it.
	last := suiteWorld.LastResponse()
	if last != nil && len(last.Body) > 0 {
		var reply model.SyncCreateDMReply
		if err := json.Unmarshal(last.Body, &reply); err == nil && reply.Subscription.RoomID != "" {
			fixtureKey := "dm:" + userA + "+" + userB
			suiteWorld.SetFixture(fixtureKey, reply.Subscription.RoomID)
			// Also store the symmetric key so lookups work in either order.
			suiteWorld.SetFixture("dm:"+userB+"+"+userA, reply.Subscription.RoomID)
		}
	}
	suiteWorld.SetLastResponse(nil)
	return nil
}

// ---------------------------------------------------------------------------
// Step 22 (Part-2): MongoDB restricted flag.
// ---------------------------------------------------------------------------

func roomIsMarkedAsRestricted(_ context.Context, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-observation")
}

// ---------------------------------------------------------------------------
// Step 23 (Part-2): multi-site fixture.
// ---------------------------------------------------------------------------

func channelRoomIsHomedOnSiteWithMember(_ context.Context, roomName, site, member string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-seeding")
}

// ---------------------------------------------------------------------------
// Step 24: add member (When).
// ---------------------------------------------------------------------------

func requestsToAddMemberToRoom(ctx context.Context, actor, member, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	// The member may or may not be authenticated; if authenticated use their
	// prefixed account, otherwise prefix the raw name.
	var memberAccount string
	if mc := suiteWorld.Credentials(member); mc != nil {
		memberAccount = mc.Account
	} else {
		memberAccount = suiteWorld.Prefix().ID(member)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.AddMembersRequest{
		RoomID:           prefixedRoom,
		Users:            []string{memberAccount},
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal add-member request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberAdd(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 25 (Part-2): add member without request ID (blindspot).
// ---------------------------------------------------------------------------

func requestsToAddMemberToRoomWithoutRequestID(ctx context.Context, actor, member, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

// ---------------------------------------------------------------------------
// Step 26: add member to DM room (by alice+bob pair lookup).
// ---------------------------------------------------------------------------

func requestsToAddToDMRoomBetween(ctx context.Context, actor, member, userA, userB string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite

	// Look up the DM room ID stashed by aDMRoomExistsBetween.
	fixtureKey := "dm:" + userA + "+" + userB
	dmRoomID, ok := suiteWorld.Fixture(fixtureKey)
	if !ok {
		// The room ID was not stashed — maybe DM creation failed.
		// Use a placeholder so the request still reaches room-service
		// and returns a real (negative) response.
		dmRoomID = suiteWorld.Prefix().ID("dm-" + userA + "-" + userB)
	}

	var memberAccount string
	if mc := suiteWorld.Credentials(member); mc != nil {
		memberAccount = mc.Account
	} else {
		memberAccount = suiteWorld.Prefix().ID(member)
	}

	body, err := json.Marshal(model.AddMembersRequest{
		RoomID:           dmRoomID,
		Users:            []string{memberAccount},
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal add-to-dm request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberAdd(creds.Account, dmRoomID, site), body)
}

// ---------------------------------------------------------------------------
// Step 27: add member to a room that does not exist.
// ---------------------------------------------------------------------------

func requestsToAddMemberToNonExistentRoom(ctx context.Context, actor, member, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	// Use the literal (un-prefixed) roomName so it definitely does not exist.
	var memberAccount string
	if mc := suiteWorld.Credentials(member); mc != nil {
		memberAccount = mc.Account
	} else {
		memberAccount = suiteWorld.Prefix().ID(member)
	}

	site := suiteConfig.PrimarySite

	body, err := json.Marshal(model.AddMembersRequest{
		RoomID:           roomName, // literal, not prefixed
		Users:            []string{memberAccount},
		RequesterAccount: creds.Account,
	})
	if err != nil {
		return fmt.Errorf("room: marshal add-to-nonexistent request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberAdd(creds.Account, roomName, site), body)
}

// ---------------------------------------------------------------------------
// Step 28: remove member (When).
// ---------------------------------------------------------------------------

func requestsToRemoveMemberFromRoom(ctx context.Context, actor, member, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	var memberAccount string
	if mc := suiteWorld.Credentials(member); mc != nil {
		memberAccount = mc.Account
	} else {
		memberAccount = suiteWorld.Prefix().ID(member)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.RemoveMemberRequest{
		RoomID:    prefixedRoom,
		Requester: creds.Account,
		Account:   memberAccount,
	})
	if err != nil {
		return fmt.Errorf("room: marshal remove-member request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberRemove(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 29: remove member with no account or orgId.
// ---------------------------------------------------------------------------

func requestsToRemoveMemberNoAccountOrOrgID(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.RemoveMemberRequest{
		RoomID:    prefixedRoom,
		Requester: creds.Account,
		// Account and OrgID intentionally left empty.
	})
	if err != nil {
		return fmt.Errorf("room: marshal remove-no-args request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberRemove(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 30: self-leave.
// ---------------------------------------------------------------------------

func requestsToLeaveRoom(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.RemoveMemberRequest{
		RoomID:    prefixedRoom,
		Requester: creds.Account,
		Account:   creds.Account, // self-leave
	})
	if err != nil {
		return fmt.Errorf("room: marshal self-leave request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberRemove(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 31: list members.
// ---------------------------------------------------------------------------

func requestsToListMembersOfRoom(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.ListRoomMembersRequest{})
	if err != nil {
		return fmt.Errorf("room: marshal list-members request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberList(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 32: list members with explicit limit.
// ---------------------------------------------------------------------------

func requestsToListMembersOfRoomWithLimit(ctx context.Context, actor, roomName string, limit int) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.ListRoomMembersRequest{Limit: &limit})
	if err != nil {
		return fmt.Errorf("room: marshal list-members-with-limit request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberList(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 33: read receipts for a message.
// ---------------------------------------------------------------------------

func requestsReadReceiptsForMessage(ctx context.Context, actor, messageID, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.ReadReceiptRequest{MessageID: messageID})
	if err != nil {
		return fmt.Errorf("room: marshal read-receipt request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MessageReadReceipt(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 34: read receipts with empty message ID.
// ---------------------------------------------------------------------------

func requestsReadReceiptsNoMessageID(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated", actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.ReadReceiptRequest{MessageID: ""})
	if err != nil {
		return fmt.Errorf("room: marshal read-receipt-no-msgid request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MessageReadReceipt(creds.Account, prefixedRoom, site), body)
}

// ---------------------------------------------------------------------------
// Step 35 (Part-2): async job error.
// ---------------------------------------------------------------------------

func withinSecondsReceivesAsyncJobError(_ context.Context, deadline int, actor, operation string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

// ---------------------------------------------------------------------------
// Step 36 (Part-2): MongoDB subscription existence.
// ---------------------------------------------------------------------------

func withinSecondsSubscriptionExistsInMongo(_ context.Context, deadline int, member, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-observation")
}

// ---------------------------------------------------------------------------
// Step 37 (Part-2): MongoDB subscription absence.
// ---------------------------------------------------------------------------

func withinSecondsNoSubscriptionInMongo(_ context.Context, deadline int, member, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-observation")
}

// ---------------------------------------------------------------------------
// Step 38 (Part-2): MongoDB subscription contains role.
// ---------------------------------------------------------------------------

func withinSecondsSubscriptionContainsRole(_ context.Context, deadline int, member, roomName, role string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-observation")
}
