// Demo client for history-service. Seeds realistic test data into MongoDB and
// Cassandra, then exercises all endpoints with formatted output.
//
// Two-terminal setup:
//
//	Terminal 1:  cd history-service/docker-local && docker compose up -d
//	Terminal 2:  cd history-service/docker-local && docker compose logs -f history
//	Terminal 3:  cd history-service-demo && docker compose up
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

// --- Cassandra UDT types for seeding (mirrors history-service/internal/models) ---

type participantUDT struct {
	ID          string
	Account     string
	EngName     string
	CompanyName string
	AppID       string
	AppName     string
	IsBot       bool
}

func (p *participantUDT) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, p.ID)
	case "account":
		return gocql.Marshal(info, p.Account)
	case "eng_name":
		return gocql.Marshal(info, p.EngName)
	case "company_name":
		return gocql.Marshal(info, p.CompanyName)
	case "app_id":
		return gocql.Marshal(info, p.AppID)
	case "app_name":
		return gocql.Marshal(info, p.AppName)
	case "is_bot":
		return gocql.Marshal(info, p.IsBot)
	}
	return nil, nil
}

type fileUDT struct {
	ID   string
	Name string
	Type string
}

func (f *fileUDT) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, f.ID)
	case "name":
		return gocql.Marshal(info, f.Name)
	case "type":
		return gocql.Marshal(info, f.Type)
	}
	return nil, nil
}

type cardUDT struct {
	Template string
	Data     []byte
}

func (c *cardUDT) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "template":
		return gocql.Marshal(info, c.Template)
	case "data":
		return gocql.Marshal(info, c.Data)
	}
	return nil, nil
}

type cardActionUDT struct {
	Verb        string
	Text        string
	CardID      string
	DisplayText string
	HideExecLog bool
	CardTmID    string
	Data        []byte
}

func (ca *cardActionUDT) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "verb":
		return gocql.Marshal(info, ca.Verb)
	case "text":
		return gocql.Marshal(info, ca.Text)
	case "card_id":
		return gocql.Marshal(info, ca.CardID)
	case "display_text":
		return gocql.Marshal(info, ca.DisplayText)
	case "hide_exec_log":
		return gocql.Marshal(info, ca.HideExecLog)
	case "card_tmid":
		return gocql.Marshal(info, ca.CardTmID)
	case "data":
		return gocql.Marshal(info, ca.Data)
	}
	return nil, nil
}

type quotedParentMessageUDT struct {
	MessageID   string
	RoomID      string
	Sender      participantUDT
	CreatedAt   time.Time
	Msg         string
	Mentions    []participantUDT
	Attachments [][]byte
	MessageLink string
}

func (q *quotedParentMessageUDT) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "message_id":
		return gocql.Marshal(info, q.MessageID)
	case "room_id":
		return gocql.Marshal(info, q.RoomID)
	case "sender":
		return gocql.Marshal(info, &q.Sender)
	case "created_at":
		return gocql.Marshal(info, q.CreatedAt)
	case "msg":
		return gocql.Marshal(info, q.Msg)
	case "mentions":
		return gocql.Marshal(info, q.Mentions)
	case "attachments":
		return gocql.Marshal(info, q.Attachments)
	case "message_link":
		return gocql.Marshal(info, q.MessageLink)
	}
	return nil, nil
}

// --- Demo data ---

const (
	siteID  = "site-local"
	account = "demo-user"
	roomID  = "demo-room"
)

var users = []participantUDT{
	{ID: "alice", Account: "alice", EngName: "Alice Chen", CompanyName: "Acme Corp", AppID: "web", AppName: "Chat Web"},
	{ID: "bob", Account: "bob", EngName: "Bob Wang", CompanyName: "Acme Corp", AppID: "mobile", AppName: "Chat Mobile"},
	{ID: "charlie", Account: "charlie", EngName: "Charlie Liu", CompanyName: "Acme Corp", AppID: "bot-1", AppName: "CI Bot", IsBot: true},
}

