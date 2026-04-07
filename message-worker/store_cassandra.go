package main

import (
	"context"
	"fmt"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

type CassandraStore struct {
	cassSession *gocql.Session
}

func NewCassandraStore(session *gocql.Session) *CassandraStore {
	return &CassandraStore{
		cassSession: session,
	}
}

func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message) error { //nolint:gocritic // value receiver per Store interface contract
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_room (
			room_id, created_at, message_id, sender, target_user,
			msg, mentions, attachments, file, card, card_action, tshow,
			thread_parent_created_at, visible_to, unread, reactions, deleted,
			sys_msg_type, sys_msg_data, federate_from, edited_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, msg.Sender, msg.TargetUser,
		msg.Content, msg.Mentions, msg.Attachments, msg.File, msg.Card,
		msg.CardAction, msg.TShow, msg.ThreadParentCreatedAt, msg.VisibleTo,
		msg.Unread, msg.Reactions, msg.Deleted, msg.SysMsgType, msg.SysMsgData,
		msg.FederateFrom, msg.EditedAt, msg.UpdatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert message %s: %w", msg.ID, err)
	}
	return nil
}
