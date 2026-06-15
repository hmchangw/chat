package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/msgbucket"
)

type cassMessageReader struct {
	session *gocql.Session
}

func NewCassMessageReader(session *gocql.Session) *cassMessageReader {
	return &cassMessageReader{session: session}
}

const msgReadReceiptQuery = `SELECT room_id, created_at, sender.account FROM messages_by_id WHERE message_id = ? LIMIT 1`

func (r *cassMessageReader) GetMessageRoomAndCreatedAt(
	ctx context.Context,
	messageID string,
) (string, time.Time, string, bool, error) {
	var (
		roomID        string
		createdAt     time.Time
		senderAccount string
	)
	err := r.session.Query(msgReadReceiptQuery, messageID).
		WithContext(ctx).
		Scan(&roomID, &createdAt, &senderAccount)
	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return "", time.Time{}, "", false, nil
		}
		return "", time.Time{}, "", false, fmt.Errorf("get message %s: %w", messageID, err)
	}
	return roomID, createdAt, senderAccount, true, nil
}

// cassMeetMarkerReader reads the latest teams_meet_started system message for a
// room. messages_by_room is partitioned by (room_id, bucket) and clustered by
// created_at, so we walk buckets from "now" backwards (newest first) and, within
// each bucket, take the newest row whose type is teams_meet_started. The first
// match is the most recent meeting for the room. The walk is bounded by
// maxBuckets so a room that never had a meeting doesn't scan unboundedly.
//
// The query never names a keyspace — the gocql session is bound to
// CASSANDRA_KEYSPACE at connect time (cassutil), so this is keyspace-aware.
type cassMeetMarkerReader struct {
	session    *gocql.Session
	bucket     msgbucket.Sizer
	maxBuckets int
}

func NewCassMeetMarkerReader(session *gocql.Session, bucket msgbucket.Sizer, maxBuckets int) *cassMeetMarkerReader {
	return &cassMeetMarkerReader{session: session, bucket: bucket, maxBuckets: maxBuckets}
}

const meetMarkerQuery = `SELECT sys_msg_data FROM messages_by_room ` +
	`WHERE room_id = ? AND bucket = ? AND type = ? ORDER BY created_at DESC LIMIT 1 ALLOW FILTERING`

func (r *cassMeetMarkerReader) GetLastTeamsMeetStarted(
	ctx context.Context,
	roomID string,
) (*model.TeamsMeetStartedSysData, bool, error) {
	bucket := r.bucket.Of(time.Now().UTC())
	for i := 0; i < r.maxBuckets; i++ {
		var sysMsgData []byte
		err := r.session.Query(meetMarkerQuery, roomID, bucket, model.MessageTypeTeamsMeetStarted).
			WithContext(ctx).
			Scan(&sysMsgData)
		if err != nil {
			if errors.Is(err, gocql.ErrNotFound) {
				bucket = r.bucket.Prev(bucket)
				continue
			}
			return nil, false, fmt.Errorf("read teams_meet_started marker for room %s: %w", roomID, err)
		}
		var marker model.TeamsMeetStartedSysData
		if len(sysMsgData) > 0 {
			if err := json.Unmarshal(sysMsgData, &marker); err != nil {
				return nil, false, fmt.Errorf("decode teams_meet_started marker for room %s: %w", roomID, err)
			}
		}
		return &marker, true, nil
	}
	return nil, false, nil
}
