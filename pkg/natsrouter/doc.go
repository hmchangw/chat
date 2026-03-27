// Package natsrouter provides pattern-based routing for NATS request/reply endpoints.
//
// Subjects use {param} placeholders (like REST API routes) instead of raw NATS wildcards.
// The router handles pattern-to-wildcard conversion, param extraction, JSON unmarshal/reply,
// middleware, and error sanitization.
//
// Example:
//
//	router := natsrouter.New(nc, "my-service")
//	router.Use(natsrouter.Recovery())
//	router.Use(natsrouter.Logging())
//
//	natsrouter.Register[MyRequest, MyResponse](
//	    router,
//	    "chat.user.{userID}.request.room.{roomID}.msg.history",
//	    func(ctx context.Context, p natsrouter.Params, req MyRequest) (*MyResponse, error) {
//	        userID := p.Get("userID")
//	        // handle request...
//	    },
//	)
package natsrouter
