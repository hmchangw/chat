// Demo client for history-service. Seeds realistic test data into MongoDB and
// Cassandra, then exercises all endpoints with formatted output.
//
// Two-terminal setup:
//
//	Terminal 1:  cd history-service/docker-local && docker compose up
//	Terminal 2:  cd history-service-demo && docker compose up
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

type participant struct {
	name    string
	engName string
	isBot   bool
}

var users = []participant{
	{"alice", "Alice Chen", false},
	{"bob", "Bob Wang", false},
	{"charlie", "Charlie Liu", true},
}

var chatMessages = []struct {
	senderIdx int
	text      string
	fileName  string
	fileType  string
	mentions  []int
	reaction  string
	reactBy   int
}{
	{0, "Hey team! The new deployment pipeline is ready for review", "", "", nil, "thumbsup", 1},
	{1, "Nice work Alice! I'll take a look this afternoon", "", "", nil, "", 0},
	{0, "Here's the architecture doc for reference", "pipeline-arch.pdf", "application/pdf", nil, "", 0},
	{2, "I've run the automated checks — all 47 tests passing", "", "", nil, "white_check_mark", 0},
	{1, "Quick question — are we using blue-green or canary for the rollout?", "", "", []int{0}, "", 0},
	{0, "Canary with 10% traffic initially, then ramp up over 30 min", "", "", nil, "", 0},
	{1, "Perfect. That matches what I had in mind", "", "", nil, "", 0},
	{2, "Monitoring dashboard is configured. I'll alert on error rate > 0.1%", "", "", nil, "eyes", 1},
	{0, "Let's target Thursday for the first canary. @bob can you prep staging?", "", "", []int{1}, "", 0},
	{1, "On it! Will have staging ready by EOD Wednesday", "", "", nil, "rocket", 0},
	{0, "Great — meeting at 2pm Thursday to kick off. Everyone good?", "", "", []int{1, 2}, "thumbsup", 1},
	{2, "I'll monitor the rollout metrics in real-time", "", "", nil, "", 0},
}

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

	header("Connecting to infrastructure")

	nc, err := nats.Connect(natsURL)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()
	ok("NATS connected")

	mongoClient, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		return fmt.Errorf("mongodb connect: %w", err)
	}
	defer mongoClient.Disconnect(ctx) //nolint:errcheck
	db := mongoClient.Database(mongoDB)
	ok("MongoDB connected")

	cluster := gocql.NewCluster(strings.Split(cassHosts, ",")...)
	cluster.Keyspace = cassKeyspace
	cluster.Consistency = gocql.One
	cassSession, err := cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("cassandra connect: %w", err)
	}
	defer cassSession.Close()
	ok("Cassandra connected")

	// --- Seed ---
	header("Seeding test data")

	joinTime := time.Now().UTC().Add(-2 * time.Hour)
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
	info("Subscription: user=%s room=%s joined=%s", username, roomID, joinTime.Format("15:04:05"))

	baseTime := joinTime.Add(5 * time.Minute)
	type seeded struct {
		id   string
		time time.Time
	}
	var msgs []seeded

	for i, cm := range chatMessages {
		ts := baseTime.Add(time.Duration(i) * 3 * time.Minute)
		msgID := fmt.Sprintf("msg-%03d", i)
		sender := users[cm.senderIdx]

		senderMap := map[string]any{
			"id": sender.name, "user_name": sender.name,
			"eng_name": sender.engName, "is_bot": sender.isBot,
			"company_name": "", "app_id": "", "app_name": "",
		}

		var fileMap map[string]any
		if cm.fileName != "" {
			fileMap = map[string]any{"id": fmt.Sprintf("f%d", i), "name": cm.fileName, "type": cm.fileType}
		}

		var mentionsList []map[string]any
		for _, mi := range cm.mentions {
			u := users[mi]
			mentionsList = append(mentionsList, map[string]any{
				"id": u.name, "user_name": u.name, "eng_name": u.engName,
				"is_bot": u.isBot, "company_name": "", "app_id": "", "app_name": "",
			})
		}

		var reactionsMap map[string][]map[string]any
		if cm.reaction != "" {
			ru := users[cm.reactBy]
			reactionsMap = map[string][]map[string]any{
				cm.reaction: {{
					"id": ru.name, "user_name": ru.name, "eng_name": ru.engName,
					"is_bot": ru.isBot, "company_name": "", "app_id": "", "app_name": "",
				}},
			}
		}

		if err := cassSession.Query(
			`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, file, mentions, reactions) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			roomID, ts, msgID, senderMap, cm.text, fileMap, mentionsList, reactionsMap,
		).Exec(); err != nil {
			return fmt.Errorf("seed message %d: %w", i, err)
		}
		msgs = append(msgs, seeded{msgID, ts})
	}
	ok("Seeded %d messages from %d participants", len(msgs), len(users))
	fmt.Println()
	for i, cm := range chatMessages {
		tag := ""
		if cm.fileName != "" {
			tag = " [file]"
		}
		if cm.reaction != "" {
			tag += " :" + cm.reaction + ":"
		}
		fmt.Printf("    %s  %-8s  %s%s\n", msgs[i].time.Format("15:04"), users[cm.senderIdx].name, truncate(cm.text, 50), tag)
	}

	timeout := 5 * time.Second

	// --- 1. LoadHistory ---
	header("1. LoadHistory — last 5 messages")
	info("Fetch the most recent 5 messages (no 'before' = now)")
	historyReq, _ := json.Marshal(map[string]any{
		"roomId": roomID,
		"limit":  5,
	})
	printResponse(request(nc, subject.MsgHistory(username, roomID, siteID), historyReq, timeout))

	// --- 2. LoadHistory with Before ---
	header("2. LoadHistory — paginate backwards from msg-007")
	info("Pass 'before' = msg-007 timestamp to get older messages")
	historyBefore, _ := json.Marshal(map[string]any{
		"roomId": roomID,
		"before": msgs[7].time.UnixMilli(),
		"limit":  3,
	})
	printResponse(request(nc, subject.MsgHistory(username, roomID, siteID), historyBefore, timeout))

	// --- 3. LoadNextMessages ---
	header("3. LoadNextMessages — from msg-003 forward")
	info("Fetch messages after msg-003 timestamp (ascending order)")
	nextReq, _ := json.Marshal(map[string]any{
		"roomId": roomID,
		"after":  msgs[3].time.UnixMilli(),
		"limit":  4,
	})
	printResponse(request(nc, subject.MsgNext(username, roomID, siteID), nextReq, timeout))

	// --- 4. GetMessageByID ---
	header("4. GetMessageByID — fetch msg-002 (has file attachment)")
	getReq, _ := json.Marshal(map[string]any{
		"roomId":    roomID,
		"messageId": "msg-002",
	})
	printResponse(request(nc, subject.MsgGet(username, roomID, siteID), getReq, timeout))

	// --- 5. LoadSurroundingMessages ---
	header("5. LoadSurroundingMessages — context around msg-005")
	info("3 before + msg-005 + 3 after = up to 7 messages")
	surroundReq, _ := json.Marshal(map[string]any{
		"roomId":    roomID,
		"messageId": "msg-005",
		"limit":     7,
	})
	printResponse(request(nc, subject.MsgSurrounding(username, roomID, siteID), surroundReq, timeout))

	// --- Error cases ---
	header("6. Error: message not found")
	notFoundReq, _ := json.Marshal(map[string]any{
		"roomId":    roomID,
		"messageId": "msg-nonexistent",
	})
	printResponse(request(nc, subject.MsgGet(username, roomID, siteID), notFoundReq, timeout))

	header("7. Error: not subscribed (unknown user)")
	notSubReq, _ := json.Marshal(map[string]any{
		"roomId": roomID,
		"limit":  5,
	})
	printResponse(request(nc, subject.MsgHistory("unknown-user", roomID, siteID), notSubReq, timeout))

	fmt.Println()
	fmt.Println("  =============================================")
	fmt.Println("  Demo complete — all endpoints exercised")
	fmt.Println("  =============================================")
	fmt.Println()
	return nil
}

func request(nc *nats.Conn, subj string, data []byte, timeout time.Duration) []byte {
	dim("-> %s", subj)
	msg, err := nc.Request(subj, data, timeout)
	if err != nil {
		log.Fatalf("  Request failed: %v", err)
	}
	return msg.Data
}

func printResponse(data []byte) {
	var pretty json.RawMessage
	if err := json.Unmarshal(data, &pretty); err != nil {
		fmt.Printf("    %s\n", string(data))
		return
	}
	formatted, _ := json.MarshalIndent(pretty, "    ", "  ")
	fmt.Printf("    %s\n", string(formatted))
}

func header(s string) {
	fmt.Printf("\n  -- %s --\n", s)
}

func ok(format string, args ...any) {
	fmt.Printf("  [OK] %s\n", fmt.Sprintf(format, args...))
}

func info(format string, args ...any) {
	fmt.Printf("  [i] %s\n", fmt.Sprintf(format, args...))
}

func dim(format string, args ...any) {
	fmt.Printf("    %s\n", fmt.Sprintf(format, args...))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "..."
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
