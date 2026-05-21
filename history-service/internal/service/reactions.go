package service

import (
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/emoji"
	pkgmodel "github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/subject"
)

// ReactMessage handles chat.user.{account}.request.room.{roomID}.{siteID}.msg.react.
// Any room member may react; reactions on soft-deleted messages can only be
// removed (not added). Cassandra writes go through ToggleReaction which keeps
// the four mirror tables consistent. A best-effort canonical MessageEvent
// (Event=reacted) is published to MsgCanonicalReacted(siteID); broadcast and
// notification fan-out happens off that subject.
func (s *HistoryService) ReactMessage(c *natsrouter.Context, siteID string, req models.ReactMessageRequest) (*models.ReactMessageResponse, error) {
	account := c.Param("account")
	roomID := c.Param("roomID")

	if strings.TrimSpace(req.MessageID) == "" {
		return nil, natsrouter.ErrBadRequest("messageId is required")
	}
	if req.Shortcode == "" {
		return nil, natsrouter.ErrBadRequest("shortcode is required")
	}

	validator := emoji.NewValidator(s.customEmojis)
	if err := validator.Validate(c, siteID, req.Shortcode); err != nil {
		if errors.Is(err, emoji.ErrInvalidShortcode) || errors.Is(err, emoji.ErrUnknownShortcode) {
			return nil, natsrouter.ErrBadRequest("invalid reaction shortcode")
		}
		slog.Error("react: validate shortcode", "error", err, "shortcode", req.Shortcode)
		return nil, natsrouter.ErrInternal("failed to validate shortcode")
	}

	if _, err := s.getAccessSince(c, account, roomID); err != nil {
		return nil, err
	}

	msg, err := s.findMessage(c, roomID, req.MessageID)
	if err != nil {
		return nil, err
	}

	users, err := s.users.FindUsersByAccounts(c, []string{account})
	if err != nil {
		slog.Error("react: resolve actor", "error", err, "account", account)
		return nil, natsrouter.ErrInternal("failed to resolve actor")
	}
	if len(users) == 0 {
		slog.Error("react: actor not found", "account", account)
		return nil, natsrouter.ErrInternal("actor not found")
	}
	actor := users[0]

	// Block ADD on deleted messages; allow REMOVE. Membership-by-id check
	// determines which side of the toggle this is.
	alreadyReacted := participantInSet(msg.Reactions[req.Shortcode], actor.ID)
	if msg.Deleted && !alreadyReacted {
		return nil, natsrouter.ErrNotFound("message not found")
	}

	cassActor := models.Participant{
		ID:      actor.ID,
		Account: actor.Account,
		EngName: actor.EngName,
	}
	reactedAt := time.Now().UTC()

	action, err := s.msgWriter.ToggleReaction(c, msg, req.Shortcode, cassActor, reactedAt)
	if err != nil {
		slog.Error("react: toggle", "error", err, "messageID", req.MessageID, "shortcode", req.Shortcode)
		return nil, natsrouter.ErrInternal("failed to toggle reaction")
	}

	canonicalEvt := pkgmodel.MessageEvent{
		Event: pkgmodel.EventReacted,
		Message: pkgmodel.Message{
			ID:          msg.MessageID,
			RoomID:      msg.RoomID,
			UserID:      msg.Sender.ID,
			UserAccount: msg.Sender.Account,
			CreatedAt:   msg.CreatedAt,
			UpdatedAt:   &reactedAt,
		},
		SiteID:    siteID,
		Timestamp: reactedAt.UnixMilli(),
		ReactionDelta: &pkgmodel.ReactionDelta{
			Shortcode: req.Shortcode,
			Action:    action,
			Actor: pkgmodel.Participant{
				UserID:      actor.ID,
				Account:     actor.Account,
				SiteID:      actor.SiteID,
				ChineseName: actor.ChineseName,
				EngName:     actor.EngName,
			},
		},
	}
	s.publishCanonicalBestEffort(c, subject.MsgCanonicalReacted(siteID), &canonicalEvt, req.MessageID, roomID)

	return &models.ReactMessageResponse{
		MessageID: req.MessageID,
		Shortcode: req.Shortcode,
		Action:    action,
		ReactedAt: reactedAt.UnixMilli(),
	}, nil
}

// participantInSet reports whether any participant in the set has the given
// id. Used to decide add-vs-remove on a toggle without depending on the
// Cassandra UDT's struct-equality semantics (which would break if display
// fields drift across reactions).
func participantInSet(set []models.Participant, id string) bool {
	for i := range set {
		if set[i].ID == id {
			return true
		}
	}
	return false
}
