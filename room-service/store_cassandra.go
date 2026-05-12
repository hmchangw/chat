package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
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
