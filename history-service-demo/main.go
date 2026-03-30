// Demo client for history-service. Seeds test data into MongoDB and Cassandra,
// then sends NATS requests to all 4 endpoints and prints the responses.
//
// Prerequisites:
//  1. docker compose up -d (from history-service/docker-local/)
//  2. go run ./history-service/cmd/ (with env vars from .env)
//  3. go run ./history-service/demo/
//
// The demo uses the same NATS_URL, MONGO_URI, and CASSANDRA_HOSTS env vars.
// If not set, defaults to localhost.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

const (
	siteID   = "site-local"
	username = "demo-user"
	roomID   = "demo-room"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()

	natsURL := envOr("NATS_URL", "nats://localhost:4222")
	mongoURI := envOr("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOr("MONGO_DB", "chat")
	cassHosts := envOr("CASSANDRA_HOSTS", "localhost")
	cassKeyspace := envOr("CASSANDRA_KEYSPACE", "chat")

	nc, err := nats.Connect(natsURL)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()
	fmt.Println("✓ Connected to NATS")

	mongoClient, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		return fmt.Errorf("mongodb connect: %w", err)
	}
	defer mongoClient.Disconnect(ctx) //nolint:errcheck
	db := mongoClient.Database(mongoDB)
	fmt.Println("✓ Connected to MongoDB")

	cluster := gocql.NewCluster(strings.Split(cassHosts, ",")...)
	cluster.Keyspace = cassKeyspace
	cluster.Consistency = gocql.One
	cassSession, err := cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("cassandra connect: %w", err)
	}
	defer cassSession.Close()
	fmt.Println("✓ Connected to Cassandra")

	// --- Seed test data ---
	fmt.Println("\n--- Seeding test data ---")

	joinTime := time.Now().UTC().Add(-1 * time.Hour)
	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: username, Username: username},
		RoomID:             roomID,
		SiteID:             siteID,
		Role:               model.RoleMember,
		HistorySharedSince: &joinTime,
		JoinedAt:           joinTime,
	}
	_, _ = db.Collection("subscriptions").DeleteMany(ctx, map[string]string{"u._id": username, "roomId": roomID})
	if _, err = db.Collection("subscriptions").InsertOne(ctx, sub); err != nil {
		return fmt.Errorf("seed subscription: %w", err)
	}
	fmt.Printf("  Subscription: username=%s roomID=%s since=%s\n", username, roomID, joinTime.Format(time.RFC3339))

	baseTime := joinTime.Add(5 * time.Minute)
	var messageTimes []time.Time
	for i := 0; i < 10; i++ {
		msgTime := baseTime.Add(time.Duration(i) * 5 * time.Minute)
		msgID := fmt.Sprintf("msg-%03d", i)
		messageTimes = append(messageTimes, msgTime)

		if err := cassSession.Query(
			`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
			roomID, msgTime, msgID, username, fmt.Sprintf("Hello from message %d!", i),
		).Exec(); err != nil {
			return fmt.Errorf("seed message %d: %w", i, err)
		}
	}
	fmt.Printf("  Seeded 10 messages (msg-000 through msg-009)\n")

	// --- Test all 4 endpoints ---
	timeout := 5 * time.Second

	// 1. LoadHistory
	fmt.Println("\n--- 1. LoadHistory (last 5 messages, lastSeen=msg-003) ---")
	historyReq, _ := json.Marshal(map[string]any{
		"roomId":   roomID,
		"limit":    5,
		"lastSeen": messageTimes[3].Format(time.RFC3339Nano),
	})
	printJSON("Response", request(nc, subject.MsgHistory(username, roomID, siteID), historyReq, timeout))

	// 2. LoadNextMessages
	fmt.Println("\n--- 2. LoadNextMessages (after msg-005, limit 3) ---")
	nextReq, _ := json.Marshal(map[string]any{
		"roomId": roomID,
		"after":  messageTimes[5].Format(time.RFC3339Nano),
		"limit":  3,
	})
	printJSON("Response", request(nc, subject.MsgNext(username, roomID, siteID), nextReq, timeout))

	// 3. GetMessageByID
	fmt.Println("\n--- 3. GetMessageByID (msg-005) ---")
	getReq, _ := json.Marshal(map[string]any{
		"roomId":    roomID,
		"messageId": "msg-005",
	})
	printJSON("Response", request(nc, subject.MsgGet(username, roomID, siteID), getReq, timeout))

	// 4. LoadSurroundingMessages
	fmt.Println("\n--- 4. LoadSurroundingMessages (around msg-005, limit 6) ---")
	surroundReq, _ := json.Marshal(map[string]any{
		"roomId":    roomID,
		"messageId": "msg-005",
		"limit":     6,
	})
	printJSON("Response", request(nc, subject.MsgSurrounding(username, roomID, siteID), surroundReq, timeout))

	// --- Error cases ---

	// 5. Forbidden — user not subscribed to room
	fmt.Println("\n--- 5. ERROR: Not subscribed to room ---")
	forbiddenReq, _ := json.Marshal(map[string]any{
		"roomId": "room-nobody-joined",
	})
	printJSON("Response", request(nc, subject.MsgHistory(username, "room-nobody-joined", siteID), forbiddenReq, timeout))

	// 6. Not found — message doesn't exist
	fmt.Println("\n--- 6. ERROR: Message not found ---")
	notFoundReq, _ := json.Marshal(map[string]any{
		"roomId":    roomID,
		"messageId": "msg-nonexistent",
	})
	printJSON("Response", request(nc, subject.MsgGet(username, roomID, siteID), notFoundReq, timeout))

	// 7. Bad request — invalid timestamp format
	fmt.Println("\n--- 7. ERROR: Invalid timestamp ---")
	badTimestampReq, _ := json.Marshal(map[string]any{
		"roomId": roomID,
		"before": "not-a-valid-timestamp",
	})
	printJSON("Response", request(nc, subject.MsgHistory(username, roomID, siteID), badTimestampReq, timeout))

	fmt.Println("\n✓ Demo complete!")
	return nil
}

func request(nc *nats.Conn, subj string, data []byte, timeout time.Duration) []byte {
	fmt.Printf("  → Subject: %s\n", subj)
	msg, err := nc.Request(subj, data, timeout)
	if err != nil {
		log.Fatalf("  Request failed: %v", err)
	}
	return msg.Data
}

func printJSON(label string, data []byte) {
	var pretty json.RawMessage
	if err := json.Unmarshal(data, &pretty); err != nil {
		fmt.Printf("  %s: %s\n", label, string(data))
		return
	}
	formatted, _ := json.MarshalIndent(pretty, "  ", "  ")
	fmt.Printf("  %s:\n  %s\n", label, string(formatted))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
