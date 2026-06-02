package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/msgbucket"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// errMessageNotFound is returned by GetMessageSender when the message row is
// missing from Cassandra. Handler code checks for this sentinel to ack-and-skip
// instead of NAK'ing (which would cause infinite JetStream redelivery).
var errMessageNotFound = errors.New("message not found")

// cassParticipant maps to the Cassandra "Participant" UDT.
// cql struct tags tell gocql's reflection-based UDT marshaler how to map each
// Go field to its Cassandra UDT field name. Without these tags, gocql would
// lowercase the Go field names (e.g. "EngName" → "engname") which would not
// match the snake_case UDT fields (e.g. "eng_name").
type cassParticipant struct {
	ID          string `cql:"id"`
	EngName     string `cql:"eng_name"`
	CompanyName string `cql:"company_name"` // ChineseName
	Account     string `cql:"account"`
	AppID       string `cql:"app_id"`
	AppName     string `cql:"app_name"`
	IsBot       bool   `cql:"is_bot"`
}

// toMentionSet converts []model.Participant to []*cassParticipant for binding
// to a Cassandra SET<FROZEN<"Participant">> column.
func toMentionSet(mentions []model.Participant) []*cassParticipant {
	if len(mentions) == 0 {
		return nil
	}
	result := make([]*cassParticipant, len(mentions))
	for i, m := range mentions {
		result[i] = &cassParticipant{
			ID:          m.UserID,
			EngName:     m.EngName,
			CompanyName: m.ChineseName,
			Account:     m.Account,
		}
	}
	return result
}

// CassandraStore implements Store using a Cassandra session.
type CassandraStore struct {
	cassSession *gocql.Session
	bucket      msgbucket.Sizer
}

func NewCassandraStore(session *gocql.Session, bucket msgbucket.Sizer) *CassandraStore {
	return &CassandraStore{cassSession: session, bucket: bucket}
}

// SaveMessage inserts msg into both messages_by_room and messages_by_id via a
// single UnloggedBatch so the two denormalized writes share one coordinator
// round-trip. UnloggedBatch (not LoggedBatch) because we don't need batch-log
// atomicity: each INSERT is idempotent on its primary key, and on partial
// failure JetStream redelivers and both INSERTs re-run safely.
func (s *CassandraStore) SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	b := s.bucket.Of(msg.CreatedAt)
	mentions := toMentionSet(msg.Mentions)

	batch := s.cassSession.NewBatch(gocql.UnloggedBatch).WithContext(ctx)
	batch.Query(
		`INSERT INTO messages_by_room
		   (room_id, bucket, created_at, message_id, sender, msg, site_id, updated_at,
		    mentions, type, sys_msg_data, tshow, quoted_parent_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, b, msg.CreatedAt, msg.ID, sender, msg.Content, siteID, msg.CreatedAt,
		mentions, msg.Type, msg.SysMsgData, msg.TShow, msg.QuotedParentMessage,
	)
	batch.Query(
		`INSERT INTO messages_by_id
		   (message_id, created_at, room_id, sender, msg, site_id, updated_at,
		    mentions, type, sys_msg_data, tshow, quoted_parent_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, msg.RoomID, sender, msg.Content, siteID, msg.CreatedAt,
		mentions, msg.Type, msg.SysMsgData, msg.TShow, msg.QuotedParentMessage,
	)
	if err := s.cassSession.ExecuteBatch(batch); err != nil {
		return fmt.Errorf("save message %s: %w", msg.ID, err)
	}
	return nil
}

