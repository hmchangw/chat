package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/migration"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// sourceRoom is the subset of a rocketchat_rooms doc the mapper decodes (relaxed extended JSON).
type sourceRoom struct {
	ID        string    `bson:"_id"`
	T         string    `bson:"t"`
	Prid      string    `bson:"prid"`
	TeamID    string    `bson:"teamId"`
	Name      string    `bson:"name"`
	FName     string    `bson:"fname"`
	RO        bool      `bson:"ro"`
	UIDs      []string  `bson:"uids"`
	Usernames []string  `bson:"usernames"`
	UpdatedAt time.Time `bson:"_updatedAt"`
	TS        time.Time `bson:"ts"`
	// Federation.Origin is the room's home site (absent ⇒ local); drives siteId stamping.
	Federation struct {
		Origin string `bson:"origin"`
	} `bson:"federation"`
}

// updateDescription is the connector's update delta; only changed field keys matter, values are opaque.
type updateDescription struct {
	UpdatedFields map[string]any `bson:"updatedFields" json:"updatedFields"`
	RemovedFields []string       `bson:"removedFields" json:"removedFields"`
}

// participantCount returns the member count, preferring uids and falling back to usernames.
func (r *sourceRoom) participantCount() int {
	if len(r.UIDs) > 0 {
		return len(r.UIDs)
	}
	return len(r.Usernames)
}

// displayName returns the friendly display name (fname), falling back to the machine name.
func (r *sourceRoom) displayName() string {
	if r.FName != "" {
		return r.FName
	}
	return r.Name
}

// handleRoom maps a rocketchat_rooms change event to an inbox InboxEvent (§4.2 / §4.0).
// Returns migration.ErrSkipped for deletes, excluded room types, and update lookup misses.
//
//nolint:gocritic // ev passed by value to mirror handle's signature; off the hot path.
func (h *handler) handleRoom(ctx context.Context, ev oplogEvent) error {
	if ev.Op == "delete" {
		// The app has no room deletion and the delete event is un-actionable (only the source _id).
		slog.Debug("skip room delete (un-actionable, no app deletion)",
			"eventId", ev.EventID, "request_id", natsutil.RequestIDFromContext(ctx))
		h.metrics.onSkipped(ctx, "room_delete")
		return migration.ErrSkipped
	}

	doc, skip, err := h.resolveDoc(ctx, ev)
	if err != nil {
		return err
	}
	if skip {
		h.metrics.onSkipped(ctx, ev.Op+"_skip")
		return migration.ErrSkipped
	}

	var sr sourceRoom
	if uerr := bson.UnmarshalExtJSON(doc, false, &sr); uerr != nil {
		return fmt.Errorf("%w: decode source room: %v", migration.ErrPoison, uerr) //nolint:errorlint // intentional single-%w sentinel wrap; decode err is informational only
	}

	// hasBot is unresolvable here without a user lookup (botDM detection deferred — see §4.2 /
	// the design's botDM note); pass false so a 2-party bot DM classifies as a plain dm for now.
	class := classifyRoom(sr.T, sr.Prid != "", sr.TeamID != "", false, sr.participantCount())
	if class.Excluded {
		slog.Debug("skip excluded room type",
			"t", sr.T, "reason", class.Reason, "eventId", ev.EventID, "request_id", natsutil.RequestIDFromContext(ctx))
		h.metrics.onSkipped(ctx, class.Reason)
		return migration.ErrSkipped
	}

	// Zero-guard an absent source timestamp with now() so the room doc never carries a year-0001
	// UpdatedAt, keeping the UpsertRoom high-water-mark guard functional.
	nowMillis := h.nowMillis()
	updatedAt := sr.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.UnixMilli(nowMillis).UTC()
	}
	createdAt := sr.TS.UTC()
	if createdAt.IsZero() {
		createdAt = updatedAt
	}

	room := model.Room{
		ID:     sr.ID,
		Type:   class.Type,
		Name:   sr.displayName(),
		SiteID: siteIDFromOrigin(sr.Federation.Origin, h.siteID),
		// ExternalAccess source field is unconfirmed (SOURCE_DATA.md §3) — default false per design.
		ExternalAccess: false,
		Restricted:     sr.RO,
		UIDs:           sr.UIDs,
		Accounts:       sr.Usernames,
		UserCount:      sr.participantCount(),
		UpdatedAt:      updatedAt,
		CreatedAt:      createdAt,
	}

	evts, err := h.roomEvents(ev, &room)
	if err != nil {
		return err
	}
	for _, evt := range evts {
		if err := h.pub.Publish(ctx, evt); err != nil {
			return err
		}
	}
	return nil
}

