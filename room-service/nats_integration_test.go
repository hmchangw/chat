//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

const testSiteID = "site-test"

// publishLog records a publish event for test inspection.
type publishLog struct {
	Stream  string
	Subject string
	Data    string
}

// publishState tracks publish logs for test verification.
type publishState struct {
	logs *[]publishLog
	mu   *sync.Mutex
}

// natsRequest sends a NATS request and logs the subject, request, reply, and any publishes.
func natsRequest(t *testing.T, nc *otelnats.Conn, subj string, payload any, ps publishState) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	ps.mu.Lock()
	before := len(*ps.logs)
	ps.mu.Unlock()

	t.Logf("──── NATS Request/Reply ────")
	t.Logf("  Subject: %s", subj)
	t.Logf("  Request: %s", string(data))

	msg, err := nc.Request(context.Background(), subj, data, 5*time.Second)
	require.NoError(t, err, "NATS request failed on subject: %s", subj)

	t.Logf("  Reply:   %s", string(msg.Data))

	// Allow async publishes to settle.
	time.Sleep(50 * time.Millisecond)

	// Log any publishes that occurred during this request
	ps.mu.Lock()
	for _, p := range (*ps.logs)[before:] {
		t.Logf("  Publish → Stream: %s | Subject: %s | Data: %s", p.Stream, p.Subject, p.Data)
	}
	ps.mu.Unlock()
	t.Logf("────────────────────────────")

	return msg
}

