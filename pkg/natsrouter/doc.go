// Package natsrouter provides pattern-based routing for NATS request/reply endpoints.
//
// Subjects use {param} placeholders (like REST API path params) instead of raw
// NATS wildcards. The router handles pattern-to-wildcard conversion, param extraction,
// JSON unmarshal/reply, middleware, and error handling.
//
// # Quick Start
//
//	router := natsrouter.New(nc, "my-service")
//	router.Use(natsrouter.Recovery())
//	router.Use(natsrouter.Logging())
//
//	natsrouter.Register(router, "chat.user.{userID}.room.{roomID}.msg.send", svc.SendMessage)
//	natsrouter.RegisterNoBody(router, "chat.user.{userID}.rooms.get.{roomID}", svc.GetRoom)
//	natsrouter.RegisterVoid(router, "chat.user.{userID}.event.typing", svc.HandleTyping)
//
// # Registration Functions
//
// There are three registration functions, each for a different handler shape:
//
//   - Register[Req, Resp]     — takes request body, returns response (request/reply)
//   - RegisterNoBody[Resp]    — no request body, returns response (GET-style)
//   - RegisterVoid[Req]       — takes request body, no response (fire-and-forget)
//
// All three extract params from the subject and panic on subscription failure
// (startup-only, fatal if broken — same as http.HandleFunc).
//
// # Pattern Routing
//
// Patterns use {name} placeholders that map to NATS single-token wildcards (*).
// At registration time the pattern is parsed into a NATS wildcard subject for
// subscription and a param extraction map. At request time, params are extracted
// from the incoming subject by position.
//
//	Pattern:  "chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history"
//	Wildcard: "chat.user.*.request.room.*.*.msg.history"
//	Params:   {userID: position 2, roomID: position 5, siteID: position 6}
//
// # Error Handling
//
// Handlers return errors using Go's standard error interface. The router
// distinguishes between user-facing errors and internal errors:
//
//   - Return a *RouteError (via Err, Errf, or ErrWithCode) to send a user-facing
//     error response. The client receives the error message and optional code.
//   - Return any other error for internal failures. The error is logged with the
//     subject for debugging, and the client receives a generic "internal error".
//
// Example:
//
//	func (s *Svc) GetRoom(ctx context.Context, p natsrouter.Params, req GetReq) (*Room, error) {
//	    room, err := s.store.Find(ctx, req.ID)
//	    if err != nil {
//	        return nil, fmt.Errorf("finding room: %w", err) // internal → "internal error"
//	    }
//	    if room == nil {
//	        return nil, natsrouter.ErrWithCode("not_found", "room not found") // → sent to client
//	    }
//	    return room, nil
//	}
//
// # Middleware
//
// Middleware wraps NATS message handlers using the standard pattern:
//
//	type Middleware func(next nats.MsgHandler) nats.MsgHandler
//
// Middleware sees the raw *nats.Msg and can inspect/modify messages, reject
// requests before unmarshal, or wrap the reply. Use router.Use() to add
// middleware — it executes in the order added.
//
// Built-in middleware:
//   - Recovery() — catches panics, logs them, replies with "internal error"
//   - Logging()  — logs subject and duration for each request
package natsrouter
