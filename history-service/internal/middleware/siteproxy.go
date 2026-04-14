package middleware

import (
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsrouter"
)

// forwardedSiteHeader marks a request as already forwarded by SiteProxy.
const forwardedSiteHeader = "X-Forwarded-Site"

// siteProxyTimeout is the deadline for forwarded cross-site NATS requests.
const siteProxyTimeout = 10 * time.Second

// SiteProxy returns middleware that transparently forwards requests to remote sites.
// It reads the "siteID" parameter from the subject. Requests targeting the local site
// (or already forwarded from another site) proceed to the next handler. Requests
// targeting a remote site are forwarded via NATS request/reply and the response is
// relayed back to the caller.
//
// The middleware sets an X-Forwarded-Site header on forwarded messages to prevent
// infinite loops when both sites subscribe to the same wildcard pattern.
func SiteProxy(localSiteID string, nc *nats.Conn) natsrouter.HandlerFunc {
	return func(c *natsrouter.Context) {
		// Already forwarded from another site — handle locally.
		if c.Msg != nil && c.Msg.Header != nil && c.Msg.Header.Get(forwardedSiteHeader) != "" {
			c.Next()
			return
		}

		siteID := c.Param("siteID")
		if siteID == "" || siteID == localSiteID {
			c.Next()
			return
		}

		// Forward to the remote site's service via NATS request/reply.
		msg := nats.NewMsg(c.Msg.Subject)
		msg.Data = c.Msg.Data
		msg.Header = make(nats.Header)
		for k, v := range c.Msg.Header {
			msg.Header[k] = v
		}
		msg.Header.Set(forwardedSiteHeader, localSiteID)

		resp, err := nc.RequestMsg(msg, siteProxyTimeout)
		if err != nil {
			slog.Error("remote site request failed",
				"subject", c.Msg.Subject,
				"remoteSiteID", siteID,
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
