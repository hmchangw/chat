package main

import (
	"context"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/migration"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// sourceUser is the subset of a users doc the mapper consumes, decoded from relaxed extended JSON.
type sourceUser struct {
	ID           string   `bson:"_id"`
	Username     string   `bson:"username"`
	Type         string   `bson:"type"`
	StatusText   string   `bson:"statusText"`
	Roles        []string `bson:"roles"`
	CustomFields struct {
		EngName  string `bson:"engName"`
		TsmcName string `bson:"tsmcName"`
		DeptID   string `bson:"deptId"`
		DeptName string `bson:"deptName"`
		SectID   string `bson:"sectId"`
		SectName string `bson:"sectName"`
	} `bson:"customFields"`
	// Federation.Origin is the user's home site (absent ⇒ local); drives siteId stamping.
	Federation struct {
		Origin string `bson:"origin"`
	} `bson:"federation"`
}

// handleUser maps a users change event to an insert-if-absent direct write to the per-site
// users collection per spec §4.1. delete is skipped (deactivation is active:false, deferred).
//
//nolint:gocritic // ev passed by value to mirror handle's signature; off the hot path.
func (h *handler) handleUser(ctx context.Context, ev oplogEvent) error {
	if ev.Op == "delete" {
		// Deactivation is active:false (an update), not a row delete; the delete event is
		// un-actionable anyway (only the source _id). Deferred — skip + metric.
		slog.Debug("skip user delete (deactivation is active:false, deferred)",
			"eventId", ev.EventID, "request_id", natsutil.RequestIDFromContext(ctx))
		h.metrics.onSkipped(ctx, "user_delete")
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

	var su sourceUser
	if uerr := bson.UnmarshalExtJSON(doc, false, &su); uerr != nil {
		return fmt.Errorf("%w: decode source user: %v", migration.ErrPoison, uerr) //nolint:errorlint // intentional single-%w sentinel wrap; decode err is informational only
	}

	u := model.User{
		ID:          idgen.GenerateUUIDv7(),
		Account:     su.Username,
		EngName:     su.CustomFields.EngName,
		ChineseName: su.CustomFields.TsmcName,
		SectID:      su.CustomFields.SectID,
		SectName:    su.CustomFields.SectName,
		DeptID:      su.CustomFields.DeptID,
		DeptName:    su.CustomFields.DeptName,
		StatusText:  su.StatusText,
		Roles:       mapUserRoles(su.Roles),
		SiteID:      siteIDFromOrigin(su.Federation.Origin, h.siteID),
	}

	inserted, err := h.target.UpsertUserIfAbsent(ctx, u)
	if err != nil {
		return fmt.Errorf("upsert user if absent (account %q): %w", u.Account, err)
	}
	if inserted {
		h.metrics.onUserSeed(ctx, "insert")
	} else {
		h.metrics.onUserSeed(ctx, "present")
	}

	// A live statusText change must reach every site (global-visibility, not home-routed). Fan to all
	// sites incl. ours — the migration has no synchronous local writer, so we learn via our own inbox.
	if ev.Op == "update" {
		var desc updateDescription
		if len(ev.UpdateDescription) > 0 {
			if derr := bson.UnmarshalExtJSON(ev.UpdateDescription, false, &desc); derr != nil {
				return fmt.Errorf("%w: decode user updateDescription: %v", migration.ErrPoison, derr) //nolint:errorlint // intentional single-%w sentinel wrap; decode err is informational only
			}
		}
		if changed(desc, "statusText") {
			if err := h.publishUserStatus(ctx, su.Username, su.StatusText); err != nil {
				return err
			}
		}
	}

	return nil
}

// publishUserStatus fans a user_status_updated InboxEvent to every site incl. our own (statusIsShow
// stays nil — owned by the company-wide sync). A publish failure Naks the whole event; re-fan is idempotent.
func (h *handler) publishUserStatus(ctx context.Context, account, statusText string) error {
	now := h.nowMillis()
	payload := mustMarshal(model.UserStatusUpdated{
		Account:    account,
		StatusText: statusText,
		Timestamp:  now,
	})
	sent := 0
	for _, dest := range h.allSiteIDs {
		if dest == "" {
			continue
		}
		evt := model.InboxEvent{
			Type:       model.InboxUserStatusUpdated,
			SiteID:     h.siteID,
			DestSiteID: dest,
			Payload:    payload,
			Timestamp:  now,
		}
		if err := h.pub.Publish(ctx, evt); err != nil {
			return fmt.Errorf("publish user_status_updated to %q: %w", dest, err)
		}
		sent++
	}
	if sent == 0 {
		// No destination sites: a statusText change would be lost. Surface a (transient) error
		// so the event Naks and is visible rather than silently dropped — ALL_SITE_IDS is empty
		// or misconfigured. (Future: hard-fail at startup instead of per-event.)
		return fmt.Errorf("no destination sites for user_status_updated (account %q): ALL_SITE_IDS empty", account)
	}
	return nil
}

// mapUserRoles maps source role strings to model.UserRole: "admin" → UserRoleAdmin, all else
// → UserRoleUser. Returns nil for no roles (an empty Roles reads as ["user"]).
func mapUserRoles(roles []string) []model.UserRole {
	if len(roles) == 0 {
		return nil
	}
	out := make([]model.UserRole, 0, len(roles))
	for _, r := range roles {
		if r == string(model.UserRoleAdmin) {
			out = append(out, model.UserRoleAdmin)
		} else {
			out = append(out, model.UserRoleUser)
		}
	}
	return out
}
