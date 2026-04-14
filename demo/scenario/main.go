// Command scenario runs an end-to-end demo of the room member management v2 feature.
//
// It seeds users into MongoDB, creates a room with an owner subscription, then issues
// add / promote / remove NATS requests against a running room-service. All NATS events
// emitted by room-worker (subscription updates, room member events, system messages,
// outbox replication) are captured and printed so the viewer can follow the flow.
//
// Usage (after `docker compose up -d` from the demo directory):
//
//	go run ./demo/scenario
//
// Environment overrides:
//
//	NATS_URL   default: nats://localhost:4222
//	MONGO_URI  default: mongodb://localhost:27017
//	MONGO_DB   default: chat_demo
//	SITE_ID    default: site-local
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/subject"
)

type config struct {
	NatsURL  string `env:"NATS_URL"  envDefault:"nats://localhost:4222"`
	MongoURI string `env:"MONGO_URI" envDefault:"mongodb://localhost:27017"`
	MongoDB  string `env:"MONGO_DB"  envDefault:"chat_demo"`
	SiteID   string `env:"SITE_ID"   envDefault:"site-local"`
}

// remoteSiteID is the fictitious home site of the cross-site user. Its only purpose is
// to demonstrate that room-worker publishes an outbox event when a member lives on a
// different site from the room.
const remoteSiteID = "site-remote"

const orgEngineering = "eng"

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatalf("demo failed: %v", err)
	}
}