// SaveThreadMessage persists the thread reply to messages_by_id and
// thread_messages_by_thread, then increments tcount on the parent message.
//
// messages_by_id uses IF NOT EXISTS (LWT) to detect JetStream redeliveries.
// thread_messages_by_thread is always written (idempotent regular INSERT) so
// that a crash between the two writes does not leave thread_messages_by_thread
// permanently missing. tcount is only incremented when messages_by_id is newly
// written (applied=true); on redelivery the current tcount is read back so the
// handler can re-publish the badge event if the previous NATS publish failed.
// LWT statements cannot be combined with non-LWT statements in a Cassandra batch.
func (s *CassandraStore) SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string, threadRoomID string) (*int, error) {
	mentions := toMentionSet(msg.Mentions)

	// MapScanCAS is required: on conflict (applied=false) gocql returns the full
	// existing row which must be consumed into a destination — ScanCAS with no
	// args fails with "not enough columns to scan into". The map is never read
	// because the only information needed is the applied bool.
	m := make(map[string]interface{})
	applied, err := s.cassSession.Query(
		`INSERT INTO messages_by_id
		 (message_id, created_at, room_id, sender, msg, site_id, updated_at, mentions,
		  thread_room_id, thread_parent_id, thread_parent_created_at, type, sys_msg_data, tshow, quoted_parent_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`,
		msg.ID, msg.CreatedAt, msg.RoomID, sender, msg.Content, siteID, msg.CreatedAt, mentions,
		threadRoomID, msg.ThreadParentMessageID, msg.ThreadParentMessageCreatedAt, msg.Type, msg.SysMsgData, msg.TShow, msg.QuotedParentMessage,
	).WithContext(ctx).MapScanCAS(m)
	if err != nil {
		return nil, fmt.Errorf("save thread message %s: %w", msg.ID, err)
	}

	// Always write thread_messages_by_thread. The regular INSERT is idempotent on
	// its primary key, so re-running it on a redelivery (when !applied) completes
	// any partial work from a prior attempt that crashed between the two writes.
	if err := s.cassSession.Query(
		`INSERT INTO thread_messages_by_thread
		 (thread_room_id, created_at, message_id, room_id, thread_parent_id, sender, msg,
		  site_id, updated_at, mentions, type, sys_msg_data, quoted_parent_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		threadRoomID, msg.CreatedAt, msg.ID, msg.RoomID, msg.ThreadParentMessageID,
		sender, msg.Content, siteID, msg.CreatedAt, mentions,
		msg.Type, msg.SysMsgData, msg.QuotedParentMessage,
	).WithContext(ctx).Exec(); err != nil {
		return nil, fmt.Errorf("save thread message %s: %w", msg.ID, err)
	}

	if !applied {
		// Redelivery: messages_by_id row already exists from a prior attempt.
		// We skip incrementParentTcount to avoid double-counting. Known limitation:
		// if the prior attempt crashed after inserting messages_by_id but before
		// completing incrementParentTcount, the tcount was never incremented and
		// will be one too low until the next reply arrives (inherent in this
		// non-atomic three-step sequence without a separate idempotency token).
		// Read the current tcount so the handler can re-publish the badge event
		// when it was the NATS publish — not the DB write — that failed.
		return s.readParentTcount(ctx, msg)
	}

	return s.incrementParentTcount(ctx, msg)
}

// readParentTcount reads the current tcount from the parent message row in
// messages_by_id. Used on redelivery to recover the badge count without
// re-incrementing. Returns nil when ThreadParentMessageCreatedAt is absent or
// the parent row is not found.
func (s *CassandraStore) readParentTcount(ctx context.Context, msg *model.Message) (*int, error) {
	if msg.ThreadParentMessageCreatedAt == nil {
		return nil, nil
	}
	var tcount *int
	err := s.cassSession.Query(
		`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msg.ThreadParentMessageID, *msg.ThreadParentMessageCreatedAt,
	).WithContext(ctx).Scan(&tcount)
	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tcount for parent %s: %w", msg.ThreadParentMessageID, err)
	}
	return tcount, nil
}

// casMaxRetries is the maximum number of CAS attempts per tcount increment.
// A conflict means another thread-reply landed between our read and write;
// 16 attempts is sufficient for any realistic burst while preventing an
// infinite loop if something unexpected keeps the row locked.
const casMaxRetries = 16

// casIncrement atomically increments the nullable INT counter starting at
// initial by calling update(newVal, expected) in a retry loop. On conflict
// (applied==false) it retries with the value returned by update. Returns the
// successfully applied new value, or an error after maxRetries consecutive failures.
func casIncrement(maxRetries int, initial *int, update func(newVal int, expected *int) (applied bool, current *int, err error)) (int, error) {
	tcount := initial
	for range maxRetries {
		newVal := 1
		if tcount != nil {
			newVal = *tcount + 1
		}
		applied, current, err := update(newVal, tcount)
		if err != nil {
			return 0, err
		}
		if applied {
			return newVal, nil
		}
		tcount = current
	}
	return 0, fmt.Errorf("cas increment exceeded %d retries", maxRetries)
}