// TestNATS_RoomMember runs all NATS integration tests with a single handler registration
// to avoid competing queue group subscribers.
func TestNATS_RoomMember(t *testing.T) {
	nc := sharedNATSConn
	db := freshDB(t)
	store := NewMongoStore(db)

	var (
		publishes []publishLog
		pubMu     sync.Mutex
	)

	streamName := "ROOMS_" + testSiteID

	logPublish := func(stream string) func(context.Context, string, []byte) error {
		return func(_ context.Context, subj string, data []byte) error {
			pubMu.Lock()
			publishes = append(publishes, publishLog{Stream: stream, Subject: subj, Data: string(data)})
			pubMu.Unlock()
			return nil
		}
	}

	handler := NewHandler(store, testSiteID, 1000,
		logPublish(streamName),
	)

	require.NoError(t, handler.RegisterCRUD(nc))
	require.NoError(t, nc.NatsConn().Flush())

	ps := publishState{logs: &publishes, mu: &pubMu}

	// Helper: count publishes to the ROOMS stream since a given offset
	publishesSince := func(offset int) []publishLog {
		pubMu.Lock()
		defer pubMu.Unlock()
		var result []publishLog
		for _, p := range publishes[offset:] {
			if p.Stream == streamName {
				result = append(result, p)
			}
		}
		return result
	}

	t.Run("RemoveMember_SelfLeave", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "rm-sl-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-sl-s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "rm-sl-r1", SiteID: testSiteID, Role: model.RoleMember,
		}))

		pubMu.Lock()
		before := len(publishes)
		pubMu.Unlock()

		subj := subject.MemberRemove("alice", "rm-sl-r1", testSiteID)
		req := model.RemoveMemberRequest{RoomID: "rm-sl-r1"}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "accepted", resp["status"])

		// Verify request was published to ROOMS stream
		streamPublishes := publishesSince(before)
		require.Len(t, streamPublishes, 1, "expected one publish to ROOMS stream")
		assert.Equal(t, subj, streamPublishes[0].Subject)
	})

	t.Run("RemoveMember_OwnerRemovesOther", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "rm-or-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-or-s1", User: model.SubscriptionUser{ID: "u1", Account: "owner1"}, RoomID: "rm-or-r1", SiteID: testSiteID, Role: model.RoleOwner,
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-or-s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "rm-or-r1", SiteID: testSiteID, Role: model.RoleMember,
		}))

		pubMu.Lock()
		before := len(publishes)
		pubMu.Unlock()

		subj := subject.MemberRemove("owner1", "rm-or-r1", testSiteID)
		req := model.RemoveMemberRequest{RoomID: "rm-or-r1"}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "accepted", resp["status"])

		// Verify request was published to ROOMS stream
		streamPublishes := publishesSince(before)
		require.Len(t, streamPublishes, 1)
	})

	t.Run("RemoveMember_NonOwnerRejected", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "rm-no-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-no-s1", User: model.SubscriptionUser{ID: "u1", Account: "bob"}, RoomID: "rm-no-r1", SiteID: testSiteID, Role: model.RoleMember,
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-no-s2", User: model.SubscriptionUser{ID: "u2", Account: "carol"}, RoomID: "rm-no-r1", SiteID: testSiteID, Role: model.RoleMember,
		}))

		subj := subject.MemberRemove("bob", "rm-no-r1", testSiteID)
		req := model.RemoveMemberRequest{RoomID: "rm-no-r1"}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "only owners")
		t.Logf("  Expected error: %s", errResp.Error)
	})

	t.Run("RemoveMember_LastOwnerCannotLeave", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "rm-lo-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-lo-s1", User: model.SubscriptionUser{ID: "u1", Account: "onlyowner"}, RoomID: "rm-lo-r1", SiteID: testSiteID, Role: model.RoleOwner,
		}))

		subj := subject.MemberRemove("onlyowner", "rm-lo-r1", testSiteID)
		req := model.RemoveMemberRequest{RoomID: "rm-lo-r1"}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "last owner")
		t.Logf("  Expected error: %s", errResp.Error)
	})

	t.Run("UpdateRole_PromoteMember", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "ur-pm-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-pm-s1", User: model.SubscriptionUser{ID: "u1", Account: "owner1"}, RoomID: "ur-pm-r1", SiteID: testSiteID, Role: model.RoleOwner,
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-pm-s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "ur-pm-r1", SiteID: testSiteID, Role: model.RoleMember,
		}))

		pubMu.Lock()
		before := len(publishes)
		pubMu.Unlock()

		subj := subject.MemberRoleUpdate("owner1", "ur-pm-r1", testSiteID)
		req := model.UpdateRoleRequest{RoomID: "ur-pm-r1", NewRole: model.RoleOwner}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "accepted", resp["status"])

		// Verify request was published to ROOMS stream (no direct DB write)
		streamPublishes := publishesSince(before)
		require.Len(t, streamPublishes, 1)

		// role should NOT have changed in DB yet (room-service only validates and publishes)
		sub, err := store.GetSubscription(ctx, "bob", "ur-pm-r1")
		require.NoError(t, err)
		assert.True(t, sub.Role == model.RoleMember, "bob should still be member — room-worker has not processed yet")
	})

	t.Run("UpdateRole_LastOwnerCannotDemote", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "ur-lo-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-lo-s1", User: model.SubscriptionUser{ID: "u1", Account: "owner1"}, RoomID: "ur-lo-r1", SiteID: testSiteID, Role: model.RoleOwner,
		}))

		subj := subject.MemberRoleUpdate("owner1", "ur-lo-r1", testSiteID)
		req := model.UpdateRoleRequest{RoomID: "ur-lo-r1", NewRole: model.RoleMember}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "last owner")
		t.Logf("  Expected error: %s", errResp.Error)

		sub, err := store.GetSubscription(ctx, "owner1", "ur-lo-r1")
		require.NoError(t, err)
		assert.True(t, sub.Role == model.RoleOwner, "owner1 should still be owner")
	})

	t.Run("UpdateRole_FederationUserCannotBeOwner", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "ur-fu-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-fu-s1", User: model.SubscriptionUser{ID: "u1", Account: "owner1"}, RoomID: "ur-fu-r1", SiteID: testSiteID, Role: model.RoleOwner,
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-fu-s2", User: model.SubscriptionUser{ID: "u2", Account: "Eng@site-b.example.com"}, RoomID: "ur-fu-r1", SiteID: "site-b", Role: model.RoleMember,
		}))

		subj := subject.MemberRoleUpdate("owner1", "ur-fu-r1", testSiteID)
		req := model.UpdateRoleRequest{RoomID: "ur-fu-r1", NewRole: model.RoleOwner}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "federation")
		t.Logf("  Expected error: %s", errResp.Error)
	})

	// --- AddMembers tests ---

	// Helper: seed a user into the users collection
	seedUser := func(t *testing.T, id, username string) {
		t.Helper()
		_, err := db.Collection("users").InsertOne(context.Background(), bson.M{
			"_id": id, "username": username, "federation": bson.M{"origin": testSiteID},
		})
		require.NoError(t, err)
	}

	// Helper: seed an org account into the hr_data collection
	seedOrg := func(t *testing.T, orgID, accountName string) {
		t.Helper()
		_, err := db.Collection("hr_data").InsertOne(context.Background(), bson.M{
			"sectId": orgID, "accountName": accountName,
		})
		require.NoError(t, err, "seedOrg %s/%s", orgID, accountName)
	}

	t.Run("AddMembers_IndividualUsers", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-iu-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		seedUser(t, "u-alice", "alice")
		seedUser(t, "u-bob", "bob")

		pubMu.Lock()
		before := len(publishes)
		pubMu.Unlock()

		subj := subject.MemberAdd("requester", "am-iu-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID:  "am-iu-r1",
			Users:   []string{"alice", "bob"},
			History: model.HistoryConfig{Mode: model.HistoryModeNone},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "accepted", resp["status"])

		// Verify request was published to ROOMS stream (room-service no longer writes subscriptions)
		streamPublishes := publishesSince(before)
		require.Len(t, streamPublishes, 1, "expected one publish to ROOMS stream")

		// Verify the normalized payload contains the resolved usernames
		var published model.AddMembersRequest
		require.NoError(t, json.Unmarshal([]byte(streamPublishes[0].Data), &published))
		assert.ElementsMatch(t, []string{"alice", "bob"}, published.Users)
	})

	t.Run("AddMembers_WithOrg", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-wo-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		seedOrg(t, "org-eng", "Engineering")

		pubMu.Lock()
		before := len(publishes)
		pubMu.Unlock()

		subj := subject.MemberAdd("requester", "am-wo-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID: "am-wo-r1",
			Orgs:   []string{"org-eng"},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "accepted", resp["status"])

		// Verify request was published to ROOMS stream
		streamPublishes := publishesSince(before)
		require.Len(t, streamPublishes, 1)

		// Verify the normalized payload contains the resolved org username
		var published model.AddMembersRequest
		require.NoError(t, json.Unmarshal([]byte(streamPublishes[0].Data), &published))
		assert.Contains(t, published.Users, "Engineering", "org should resolve to username 'Engineering' (local domain)")
		assert.Contains(t, published.Orgs, "org-eng", "orgs should be preserved in the normalized request")
	})

	t.Run("AddMembers_BotsFiltered", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-bf-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))

		pubMu.Lock()
		before := len(publishes)
		pubMu.Unlock()

		subj := subject.MemberAdd("requester", "am-bf-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID: "am-bf-r1",
			Users:  []string{"notify.bot", "p_webhook", "dave"},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "accepted", resp["status"])

		// Verify the published payload only contains "dave" (bots filtered)
		streamPublishes := publishesSince(before)
		require.Len(t, streamPublishes, 1)

		var published model.AddMembersRequest
		require.NoError(t, json.Unmarshal([]byte(streamPublishes[0].Data), &published))
		assert.Equal(t, []string{"dave"}, published.Users, "bots should be filtered out")
	})

	t.Run("AddMembers_ChannelSource", func(t *testing.T) {
		ctx := context.Background()

		// Source channel with subscriptions (no room_members)
		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-cs-source", Name: "source", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "am-cs-ss1", User: model.SubscriptionUser{ID: "u-eve", Account: "eve"}, RoomID: "am-cs-source", SiteID: testSiteID, Role: model.RoleMember,
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "am-cs-ss2", User: model.SubscriptionUser{ID: "u-frank", Account: "frank"}, RoomID: "am-cs-source", SiteID: testSiteID, Role: model.RoleMember,
		}))

		// Target room
		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-cs-r1", Name: "target", Type: model.RoomTypeGroup, SiteID: testSiteID}))

		pubMu.Lock()
		before := len(publishes)
		pubMu.Unlock()

		subj := subject.MemberAdd("requester", "am-cs-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID:   "am-cs-r1",
			Channels: []string{"am-cs-source"},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "accepted", resp["status"])

		// Verify the published payload contains eve and frank
		streamPublishes := publishesSince(before)
		require.Len(t, streamPublishes, 1)

		var published model.AddMembersRequest
		require.NoError(t, json.Unmarshal([]byte(streamPublishes[0].Data), &published))
		assert.ElementsMatch(t, []string{"eve", "frank"}, published.Users)
	})

	t.Run("AddMembers_RoomAtCapacity", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-rc-r1", Name: "full", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		// Seed 1000 subscriptions to fill the room via direct MongoDB insert
		docs := make([]interface{}, 1000)
		for i := range docs {
			docs[i] = model.Subscription{
				ID: fmt.Sprintf("am-rc-s%d", i), User: model.SubscriptionUser{ID: fmt.Sprintf("u%d", i), Account: fmt.Sprintf("user%d", i)},
				RoomID: "am-rc-r1", SiteID: testSiteID, Role: model.RoleMember,
			}
		}
		_, err := db.Collection("subscriptions").InsertMany(ctx, docs)
		require.NoError(t, err)

		subj := subject.MemberAdd("requester", "am-rc-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID: "am-rc-r1",
			Users:  []string{"newuser"},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "capacity")
		t.Logf("  Expected error: %s", errResp.Error)
	})
}