func run() error {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// --- Connect to dependencies ------------------------------------------------

	section("1. Connect to NATS + MongoDB")
	nc, err := nats.Connect(cfg.NatsURL, nats.Name("demo-scenario"))
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer func() { _ = nc.Drain() }()

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		return fmt.Errorf("mongo connect: %w", err)
	}
	defer mongoutil.Disconnect(ctx, mongoClient)
	db := mongoClient.Database(cfg.MongoDB)

	// --- Reset state and seed data ---------------------------------------------

	section("2. Reset DB and seed users / room")
	if err := resetDB(ctx, db); err != nil {
		return fmt.Errorf("reset db: %w", err)
	}

	// Owner of the room — local site.
	alice := model.User{ID: uuid.New().String(), Account: "alice", SiteID: cfg.SiteID, SectID: orgEngineering, EngName: "Alice"}
	// Cross-site user to show outbox replication.
	bob := model.User{ID: uuid.New().String(), Account: "bob", SiteID: remoteSiteID, SectID: orgEngineering, EngName: "Bob"}
	// Local users that belong to the engineering org — these get resolved via `users.find({sectId: "eng"})`.
	carol := model.User{ID: uuid.New().String(), Account: "carol", SiteID: cfg.SiteID, SectID: orgEngineering, EngName: "Carol"}
	dave := model.User{ID: uuid.New().String(), Account: "dave", SiteID: cfg.SiteID, SectID: orgEngineering, EngName: "Dave"}
	// Bot account — should be filtered out even if present in the org.
	eveBot := model.User{ID: uuid.New().String(), Account: "eve.bot", SiteID: cfg.SiteID, SectID: orgEngineering, EngName: "Eve Bot"}

	if err := seedUsers(ctx, db, []model.User{alice, bob, carol, dave, eveBot}); err != nil {
		return fmt.Errorf("seed users: %w", err)
	}
	log.Printf("seeded users: alice(@%s), bob(@%s, cross-site), carol(@%s), dave(@%s), eve.bot(filtered)",
		cfg.SiteID, remoteSiteID, cfg.SiteID, cfg.SiteID)

	roomID := uuid.New().String()
	now := time.Now().UTC()
	room := model.Room{
		ID: roomID, Name: "demo-general", Type: model.RoomTypeGroup,
		CreatedBy: alice.ID, SiteID: cfg.SiteID, UserCount: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.Collection("rooms").InsertOne(ctx, room); err != nil {
		return fmt.Errorf("insert room: %w", err)
	}
	ownerSub := model.Subscription{
		ID: uuid.New().String(), User: model.SubscriptionUser{ID: alice.ID, Account: alice.Account},
		RoomID: roomID, SiteID: cfg.SiteID, Roles: []model.Role{model.RoleOwner}, JoinedAt: now,
	}
	if _, err := db.Collection("subscriptions").InsertOne(ctx, ownerSub); err != nil {
		return fmt.Errorf("insert owner subscription: %w", err)
	}
	log.Printf("created room %q (id=%s) owned by alice", room.Name, roomID)

	// --- Subscribe to all interesting NATS events ------------------------------

	section("3. Subscribe to NATS events")
	recorder := newEventRecorder()
	watches := []string{
		"chat.user.*.event.subscription.update",
		"chat.room.*.event.member",
		subject.MsgCanonicalWildcard(cfg.SiteID), // chat.msg.canonical.{site}.>
		subject.OutboxWildcard(cfg.SiteID),       // outbox.{site}.>
	}
	for _, pattern := range watches {
		if _, err := nc.Subscribe(pattern, recorder.handle(pattern)); err != nil {
			return fmt.Errorf("subscribe %s: %w", pattern, err)
		}
		log.Printf("watching %s", pattern)
	}
	if err := nc.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	// --- Scenario steps --------------------------------------------------------

	section("4. alice adds bob + carol to the room")
	addBobCarol, err := json.Marshal(model.AddMembersRequest{
		RoomID: roomID, Users: []string{"bob", "carol"},
		History: model.HistoryConfig{Mode: model.HistoryModeNone},
	})
	if err != nil {
		return fmt.Errorf("marshal add-members request: %w", err)
	}
	natsRequest(nc, subject.MemberAdd("alice", roomID, cfg.SiteID), addBobCarol)
	waitForEvents(recorder, 500*time.Millisecond)

	section("5. alice promotes bob (cross-site) to owner — expect role_updated outbox event")
	promoteBob, err := json.Marshal(model.UpdateRoleRequest{
		RoomID: roomID, Account: "bob", NewRole: model.RoleOwner,
	})
	if err != nil {
		return fmt.Errorf("marshal promote request: %w", err)
	}
	natsRequest(nc, subject.MemberRoleUpdate("alice", roomID, cfg.SiteID), promoteBob)
	waitForEvents(recorder, 500*time.Millisecond)

	section("6. alice adds the engineering org — org members resolved via users.sectId, bots filtered")
	addOrg, err := json.Marshal(model.AddMembersRequest{
		RoomID: roomID, Orgs: []string{orgEngineering},
		History: model.HistoryConfig{Mode: model.HistoryModeNone},
	})
	if err != nil {
		return fmt.Errorf("marshal add-org request: %w", err)
	}
	natsRequest(nc, subject.MemberAdd("alice", roomID, cfg.SiteID), addOrg)
	waitForEvents(recorder, 500*time.Millisecond)

	section("7. alice removes bob — expect member_removed event + outbox to site-remote")
	removeBob, err := json.Marshal(model.RemoveMemberRequest{
		RoomID: roomID, Account: "bob",
	})
	if err != nil {
		return fmt.Errorf("marshal remove request: %w", err)
	}
	natsRequest(nc, subject.MemberRemove("alice", roomID, cfg.SiteID), removeBob)
	waitForEvents(recorder, 500*time.Millisecond)

	// --- Final DB snapshot ------------------------------------------------------

	section("8. Final MongoDB state")
	printRoomState(ctx, db, roomID)

	section("9. Event summary")
	recorder.summary()

	log.Println()
	log.Println("demo complete — see docs/superpowers/specs/2026-04-10-room-member-management-v2-design.md for the full spec.")
	return nil
}

// --- helpers ----------------------------------------------------------------

func section(title string) {
	log.Println()
	log.Println("══ " + title + " " + strings.Repeat("═", max(0, 78-4-len(title))))
}

func resetDB(ctx context.Context, db *mongo.Database) error {
	for _, coll := range []string{"users", "rooms", "subscriptions", "room_members"} {
		if _, err := db.Collection(coll).DeleteMany(ctx, bson.M{}); err != nil {
			return fmt.Errorf("clear %s: %w", coll, err)
		}
	}
	return nil
}

func seedUsers(ctx context.Context, db *mongo.Database, users []model.User) error {
	docs := make([]interface{}, len(users))
	for i := range users {
		docs[i] = users[i]
	}
	_, err := db.Collection("users").InsertMany(ctx, docs)
	return err
}

