package service

import (
	"fmt"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// maxBatchMessageIDs caps a single batch GetMessagesByIDs request. The decrypting
// IN-clause read fans out per partition in Cassandra, so an unbounded list would
// be a denial-of-service vector; 200 comfortably covers a page of search hits.
const maxBatchMessageIDs = 200

// GetMessagesByIDs fetches a batch of messages by ID, spanning arbitrary rooms,
// and enforces the SAME per-message access the single GetMessageByID path does:
// per-room subscription (getAccessSince) + the access-window floor + quote
// redaction. Messages the caller cannot see (not subscribed to the room, or
// created before the room's access window) are dropped rather than erroring;
// missing IDs are already omitted by the repository. This is the server side of
// the Approach-B encrypted-search content fetch.
func (s *HistoryService) GetMessagesByIDs(c *natsrouter.Context, req models.GetMessagesByIDsRequest) (*models.GetMessagesByIDsResponse, error) {
	account := c.Param("account")
	c.WithLogValues("account", account, "message_id_count", len(req.MessageIDs))

	if len(req.MessageIDs) == 0 {
		return nil, errcode.BadRequest("messageIds must not be empty")
	}
	if len(req.MessageIDs) > maxBatchMessageIDs {
		return nil, errcode.BadRequest("too many messageIds requested")
	}

	msgs, err := s.msgReader.GetMessagesByIDs(c, req.MessageIDs)
	if err != nil {
		return nil, fmt.Errorf("batch fetching messages: %w", err)
	}

	// Cache the per-room access decision so a batch of N messages from one room
	// triggers exactly one subscription lookup. A room maps to a (accessSince,
	// allowed) pair; allowed=false means the caller is not subscribed (drop).
	type roomAccess struct {
		accessSince *time.Time
		allowed     bool
	}
	accessByRoom := make(map[string]roomAccess, len(msgs))

	out := make([]models.Message, 0, len(msgs))
	for i := range msgs {
		msg := msgs[i]
		access, seen := accessByRoom[msg.RoomID]
		if !seen {
			accessSince, accErr := s.getAccessSince(c, account, msg.RoomID)
			if accErr != nil {
				// Not-subscribed is an expected per-room drop in a cross-room
				// batch — getAccessSince tags it with MessageNotSubscribed.
				// Any other error (infra/subscription store failure) fails the
				// whole request.
				if errcode.HasReason(accErr, errcode.MessageNotSubscribed) {
					access = roomAccess{allowed: false}
				} else {
					return nil, accErr
				}
			} else {
				access = roomAccess{accessSince: accessSince, allowed: true}
			}
			accessByRoom[msg.RoomID] = access
		}
		if !access.allowed {
			continue
		}
		// Access-window floor: identical to GetMessageByID's check.
		if access.accessSince != nil && msg.CreatedAt.Before(*access.accessSince) {
			continue
		}
		redactUnavailableQuote(&msg, access.accessSince)
		out = append(out, msg)
	}

	return &models.GetMessagesByIDsResponse{Messages: out}, nil
}
