// Package errcode is the single source of client-facing error envelopes for
// every transport (NATS request/reply, JetStream replies, Gin HTTP).
//
// # Wire envelope
//
//		{"error":"<message>","code":"<category>","reason":"<specific>"?,"metadata":{…}?}
//
//	  - error    — human-readable, user-safe message.
//	  - code     — one Code (bad_request, unauthenticated, forbidden,
//	    not_found, conflict, too_many_requests, unavailable, internal). Always present.
//	  - reason   — optional Reason (domain code, e.g. "max_room_size_reached"),
//	    declared in codes_<service>.go. Frontend logic: trigger = reason ?? code.
//	  - metadata — optional map[string]string for structured detail.
//
// # Two types, by design
//
// Code and Reason are distinct types so the compiler rejects
// New(SomeReason, …) and WithReason(SomeCode).
//
// # Leak guarantee
//
// Error.cause is unexported; encoding/json cannot serialize it. The cause is
// reachable only server-side via Unwrap()/errors.Is/As and is logged exactly
// once by Classify.
//
// # Wrapping invariant: at most one *errcode.Error per chain
//
// Allowed:
//
//	return errcode.BadRequest("name is required")
//	return errcode.NotFound("x", errcode.WithReason(RoomNotMember))
//	return errcode.Internal("x", errcode.WithCause(rawDBErr)) // RAW cause only
//	return fmt.Errorf("checking room: %w", typedErr)          // typed survives
//	return typedErr
//
// Forbidden (WithCause panics; semgrep-flagged):
//
//	return errcode.Internal("x", errcode.WithCause(anotherErrcodeErr))
//
// Also forbidden (defeats the invariant; semgrep-flagged): two errcode errors
// in one chain via multi-verb fmt.Errorf — Classify picks the first.
//
//	return fmt.Errorf("%w and %w", errcodeA, errcodeB)
//
// Propagate with a single %w only.
//
// # Logging
//
// Classify logs each error exactly once at a category-aware level (server
// faults ERROR, expected client errors INFO). Handlers must NOT log-then-reply.
//
// Attach domain context once at handler entry:
//   - natsrouter handler (has *Context):  c.WithLogValues("account", a)
//   - Gin / raw NATS (has context.Context): ctx = errcode.WithLogValues(ctx, …)
//
// Never call the package func errcode.WithLogValues with a *natsrouter.Context
// as parent — use the method, which derives from the inner ctx and avoids the
// Value-delegation cycle.
//
// Trust boundary: WithLogValues attributes are SERVER-ONLY (never serialized);
// WithMetadata is CLIENT-VISIBLE (ships in the envelope). Never wrap raw
// message bodies, tokens, or secrets into a cause — WithCause logs err.Error().
package errcode