// incrementParentTcount increments tcount on the parent message row in both
// messages_by_id and messages_by_room using Cassandra Lightweight Transactions
// (IF tcount = ?). Each table is incremented independently via casIncrement,
// which retries up to casMaxRetries times on CAS conflict.
// Binding a nil *int as the IF condition evaluates to IF tcount = null, which
// handles the initial case where tcount has never been set on the parent row.
// If ThreadParentMessageCreatedAt is nil the increment is silently skipped —
// tcount cannot be updated without the full primary key of the parent row.
// Returns the authoritative post-CAS tcount from messages_by_id, or nil when
// the increment was skipped (ThreadParentMessageCreatedAt absent or row not found).
func (s *CassandraStore) incrementParentTcount(ctx context.Context, msg *model.Message) (*int, error) {
	if msg.ThreadParentMessageCreatedAt == nil {
		return nil, nil
	}
	parentID := msg.ThreadParentMessageID
	parentCreatedAt := *msg.ThreadParentMessageCreatedAt
	parentBucket := s.bucket.Of(parentCreatedAt)

	// CAS increment on messages_by_id (no bucket — table unchanged).
	var tcount *int
	if err := s.cassSession.Query(
		`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		parentID, parentCreatedAt,
	).WithContext(ctx).Scan(&tcount); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tcount for parent message %s: %w", parentID, err)
	}
	newTcount, err := casIncrement(casMaxRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
		var current *int
		applied, err := s.cassSession.Query(
			`UPDATE messages_by_id SET tcount = ? WHERE message_id = ? AND created_at = ? IF tcount = ?`,
			newVal, parentID, parentCreatedAt, expected,
		).WithContext(ctx).ScanCAS(&current)
		return applied, current, err
	})
	if err != nil {
		return nil, fmt.Errorf("cas tcount in messages_by_id for parent %s: %w", parentID, err)
	}

	if err := s.cassSession.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`,
		msg.RoomID, parentBucket, parentCreatedAt, parentID,
	).WithContext(ctx).Scan(&tcount); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			// messages_by_id was updated but messages_by_room has no row at this
			// (room_id, bucket, created_at, message_id) coordinate. This indicates
			// a data inconsistency — history-service reads tcount from messages_by_room,
			// so clients will see a stale thread count for this parent message.
			slog.Error("tcount divergence: parent row not found in messages_by_room after messages_by_id update",
				"parentMessageID", parentID,
				"roomID", msg.RoomID,
				"bucket", parentBucket,
			)
			return &newTcount, nil
		}
		return nil, fmt.Errorf("read tcount in messages_by_room for parent %s: %w", parentID, err)
	}
	if _, err := casIncrement(casMaxRetries, tcount, func(newVal int, expected *int) (bool, *int, error) {
		var current *int
		applied, err := s.cassSession.Query(
			`UPDATE messages_by_room SET tcount = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ? IF tcount = ?`,
			newVal, msg.RoomID, parentBucket, parentCreatedAt, parentID, expected,
		).WithContext(ctx).ScanCAS(&current)
		return applied, current, err
	}); err != nil {
		return nil, fmt.Errorf("cas tcount in messages_by_room for parent %s: %w", parentID, err)
	}

	return &newTcount, nil
}

// IF EXISTS prevents phantom rows on missing parents; misses log at ERROR
// because a silent miss permanently breaks thread reads for that parent.
func (s *CassandraStore) UpdateParentMessageThreadRoomID(ctx context.Context, parentMessageID, roomID string, parentCreatedAt time.Time, threadRoomID string) error {
	parentBucket := s.bucket.Of(parentCreatedAt)

	applied, err := s.cassSession.Query(
		`UPDATE messages_by_id SET thread_room_id = ? WHERE message_id = ? AND created_at = ? IF EXISTS`,
		threadRoomID, parentMessageID, parentCreatedAt,
	).WithContext(ctx).ScanCAS()
	if err != nil {
		return fmt.Errorf("set thread_room_id on parent %s in messages_by_id: %w", parentMessageID, err)
	}
	if !applied {
		slog.Error("thread_room_id stamp on messages_by_id missed: parent row not found at the given (message_id, created_at) coordinates",
			"request_id", natsutil.RequestIDFromContext(ctx),
			"messageID", parentMessageID,
			"parentCreatedAt", parentCreatedAt,
			"threadRoomID", threadRoomID,
		)
	}

	applied, err = s.cassSession.Query(
		`UPDATE messages_by_room SET thread_room_id = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ? IF EXISTS`,
		threadRoomID, roomID, parentBucket, parentCreatedAt, parentMessageID,
	).WithContext(ctx).ScanCAS()
	if err != nil {
		return fmt.Errorf("set thread_room_id on parent %s in messages_by_room: %w", parentMessageID, err)
	}
	if !applied {
		slog.Error("thread_room_id stamp on messages_by_room missed: parent row not found at the given (room_id, bucket, created_at, message_id) coordinates",
			"request_id", natsutil.RequestIDFromContext(ctx),
			"messageID", parentMessageID,
			"roomID", roomID,
			"bucket", parentBucket,
			"parentCreatedAt", parentCreatedAt,
			"threadRoomID", threadRoomID,
		)
	}
	return nil
}

// GetMessageSender reads the sender UDT from messages_by_id for the given message ID.
// Returns an error if the message does not exist.
func (s *CassandraStore) GetMessageSender(ctx context.Context, messageID string) (*cassParticipant, error) {
	var sender cassParticipant
	if err := s.cassSession.Query(
		`SELECT sender FROM messages_by_id WHERE message_id = ? LIMIT 1`,
		messageID,
	).WithContext(ctx).Scan(&sender); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("get sender for message %s: %w", messageID, errMessageNotFound)
		}
		return nil, fmt.Errorf("get sender for message %s: %w", messageID, err)
	}
	return &sender, nil
}
