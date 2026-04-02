//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

const testSiteID = "site-test"
const testDomain = "http://site-test"

// natsRequest sends a NATS request and logs the subject, request, reply, and any publishes.
type publishState struct {
	logs *[]publishLog
	mu   *sync.Mutex
}

func natsRequest(t *testing.T, nc *nats.Conn, subj string, payload any, ps publishState) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	ps.mu.Lock()
	before := len(*ps.logs)
	ps.mu.Unlock()

	t.Logf("──── NATS Request/Reply ────")
	t.Logf("  Subject: %s", subj)
	t.Logf("  Request: %s", string(data))

	msg, err := nc.Request(subj, data, 5*time.Second)
	require.NoError(t, err, "NATS request failed on subject: %s", subj)

	t.Logf("  Reply:   %s", string(msg.Data))

	// Allow async publishes (e.g., dispatchMemberEvents) to settle
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

// publishLog records a publish event for test inspection.
type publishLog struct {
	Stream  string
	Subject string
	Data    string
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

	logPublish := func(stream string) func(string, []byte) error {
		return func(subj string, data []byte) error {
			pubMu.Lock()
			publishes = append(publishes, publishLog{Stream: stream, Subject: subj, Data: string(data)})
			pubMu.Unlock()
			return nc.Publish(subj, data)
		}
	}

	handler := NewHandler(store, testSiteID, testDomain, 1000,
		logPublish("ROOMS_"+testSiteID),
		logPublish("(local)"),
		logPublish("OUTBOX_"+testSiteID),
	)

	require.NoError(t, handler.RegisterCRUD(nc))
	_, err := nc.QueueSubscribe(subject.MemberInviteWildcard(testSiteID), "room-service", handler.NatsHandleInvite)
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	ps := publishState{logs: &publishes, mu: &pubMu}

	t.Run("RemoveMember_SelfLeave", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "rm-sl-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-sl-s1", User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "rm-sl-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
		}))

		subj := subject.MemberRemove("alice", "rm-sl-r1", testSiteID)
		req := model.RemoveMemberRequest{RoomID: "rm-sl-r1", Username: "alice"}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		_, err := store.GetSubscription(ctx, "alice", "rm-sl-r1")
		assert.Error(t, err, "subscription should be deleted after self-leave")
	})

	t.Run("RemoveMember_OwnerRemovesOther", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "rm-or-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-or-s1", User: model.SubscriptionUser{ID: "u1", Username: "owner1"}, RoomID: "rm-or-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleOwner},
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-or-s2", User: model.SubscriptionUser{ID: "u2", Username: "bob"}, RoomID: "rm-or-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
		}))

		subj := subject.MemberRemove("owner1", "rm-or-r1", testSiteID)
		req := model.RemoveMemberRequest{RoomID: "rm-or-r1", Username: "bob"}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		_, err := store.GetSubscription(ctx, "bob", "rm-or-r1")
		assert.Error(t, err, "bob's subscription should be deleted")
		_, err = store.GetSubscription(ctx, "owner1", "rm-or-r1")
		assert.NoError(t, err, "owner1's subscription should still exist")
	})

	t.Run("RemoveMember_NonOwnerRejected", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "rm-no-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-no-s1", User: model.SubscriptionUser{ID: "u1", Username: "bob"}, RoomID: "rm-no-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "rm-no-s2", User: model.SubscriptionUser{ID: "u2", Username: "carol"}, RoomID: "rm-no-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
		}))

		subj := subject.MemberRemove("bob", "rm-no-r1", testSiteID)
		req := model.RemoveMemberRequest{RoomID: "rm-no-r1", Username: "carol"}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "only owners")
		t.Logf("  Expected error: %s", errResp.Error)
	})

	t.Run("UpdateRole_PromoteMember", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "ur-pm-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-pm-s1", User: model.SubscriptionUser{ID: "u1", Username: "owner1"}, RoomID: "ur-pm-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleOwner},
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-pm-s2", User: model.SubscriptionUser{ID: "u2", Username: "bob"}, RoomID: "ur-pm-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
		}))

		subj := subject.MemberRoleUpdate("owner1", "ur-pm-r1", testSiteID)
		req := model.UpdateRoleRequest{RoomID: "ur-pm-r1", Username: "bob", NewRole: model.RoleOwner}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		sub, err := store.GetSubscription(ctx, "bob", "ur-pm-r1")
		require.NoError(t, err)
		assert.True(t, HasRole(sub.Roles, model.RoleOwner), "bob should be owner now, got roles: %v", sub.Roles)
	})

	t.Run("UpdateRole_LastOwnerCannotDemote", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "ur-lo-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-lo-s1", User: model.SubscriptionUser{ID: "u1", Username: "owner1"}, RoomID: "ur-lo-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleOwner},
		}))

		subj := subject.MemberRoleUpdate("owner1", "ur-lo-r1", testSiteID)
		req := model.UpdateRoleRequest{RoomID: "ur-lo-r1", Username: "owner1", NewRole: model.RoleMember}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "last owner")
		t.Logf("  Expected error: %s", errResp.Error)

		sub, err := store.GetSubscription(ctx, "owner1", "ur-lo-r1")
		require.NoError(t, err)
		assert.True(t, HasRole(sub.Roles, model.RoleOwner), "owner1 should still be owner")
	})

	t.Run("UpdateRole_FederationUserCannotBeOwner", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "ur-fu-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-fu-s1", User: model.SubscriptionUser{ID: "u1", Username: "owner1"}, RoomID: "ur-fu-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleOwner},
		}))
		require.NoError(t, store.CreateSubscription(ctx, &model.Subscription{
			ID: "ur-fu-s2", User: model.SubscriptionUser{ID: "u2", Username: "Eng@site-b.example.com"}, RoomID: "ur-fu-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
		}))

		subj := subject.MemberRoleUpdate("owner1", "ur-fu-r1", testSiteID)
		req := model.UpdateRoleRequest{RoomID: "ur-fu-r1", Username: "Eng@site-b.example.com", NewRole: model.RoleOwner}
		msg := natsRequest(t, nc, subj, req, ps)

		var errResp model.ErrorResponse
		require.NoError(t, json.Unmarshal(msg.Data, &errResp))
		assert.Contains(t, errResp.Error, "federation")
		t.Logf("  Expected error: %s", errResp.Error)
	})

	// --- AddMembers tests ---

	// Helper: seed a user into the users collection (needed for GetUserID in addMembers flow)
	seedUser := func(t *testing.T, id, username string) {
		t.Helper()
		_, _ = db.Collection("users").InsertOne(context.Background(), bson.M{
			"_id": id, "username": username, "federation": bson.M{"origin": testSiteID},
		})
	}

	// Helper: seed an org into the orgs collection
	seedOrg := func(t *testing.T, id, name, locationURL string) {
		t.Helper()
		_, _ = db.Collection("orgs").InsertOne(context.Background(), bson.M{
			"_id": id, "name": name, "locationUrl": locationURL,
		})
	}

	t.Run("AddMembers_IndividualUsers", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-iu-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		seedUser(t, "u-alice", "alice")
		seedUser(t, "u-bob", "bob")

		subj := subject.MemberAdd("requester", "am-iu-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID:  "am-iu-r1",
			Users:   []string{"alice", "bob"},
			History: model.HistoryConfig{Mode: model.HistoryModeNone},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		// Verify subscriptions created
		sub, err := store.GetSubscription(ctx, "alice", "am-iu-r1")
		require.NoError(t, err)
		assert.Equal(t, "u-alice", sub.User.ID)
		assert.True(t, HasRole(sub.Roles, model.RoleMember))
		assert.False(t, sub.SharedHistorySince.IsZero(), "SharedHistorySince should be set for mode=none")

		sub2, err := store.GetSubscription(ctx, "bob", "am-iu-r1")
		require.NoError(t, err)
		assert.Equal(t, "u-bob", sub2.User.ID)
	})

	t.Run("AddMembers_HistoryAll", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-ha-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		seedUser(t, "u-carol", "carol")

		subj := subject.MemberAdd("requester", "am-ha-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID:  "am-ha-r1",
			Users:   []string{"carol"},
			History: model.HistoryConfig{Mode: model.HistoryModeAll},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		sub, err := store.GetSubscription(ctx, "carol", "am-ha-r1")
		require.NoError(t, err)
		assert.True(t, sub.SharedHistorySince.IsZero(), "SharedHistorySince should be zero for mode=all")
	})

	t.Run("AddMembers_WithOrg", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-wo-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		seedOrg(t, "org-eng", "Engineering", testDomain)
		seedUser(t, "u-eng", "Engineering")

		subj := subject.MemberAdd("requester", "am-wo-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID: "am-wo-r1",
			Orgs:   []string{"org-eng"},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		// Org resolves to "Engineering" (local, matches currentDomain)
		sub, err := store.GetSubscription(ctx, "Engineering", "am-wo-r1")
		require.NoError(t, err)
		assert.Equal(t, "u-eng", sub.User.ID)

		// room_members should have org doc + individual doc
		members, err := store.GetRoomMembers(ctx, "am-wo-r1")
		require.NoError(t, err)
		assert.Equal(t, 2, len(members), "expected 1 org doc + 1 individual doc")
	})

	t.Run("AddMembers_BotsFiltered", func(t *testing.T) {
		ctx := context.Background()

		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-bf-r1", Name: "general", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		seedUser(t, "u-dave", "dave")

		subj := subject.MemberAdd("requester", "am-bf-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID: "am-bf-r1",
			Users:  []string{"notify.bot", "p_webhook", "dave"},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		// Only dave should have a subscription
		_, err := store.GetSubscription(ctx, "dave", "am-bf-r1")
		assert.NoError(t, err)
		_, err = store.GetSubscription(ctx, "notify.bot", "am-bf-r1")
		assert.Error(t, err, "bot should be filtered out")
		_, err = store.GetSubscription(ctx, "p_webhook", "am-bf-r1")
		assert.Error(t, err, "bot should be filtered out")
	})

	t.Run("AddMembers_ChannelSource", func(t *testing.T) {
		ctx := context.Background()

		// Source channel with subscriptions (no room_members)
		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-cs-source", Name: "source", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		require.NoError(t, store.BulkCreateSubscriptions(ctx, []*model.Subscription{
			{ID: "am-cs-ss1", User: model.SubscriptionUser{ID: "u-eve", Username: "eve"}, RoomID: "am-cs-source", SiteID: testSiteID, Roles: []model.Role{model.RoleMember}},
			{ID: "am-cs-ss2", User: model.SubscriptionUser{ID: "u-frank", Username: "frank"}, RoomID: "am-cs-source", SiteID: testSiteID, Roles: []model.Role{model.RoleMember}},
		}))
		seedUser(t, "u-eve", "eve")
		seedUser(t, "u-frank", "frank")

		// Target room
		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-cs-r1", Name: "target", Type: model.RoomTypeGroup, SiteID: testSiteID}))

		subj := subject.MemberAdd("requester", "am-cs-r1", testSiteID)
		req := model.AddMembersRequest{
			RoomID:   "am-cs-r1",
			Channels: []string{"am-cs-source"},
		}
		msg := natsRequest(t, nc, subj, req, ps)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		assert.Equal(t, "ok", resp["status"])

		// eve and frank should now be in the target room
		_, err := store.GetSubscription(ctx, "eve", "am-cs-r1")
		assert.NoError(t, err)
		_, err = store.GetSubscription(ctx, "frank", "am-cs-r1")
		assert.NoError(t, err)
	})

	t.Run("AddMembers_RoomAtCapacity", func(t *testing.T) {
		ctx := context.Background()

		// Create room with 999 existing subscriptions (via count)
		require.NoError(t, store.CreateRoom(ctx, &model.Room{ID: "am-rc-r1", Name: "full", Type: model.RoomTypeGroup, SiteID: testSiteID}))
		// Seed 1000 subscriptions to fill the room
		subs := make([]*model.Subscription, 1000)
		for i := range subs {
			subs[i] = &model.Subscription{
				ID: fmt.Sprintf("am-rc-s%d", i), User: model.SubscriptionUser{ID: fmt.Sprintf("u%d", i), Username: fmt.Sprintf("user%d", i)},
				RoomID: "am-rc-r1", SiteID: testSiteID, Roles: []model.Role{model.RoleMember},
			}
		}
		require.NoError(t, store.BulkCreateSubscriptions(ctx, subs))

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