// roomEvents builds the InboxEvents for a room change: always room_sync, preceded by
// room_renamed (name/fname changed) or room_restricted (ro changed) on the matching update.
//
//nolint:gocritic // ev passed by value to mirror handle's signature; off the hot path.
func (h *handler) roomEvents(ev oplogEvent, room *model.Room) ([]model.InboxEvent, error) {
	if ev.Op != "update" {
		return []model.InboxEvent{h.roomSyncEvent(room)}, nil
	}

	var desc updateDescription
	if len(ev.UpdateDescription) > 0 {
		if err := bson.UnmarshalExtJSON(ev.UpdateDescription, false, &desc); err != nil {
			return nil, fmt.Errorf("%w: decode room updateDescription: %v", migration.ErrPoison, err) //nolint:errorlint // intentional single-%w sentinel wrap; decode err is informational only
		}
	}

	switch {
	case changed(desc, "name") || changed(desc, "fname"):
		return []model.InboxEvent{h.roomRenamedEvent(room), h.roomSyncEvent(room)}, nil
	case changed(desc, "ro"):
		return []model.InboxEvent{h.roomRestrictedEvent(room), h.roomSyncEvent(room)}, nil
	default:
		return []model.InboxEvent{h.roomSyncEvent(room)}, nil
	}
}

// changed reports whether the named field appears in the update delta (set or removed).
func changed(desc updateDescription, field string) bool {
	if _, ok := desc.UpdatedFields[field]; ok {
		return true
	}
	for _, rf := range desc.RemovedFields {
		if rf == field {
			return true
		}
	}
	return false
}

func (h *handler) roomSyncEvent(room *model.Room) model.InboxEvent {
	return h.inboxEvent(model.InboxEventType("room_sync"), room.SiteID, mustMarshal(room))
}

func (h *handler) roomRenamedEvent(room *model.Room) model.InboxEvent {
	// Use the source _updatedAt millis (zero-guarded in handleRoom) as the nameUpdatedAt high-water
	// mark so UpdateSubscriptionNamesForRoom matches the companion room_sync guard.
	return h.inboxEvent(model.InboxRoomRenamed, room.SiteID, mustMarshal(model.RoomRenamedInboxPayload{
		RoomID:    room.ID,
		NewName:   room.Name,
		Timestamp: room.UpdatedAt.UnixMilli(),
	}))
}

func (h *handler) roomRestrictedEvent(room *model.Room) model.InboxEvent {
	// Use the source _updatedAt millis (zero-guarded in handleRoom) as the visibilityUpdatedAt
	// high-water mark so ApplySubscriptionVisibility matches the companion room_sync guard.
	return h.inboxEvent(model.InboxRoomRestricted, room.SiteID, mustMarshal(model.RoomRestrictedInboxPayload{
		RoomID:         room.ID,
		Restricted:     room.Restricted,
		ExternalAccess: room.ExternalAccess,
		OwnerAccount:   "",
		Timestamp:      room.UpdatedAt.UnixMilli(),
	}))
}

// inboxEvent wraps an inner payload in the local-INBOX InboxEvent envelope. SiteID is the
// record's home site; DestSiteID is this deployment (the local inbox-worker applies it).
func (h *handler) inboxEvent(t model.InboxEventType, siteID string, payload []byte) model.InboxEvent {
	return model.InboxEvent{
		Type:       t,
		SiteID:     siteID,
		DestSiteID: h.siteID,
		Payload:    payload,
		Timestamp:  h.nowMillis(),
	}
}
