package main

import (
	"context"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

// cassParticipant maps to the Cassandra "Participant" UDT.
type cassParticipant struct {
	ID          string
	EngName     string
	CompanyName string // ChineseName
	Account     string
	AppID       string
	AppName     string
	IsBot       bool
}

// MarshalUDT implements gocql.UDTMarshaler for cassParticipant.
func (p *cassParticipant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, p.ID)
	case "eng_name":
		return gocql.Marshal(info, p.EngName)
	case "company_name":
		return gocql.Marshal(info, p.CompanyName)
	case "account":
		return gocql.Marshal(info, p.Account)
	case "app_id":
		return gocql.Marshal(info, p.AppID)
	case "app_name":
		return gocql.Marshal(info, p.AppName)
	case "is_bot":
		return gocql.Marshal(info, p.IsBot)
	default:
		return nil, nil
	}
}

// CassandraStore implements Store using a Cassandra session.
type CassandraStore struct {
	cassSession *gocql.Session
}

func NewCassandraStore(session *gocql.Session) *CassandraStore {
	return &CassandraStore{cassSession: session}
}

// SaveMessage inserts msg into both messages_by_room and messages_by_id.
// Full implementation added in Task 5.
func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message, sender cassParticipant, siteID string) error {
	return nil
}
