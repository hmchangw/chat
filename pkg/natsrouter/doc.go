// Package natsrouter provides Gin-style pattern-based routing for NATS request/reply endpoints.
//
// Subjects use {param} placeholders (like REST path params) instead of raw NATS wildcards.
// The router handles pattern-to-wildcard conversion, param extraction, JSON marshal/unmarshal,
// middleware, and error handling.
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
// # Context
//
// Every handler and middleware receives a *Context that implements context.Context.
// It can be passed directly to database calls and carries:
//   - Params extracted from the NATS subject
//   - A key-value store for middleware-to-handler data passing (e.g., auth user)
//   - The raw *nats.Msg for advanced use cases
//
// Handlers access params via c.Param("name") and middleware data via c.Get("key").
//
// # Registration Functions
//
// Three handler shapes:
//
//   - Register[Req, Resp]     — request body + response (request/reply)
//   - RegisterNoBody[Resp]    — no body, response only (GET-style)
//   - RegisterVoid[Req]       — request body, no response (fire-and-forget)
//
// All accept a Registrar (Router or Group) and panic on subscription failure.
//
// # Route Groups
//
// Groups reduce boilerplate by sharing a subject prefix and optional middleware:
//
//	msg := router.Group("chat.user.{userID}.request.room.{roomID}.site-1.msg")
//	natsrouter.Register(msg, "history", svc.LoadHistory)
//	natsrouter.Register(msg, "next", svc.LoadNextMessages)
//
// Group middleware runs after router middleware, before the handler:
//
//	admin := router.Group("admin.{userID}", adminAuthMiddleware)
//	natsrouter.Register(admin, "users.delete", svc.DeleteUser)
//
// # Middleware
//
// Middleware is a HandlerFunc that calls c.Next() to continue the chain:
//
//	func AuthMiddleware() natsrouter.HandlerFunc {
//	    return func(c *natsrouter.Context) {
//	        user, err := validateToken(c.Msg.Header.Get("Authorization"))
//	        if err != nil {
//	            c.ReplyError("unauthorized")
//	            c.Abort()
//	            return
//	        }
//	        c.Set("user", user)
//	        c.Next()
//	    }
//	}
//
// Built-in middleware:
//   - Recovery() — catches panics, logs them, replies with "internal error"
//   - Logging()  — logs subject and duration for each request
//
// # Error Handling
//
// Handlers return errors using Go's standard error interface:
//
//   - Return a *RouteError (via ErrNotFound, ErrForbidden, ErrBadRequest, etc.)
//     to send a user-facing error. The client receives the message and code.
//   - Return any other error for internal failures. It is logged and the client
//     receives "internal error".
//
// Standard error codes: CodeBadRequest, CodeNotFound, CodeForbidden, CodeConflict.
//
// # Pattern Routing
//
// Patterns use {name} placeholders that map to NATS single-token wildcards (*).
//
//	Pattern:  "chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history"
//	Wildcard: "chat.user.*.request.room.*.*.msg.history"
//	Params:   {userID: position 2, roomID: position 5, siteID: position 6}
package natsrouter