func natsRequest(nc *nats.Conn, subj string, data []byte) {
	log.Printf("→ request  %s", subj)
	log.Printf("  payload  %s", string(data))
	msg, err := nc.Request(subj, data, 5*time.Second)
	if err != nil {
		log.Printf("  ERROR    %v", err)
		return
	}
	log.Printf("← reply    %s", string(msg.Data))
}

func waitForEvents(r *eventRecorder, d time.Duration) {
	time.Sleep(d)
	r.flushRecent()
}

func printRoomState(ctx context.Context, db *mongo.Database, roomID string) {
	var room model.Room
	if err := db.Collection("rooms").FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		log.Printf("get room: %v", err)
		return
	}
	log.Printf("room.userCount = %d", room.UserCount)

	cur, err := db.Collection("subscriptions").Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		log.Printf("list subs: %v", err)
		return
	}
	var subs []model.Subscription
	if err := cur.All(ctx, &subs); err != nil {
		log.Printf("decode subs: %v", err)
		return
	}
	log.Printf("subscriptions (%d):", len(subs))
	for i := range subs {
		log.Printf("  - %-10s roles=%v siteId=%s", subs[i].User.Account, subs[i].Roles, subs[i].SiteID)
	}

	cur, err = db.Collection("room_members").Find(ctx, bson.M{"rid": roomID})
	if err != nil {
		log.Printf("list room_members: %v", err)
		return
	}
	var members []model.RoomMember
	if err := cur.All(ctx, &members); err != nil {
		log.Printf("decode room_members: %v", err)
		return
	}
	log.Printf("room_members (%d):", len(members))
	for _, m := range members {
		log.Printf("  - type=%-10s id=%s account=%s", m.Member.Type, m.Member.ID, m.Member.Account)
	}
}

// --- event recorder ---------------------------------------------------------

type observedEvent struct {
	Pattern string
	Subject string
	Data    []byte
	When    time.Time
}

type eventRecorder struct {
	mu     sync.Mutex
	events []observedEvent
	cursor int // index up to which events have already been printed
}

func newEventRecorder() *eventRecorder { return &eventRecorder{} }

func (r *eventRecorder) handle(pattern string) nats.MsgHandler {
	return func(m *nats.Msg) {
		r.mu.Lock()
		r.events = append(r.events, observedEvent{
			Pattern: pattern, Subject: m.Subject, Data: m.Data, When: time.Now(),
		})
		r.mu.Unlock()
	}
}

// flushRecent prints events captured since the previous flush.
func (r *eventRecorder) flushRecent() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cursor == len(r.events) {
		log.Printf("  (no events)")
		return
	}
	for _, evt := range r.events[r.cursor:] {
		log.Printf("  ▸ %s", evt.Subject)
		log.Printf("    %s", prettyJSON(evt.Data))
	}
	r.cursor = len(r.events)
}

func (r *eventRecorder) summary() {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := make(map[string]int)
	for _, evt := range r.events {
		counts[evt.Pattern]++
	}
	for _, w := range []string{
		"chat.user.*.event.subscription.update",
		"chat.room.*.event.member",
		"chat.msg.canonical.*.>",
		"outbox.*.>",
	} {
		// Lookup with the wildcard pattern the handler was registered under.
		matched := 0
		for p, n := range counts {
			if wildcardKey(p) == wildcardKey(w) {
				matched += n
			}
		}
		log.Printf("  %-45s %d", w, matched)
	}
	log.Printf("  (total events captured: %d)", len(r.events))
}

// wildcardKey normalises a pattern so the summary tallies match what was registered.
func wildcardKey(p string) string {
	// Matching on the first two tokens is enough to distinguish the four streams we
	// care about in this demo.
	prefix := ""
	depth := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			depth++
			if depth == 2 {
				return prefix
			}
		}
		prefix += string(p[i])
	}
	return prefix
}

func prettyJSON(data []byte) string {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	out, err := json.MarshalIndent(v, "    ", "  ")
	if err != nil {
		return string(data)
	}
	return string(out)
}