// seedRow holds ALL 24 columns of messages_by_room for a single message.
type seedRow struct {
	senderIdx              int
	targetIdx              int // -1 = no target
	msg                    string
	mentions               []int // indices into users
	attachments            [][]byte
	file                   *fileUDT
	card                   *cardUDT
	cardAction             *cardActionUDT
	tshow                  bool
	threadParentID         string // message_id of thread parent
	threadParentOffsetMins int    // 0 = nil
	quotedParentMessage    *quotedParentMessageUDT
	visibleTo              string
	unread                 bool
	reactions              map[string]int // emoji → reactBy user index
	deleted                bool
	msgType                string
	sysMsgData             []byte
	siteID                 string
	editedOffsetMins       int // 0 = nil, positive = edited at ts+offset
	updatedOffsetMins      int // 0 = nil
}

var chatMessages = []seedRow{
	{
		senderIdx: 0, targetIdx: -1,
		msg:       "Hey team! The new deployment pipeline is ready for review",
		reactions: map[string]int{"thumbsup": 1},
		unread:    true,
	},
	{
		senderIdx: 1, targetIdx: 0,
		msg:       "Nice work Alice! I'll take a look this afternoon",
		visibleTo: "",
	},
	{
		senderIdx: 0, targetIdx: -1,
		msg:  "Here's the architecture doc for reference",
		file: &fileUDT{ID: "f1", Name: "pipeline-arch.pdf", Type: "application/pdf"},
		attachments: [][]byte{
			[]byte("blob-attachment-preview-1"),
		},
	},
	{
		senderIdx: 2, targetIdx: -1,
		msg: "I've run the automated checks - all 47 tests passing",
		card: &cardUDT{
			Template: "ci-report",
			Data:     []byte(`{"passed":47,"failed":0,"skipped":0}`),
		},
		reactions: map[string]int{"white_check_mark": 0},
	},
	{
		senderIdx: 1, targetIdx: -1,
		msg:      "Quick question - are we using blue-green or canary for the rollout?",
		mentions: []int{0},
	},
	{
		senderIdx: 0, targetIdx: -1,
		msg:               "Canary with 10% traffic initially, then ramp up over 30 min",
		editedOffsetMins:  2,
		updatedOffsetMins: 2,
	},
	{
		senderIdx: 1, targetIdx: -1,
		msg: "Perfect. That matches what I had in mind",
	},
	{
		senderIdx: 2, targetIdx: -1,
		msg: "Monitoring dashboard is configured. I'll alert on error rate > 0.1%",
		cardAction: &cardActionUDT{
			Verb:        "open_dashboard",
			Text:        "Open Dashboard",
			CardID:      "card-monitor-1",
			DisplayText: "Click to open monitoring",
			HideExecLog: false,
			CardTmID:    "tmpl-monitor",
			Data:        []byte(`{"url":"https://grafana.internal/d/deploy"}`),
		},
		reactions: map[string]int{"eyes": 1},
	},
	{
		senderIdx: 0, targetIdx: -1,
		msg:                    "Let's target Thursday for the first canary. @bob can you prep staging?",
		mentions:               []int{1},
		tshow:                  true,
		threadParentID:         "msg-005",
		threadParentOffsetMins: -9, // points back to msg-005 (3 min intervals, msg-005 is at offset 15)
		quotedParentMessage: &quotedParentMessageUDT{
			MessageID:   "msg-005",
			RoomID:      roomID,
			Sender:      users[0],
			Msg:         "Canary with 10% traffic initially, then ramp up over 30 min",
			MessageLink: "https://chat.example.com/rooms/demo-room/msg-005",
		},
	},
	{
		senderIdx: 1, targetIdx: -1,
		msg:       "On it! Will have staging ready by EOD Wednesday",
		reactions: map[string]int{"rocket": 0},
	},
	{
		senderIdx: 0, targetIdx: -1,
		msg:        "Great - meeting at 2pm Thursday to kick off. Everyone good?",
		mentions:   []int{1, 2},
		reactions:  map[string]int{"thumbsup": 1},
		msgType:    "meeting_scheduled",
		sysMsgData: []byte(`{"time":"2026-04-03T14:00:00Z","title":"Canary Kickoff"}`),
		siteID:     "site-remote",
	},
	{
		senderIdx: 2, targetIdx: -1,
		msg:     "I'll monitor the rollout metrics in real-time",
		deleted: false,
	},
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
		User:               model.SubscriptionUser{ID: account, Account: account},
		RoomID:             roomID,
		SiteID:             siteID,
		Role:               model.RoleMember,
		HistorySharedSince: &joinTime,
		JoinedAt:           joinTime,
	}
	_, _ = db.Collection("subscriptions").DeleteMany(ctx, map[string]string{"u._id": account, "roomId": roomID})
	if _, err = db.Collection("subscriptions").InsertOne(ctx, sub); err != nil {
		return fmt.Errorf("seed subscription: %w", err)
	}
	info("Subscription: user=%s room=%s joined=%s", account, roomID, joinTime.Format("15:04:05"))

	// Clean old demo messages
	if err := cassSession.Query(`DELETE FROM messages_by_room WHERE room_id = ?`, roomID).Exec(); err != nil {
		return fmt.Errorf("cleaning old messages: %w", err)
	}

	baseTime := joinTime.Add(5 * time.Minute)
	type seeded struct {
		id   string
		time time.Time
	}
	var msgs []seeded

	const insertCQL = `INSERT INTO messages_by_room (
		room_id, created_at, message_id, sender, target_user, msg,
		mentions, attachments, file, card, card_action,
		tshow, thread_parent_id, thread_parent_created_at,
		quoted_parent_message, visible_to, unread,
		reactions, deleted, type, sys_msg_data,
		site_id, edited_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	for i := range chatMessages {
		cm := &chatMessages[i]
		ts := baseTime.Add(time.Duration(i) * 3 * time.Minute)
		msgID := fmt.Sprintf("msg-%03d", i)

		sender := users[cm.senderIdx]

		var target *participantUDT
		if cm.targetIdx >= 0 {
			t := users[cm.targetIdx]
			target = &t
		}

		var mentions []participantUDT
		for _, mi := range cm.mentions {
			mentions = append(mentions, users[mi])
		}

		var reactions map[string][]participantUDT
		if len(cm.reactions) > 0 {
			reactions = make(map[string][]participantUDT)
			for emoji, userIdx := range cm.reactions {
				reactions[emoji] = []participantUDT{users[userIdx]}
			}
		}

		var threadParent *time.Time
		if cm.threadParentOffsetMins != 0 {
			tp := ts.Add(time.Duration(cm.threadParentOffsetMins) * time.Minute)
			threadParent = &tp
		}

		var editedAt *time.Time
		if cm.editedOffsetMins > 0 {
			ea := ts.Add(time.Duration(cm.editedOffsetMins) * time.Minute)
			editedAt = &ea
		}

		var updatedAt *time.Time
		if cm.updatedOffsetMins > 0 {
			ua := ts.Add(time.Duration(cm.updatedOffsetMins) * time.Minute)
			updatedAt = &ua
		}

		if err := cassSession.Query(insertCQL,
			roomID, ts, msgID,
			&sender, target, cm.msg,
			mentions, cm.attachments, cm.file, cm.card, cm.cardAction,
			cm.tshow, cm.threadParentID, threadParent,
			cm.quotedParentMessage, cm.visibleTo, cm.unread,
			reactions, cm.deleted, cm.msgType, cm.sysMsgData,
			cm.siteID, editedAt, updatedAt,
		).Exec(); err != nil {
			return fmt.Errorf("seed message %d: %w", i, err)
		}
		msgs = append(msgs, seeded{msgID, ts})
	}
	ok("Seeded %d messages (all 24 columns) from %d participants", len(msgs), len(users))
	fmt.Println()
	for i := range chatMessages {
		cm := &chatMessages[i]
		tags := ""
		if cm.file != nil {
			tags += " [file]"
		}
		if cm.card != nil {
			tags += " [card]"
		}
		if cm.cardAction != nil {
			tags += " [action]"
		}
		if cm.tshow {
			tags += " [thread]"
		}
		if cm.editedOffsetMins > 0 {
			tags += " [edited]"
		}
		if cm.quotedParentMessage != nil {
			tags += " [quoted]"
		}
		if cm.msgType != "" {
			tags += " [type:" + cm.msgType + "]"
		}
		if cm.siteID != "" {
			tags += " [site:" + cm.siteID + "]"
		}
		if len(cm.reactions) > 0 {
			for emoji := range cm.reactions {
				tags += " :" + emoji + ":"
			}
		}
		fmt.Printf("    %s  %-8s  %s%s\n", msgs[i].time.Format("15:04"), users[cm.senderIdx].Account, truncate(cm.msg, 45), tags)
	}

	timeout := 5 * time.Second

	// --- 1. LoadHistory ---
	header("1. LoadHistory - last 5 messages")
	info("Fetch the most recent 5 messages (no 'before' = now)")
	historyReq, _ := json.Marshal(map[string]any{
		"limit": 5,
	})
	printResponse(request(nc, subject.MsgHistory(account, roomID, siteID), historyReq, timeout))

	// --- 2. LoadHistory with Before ---
	header("2. LoadHistory - paginate backwards from msg-007")
	info("Pass 'before' = msg-007 timestamp to get older messages")
	historyBefore, _ := json.Marshal(map[string]any{
		"before": msgs[7].time.UnixMilli(),
		"limit":  3,
	})
	printResponse(request(nc, subject.MsgHistory(account, roomID, siteID), historyBefore, timeout))

	// --- 3. LoadNextMessages ---
	header("3. LoadNextMessages - from msg-003 forward")
	info("Fetch messages after msg-003 timestamp (ascending order)")
	nextReq, _ := json.Marshal(map[string]any{
		"after": msgs[3].time.UnixMilli(),
		"limit": 4,
	})
	printResponse(request(nc, subject.MsgNext(account, roomID, siteID), nextReq, timeout))

	// --- 4. GetMessageByID - file message ---
	header("4. GetMessageByID - msg-002 (file attachment)")
	getFileReq, _ := json.Marshal(map[string]any{
		"messageId": "msg-002",
	})
	printResponse(request(nc, subject.MsgGet(account, roomID, siteID), getFileReq, timeout))

	// --- 5. GetMessageByID - card message ---
	header("5. GetMessageByID - msg-003 (CI report card)")
	getCardReq, _ := json.Marshal(map[string]any{
		"messageId": "msg-003",
	})
	printResponse(request(nc, subject.MsgGet(account, roomID, siteID), getCardReq, timeout))

	// --- 6. GetMessageByID - card action message ---
	header("6. GetMessageByID - msg-007 (card action + reactions)")
	getActionReq, _ := json.Marshal(map[string]any{
		"messageId": "msg-007",
	})
	printResponse(request(nc, subject.MsgGet(account, roomID, siteID), getActionReq, timeout))

	// --- 7. GetMessageByID - with type + remote site + mentions ---
	header("7. GetMessageByID - msg-010 (type + remote site + mentions)")
	getSysReq, _ := json.Marshal(map[string]any{
		"messageId": "msg-010",
	})
	printResponse(request(nc, subject.MsgGet(account, roomID, siteID), getSysReq, timeout))

	// --- 8. LoadSurroundingMessages ---
	header("8. LoadSurroundingMessages - context around msg-005")
	info("3 before + msg-005 + 3 after = up to 7 messages")
	surroundReq, _ := json.Marshal(map[string]any{
		"messageId": "msg-005",
		"limit":     7,
	})
	printResponse(request(nc, subject.MsgSurrounding(account, roomID, siteID), surroundReq, timeout))

	// --- Error cases ---
	header("9. Error: message not found")
	notFoundReq, _ := json.Marshal(map[string]any{
		"messageId": "msg-nonexistent",
	})
	printResponse(request(nc, subject.MsgGet(account, roomID, siteID), notFoundReq, timeout))

	header("10. Error: not subscribed (unknown user)")
	notSubReq, _ := json.Marshal(map[string]any{
		"limit": 5,
	})
	printResponse(request(nc, subject.MsgHistory("unknown-user", roomID, siteID), notSubReq, timeout))

	fmt.Println()
	fmt.Println("  =============================================")
	fmt.Println("  Demo complete - all endpoints exercised")
	fmt.Println("  =============================================")
	fmt.Println()
	return nil
}

func request(nc *nats.Conn, subj string, data []byte, timeout time.Duration) []byte {
	dim("-> %s", subj)
	msg, err := nc.Request(subj, data, timeout)
	if err != nil {
		warn("Request failed: %v", err)
		return []byte(`{"error":"request failed"}`)
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

func warn(format string, args ...any) {
	fmt.Printf("  [!] %s\n", fmt.Sprintf(format, args...))
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
