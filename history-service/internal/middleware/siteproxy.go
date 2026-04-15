package middleware

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// forwardedSiteHeader marks a request as already forwarded by SiteProxy.
const forwardedSiteHeader = "X-Forwarded-Site"

// siteProxyTimeout is the deadline for forwarded cross-site NATS requests.
const siteProxyTimeout = 10 * time.Second

// RoomFinder looks up a room by ID. Implemented by mongorepo.RoomRepo.
type RoomFinder interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
}

// SiteProxy returns middleware that transparently forwards requests to remote sites.
// It fetches the room from MongoDB using the "roomID" subject parameter. If the room's
// SiteID matches localSiteID, the request is handled locally. Otherwise it is forwarded
// to the remote site by replacing localSiteID with room.SiteID in the subject.
//
// Requests already forwarded from another site (X-Forwarded-Site header present) are
// handled locally to prevent infinite loops.
func SiteProxy(localSiteID string, nc *nats.Conn, rooms RoomFinder) natsrouter.HandlerFunc {
	return func(c *natsrouter.Context) {
		// Already forwarded from another site — handle locally.
		if c.Msg != nil && c.Msg.Header != nil && c.Msg.Header.Get(forwardedSiteHeader) != "" {
			c.Next()
			return
		}

		roomID := c.Param("roomID")

		room, err := rooms.GetRoom(c, roomID)
		if err != nil {
			slog.Error("room lookup failed",
				"roomID", roomID,
				"error", err,
			)
			c.ReplyError("internal error")
			c.Abort()
			return
		}
		if room == nil {
			c.ReplyError("room not found")
			c.Abort()
			return
		}

		if room.SiteID == localSiteID {
			c.Next()
			return
		}

		// Forward to the remote site's service via NATS request/reply.
		remoteSubject := strings.Replace(c.Msg.Subject, localSiteID, room.SiteID, 1)

		msg := nats.NewMsg(remoteSubject)
		msg.Data = c.Msg.Data
		msg.Header = make(nats.Header)
		for k, v := range c.Msg.Header {
			msg.Header[k] = v
		}
		msg.Header.Set(forwardedSiteHeader, localSiteID)

		resp, err := nc.RequestMsg(msg, siteProxyTimeout)
		if err != nil {
			slog.Error("remote site request failed",
				"subject", remoteSubject,
				"remoteSiteID", room.SiteID,
				"error", err,
			)
			c.ReplyError("remote site unavailable")
			c.Abort()
			return
		}

		if err := c.Msg.Respond(resp.Data); err != nil {
			slog.Error("relay response failed",
				"subject", c.Msg.Subject,
				"error", err,
			)
		}
		c.Abort()
	}
}
