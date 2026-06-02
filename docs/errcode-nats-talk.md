# A Unified Error Contract over NATS Request/Reply

### `pkg/errcode` — one envelope from handler to frontend

> Every service speaks the same contract. Three of them as a representative cross-section:
> **room-service** uses raw core request/reply — the wiring is explicit.
> **history-service** uses `pkg/natsrouter` — the wiring is handled by the framework.
> **auth-service** uses Gin/HTTP — the same error, a different boundary adapter.
> Whatever the transport, business logic returns the *same* typed error.

---

## The error response structure — every field

One shape on the wire, owned by `errcode.Error` (Go) — `cause` is unexported, so JSON **cannot** leak it:

```go
type Error struct {
	Code     Code              `json:"code"`               // ALWAYS present — e.g. "not_found"
	Reason   Reason            `json:"reason,omitempty"`   // optional domain code — e.g. "not_subscribed"
	Message  string            `json:"error"`              // user-safe text — e.g. "message not found"
	Metadata map[string]string `json:"metadata,omitempty"` // optional structured detail — e.g. {"max_size":"500"}
	cause    error             // UNEXPORTED → never serialized, server-log only — e.g. fmt.Errorf("get room: %w", mongo.ErrNoDocuments)
}
```

| Field | Wire key | Always? | What it's for |
|---|---|---|---|
| `Code` | `code` | ✅ yes | Closed 8-value set (`not_found`, `forbidden`, …). Drives HTTP status + UX category. |
| `Reason` | `reason` | optional | Open per-service domain code (`not_subscribed`). **The thing the frontend keys off.** |
| `Message` | `error` | ✅ yes | Human text. **Display only — never key off it** (wording changes). |
| `Metadata` | `metadata` | optional | Structured key/values (e.g. limits) when the UI needs them. |
| `cause` | — | never | The infra error. Logged once server-side by `Classify`; **never** on the wire. |

```jsonc
{ "error": "message not found",    "code": "not_found" }
{ "error": "not subscribed to room","code": "forbidden", "reason": "not_subscribed" }
{ "error": "internal error",        "code": "internal" }   // fmt.Errorf(...) collapsed; cause hidden
```

---

## Mental model

```text
Business logic returns  →  errcode.NotFound("...")        // typed, user-facing
                        →  fmt.Errorf("get room: %w", e)  // infra → collapses to "internal"

The boundary (errnats.Reply / router) does the rest: classify, log once, marshal envelope.
```

You almost only ever touch the **left column**. The boundary call is *one line, written once per handler file*.

---

## The eight constructors — the name is the HTTP/wire category

```go
errcode.BadRequest("...")        // 400   errcode.Conflict("...")          // 409
errcode.Unauthenticated("...")   // 401   errcode.TooManyRequests("...")   // 429
errcode.Forbidden("...")         // 403   errcode.Unavailable("...")       // 503
errcode.NotFound("...")          // 404   errcode.Internal("...")          // 500
```

Options (only when needed): `WithReason(...)` · `WithCause(infraErr)` · `WithMetadata(...)`

---

# Example A — room-service (raw core req/reply)

## A1 · Subscription and handler wiring

```go
// RegisterCRUD registers NATS request/reply handlers with a queue group.
func (h *Handler) RegisterCRUD(nc *otelnats.Conn) error {
	const queue = "room-service"
	if _, err := nc.QueueSubscribe(subject.MemberRoleUpdateWildcard(h.siteID), queue, h.natsUpdateRole); err != nil {
		return fmt.Errorf("subscribe member role update: %w", err)
	}
	// ...12 more subjects, same shape...
}

func (h *Handler) natsUpdateRole(m otelnats.Msg) {
	ctx, err := wrappedCtx(m)                 // ← validate request-id, seed log ctx
	if err != nil {
		errnats.Reply(ctx, m.Msg, err)        // ← THE boundary call
		return
	}
	resp, err := h.handleUpdateRole(ctx, m.Msg.Subject, m.Msg.Data)
	if err != nil {
		errnats.Reply(ctx, m.Msg, err)        // ← same one line on every error
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to update-role message", "error", err)
	}
}
```

`wrappedCtx` is the only service-specific helper — strict request-id (dedup-critical path):

```go
func wrappedCtx(m otelnats.Msg) (context.Context, error) {
	ctx, id, err := natsutil.RequireRequestID(m.Context(), m.Msg.Header, m.Msg.Subject)
	if err != nil {
		return m.Context(), err            // BadRequest → caller replies it
	}
	return errcode.WithLogValues(ctx, "request_id", id), nil
}
```

---

## A2 · Business logic returns typed errors

```go
func (h *Handler) handleUpdateRole(ctx context.Context, subj string, data []byte) ([]byte, error) {
	requester, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid subject: %s", subj)   // infra-ish → "internal", subject never leaks
	}
	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, errcode.BadRequest("invalid request")     // 400
	}
	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("get room: %w", err)           // DB down → "internal"
	}
	if room.Type != model.RoomTypeChannel {
		return nil, errRoomTypeGuard                          // sentinel (see A3)
	}
	if !hasRole(requesterSub.Roles, model.RoleOwner) {
		return nil, errOnlyOwners                             // 403 + reason
	}
	if req.NewRole == model.RoleOwner && hasRole(target.Subscription.Roles, model.RoleOwner) {
		return nil, errAlreadyOwner                           // 409 + reason
	}
	// ...happy path: publish to stream, return accepted...
}
```

No logging, no marshalling, no status codes. Just `return`.

---

## A3 · Sentinels — define repeated errors once (`room-service/helper.go`)

```go
var (
	errInvalidRole      = errcode.BadRequest("invalid role: must be owner or member")
	errOnlyOwners       = errcode.Forbidden("only owners can update roles",       errcode.WithReason(errcode.RoomNotOwner))
	errAlreadyOwner     = errcode.Conflict ("user is already an owner",           errcode.WithReason(errcode.RoomAlreadyOwner))
	errCannotDemoteLast = errcode.Conflict ("cannot demote the last owner",       errcode.WithReason(errcode.RoomCannotDemoteLastOwner))
	errRoomTypeGuard    = errcode.BadRequest("role update is only allowed in channel rooms",
	                                          errcode.WithReason(errcode.RoomNonChannelOperation))
)
```

Return the **singleton** at every site → `errors.Is(err, errOnlyOwners)` matches everywhere, and the frontend gets a stable `reason`.

---

# Example B — history-service (`pkg/natsrouter`)

## B1 · Typed handler registration — no wiring required

```go
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, siteID string) {
	natsrouter.Register(r, subject.MsgHistoryPattern(siteID),   s.LoadHistory)
	natsrouter.Register(r, subject.MsgGetPattern(siteID),       s.GetMessageByID)
	natsrouter.Register(r, subject.MsgThreadPattern(siteID),    s.GetThreadMessages)
	// ...
}
```

Handler signature is `func(c *natsrouter.Context, req T) (*R, error)`. The router unmarshals the body, **calls `errnats.Reply` for you on error**, and `ReplyJSON`s on success:

```go
// pkg/natsrouter/register.go — written ONCE, for every service
func Register[Req, Resp any](r *Router, pattern string, fn func(c *Context, req Req) (*Resp, error)) {
	handler := func(c *Context) {
		var req Req
		if err := json.Unmarshal(c.Msg.Data, &req); err != nil {
			replyErr(c, errcode.BadRequest("invalid request payload", errcode.WithCause(err)))
			return
		}
		resp, err := fn(c, req)
		if err != nil { replyErr(c, err); return }   // ← the boundary, automatic
		c.ReplyJSON(resp)
	}
	r.addRoute(pattern, []HandlerFunc{handler})
}
```

---

## B2 · A handler — identical return style to room-service

```go
func (s *HistoryService) GetMessageByID(c *natsrouter.Context, req models.GetMessageByIDRequest) (*models.Message, error) {
	account := c.Param("account")          // subject token, not a manual parse
	roomID := c.Param("roomID")
	c.WithLogValues("account", account, "room_id", roomID)

	accessSince, err := s.getAccessSince(c, account, roomID)   // c IS the context.Context
	if err != nil {
		return nil, err                                        // already typed — just bubble it
	}
	msg, err := s.findMessage(c, roomID, req.MessageID)
	if err != nil {
		return nil, err
	}
	if accessSince != nil && msg.CreatedAt.Before(*accessSince) {
		return nil, errcode.Forbidden("message is outside access window",
			errcode.WithReason(errcode.MessageOutsideAccessWindow))   // 403 + reason
	}
	return msg, nil
}
```

---

## B3 · Helpers — the full vocabulary in one place

```go
// 403 with a reason the frontend keys off
func (s *HistoryService) getAccessSince(ctx context.Context, account, roomID string) (*time.Time, error) {
	accessSince, subscribed, err := s.subscriptions.GetHistorySharedSince(ctx, account, roomID)
	if err != nil {
		return nil, fmt.Errorf("verifying room access for %s/%s: %w", account, roomID, err)  // infra → internal
	}
	if !subscribed {
		return nil, errcode.Forbidden("not subscribed to room", errcode.WithReason(errcode.MessageNotSubscribed))
	}
	return accessSince, nil
}

// 404 — distinct "not found" from "bad input"
func (s *HistoryService) findMessage(ctx context.Context, roomID, messageID string) (*models.Message, error) {
	if messageID == "" {
		return nil, errcode.BadRequest("messageId is required")     // 400
	}
	msg, err := s.msgReader.GetMessageByID(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("retrieving message %s: %w", messageID, err)  // infra → internal
	}
	if msg == nil || msg.RoomID != roomID {
		return nil, errcode.NotFound("message not found")          // 404
	}
	return msg, nil
}

// WithCause: keep the parse error server-side, generic message to the client
func parsePageRequest(cursor string, limit int) (cassrepo.PageRequest, error) {
	q, err := cassrepo.ParsePageRequest(cursor, limit)
	if err != nil {
		return cassrepo.PageRequest{}, errcode.BadRequest("invalid pagination cursor", errcode.WithCause(err))
	}
	return q, nil
}
```

---

# Example C — auth-service (Gin / HTTP)

The same `*errcode.Error` also drives the HTTP boundary — the only change is the adapter: `errhttp.Write` instead of `errnats.Reply`. The constructor name maps straight to the HTTP status.

## C1 · Routes and request-id middleware

```go
func registerRoutes(r *gin.Engine, h *AuthHandler) {
	r.POST("/auth",    h.HandleAuth)
	r.GET("/healthz",  h.HandleHealth)
}

// Same request-id primitive as the NATS path (idgen.ResolveRequestID),
// so one ID flows HTTP → NATS → logs.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := idgen.ResolveRequestID(c.GetHeader(natsutil.RequestIDHeader))
		c.Set("request_id", id)
		c.Request = c.Request.WithContext(natsutil.WithRequestID(c.Request.Context(), id))
		c.Header(natsutil.RequestIDHeader, id)
		c.Next()
	}
}
```

---

## C2 · The handler — same return style, `errhttp.Write` at the boundary

```go
func (h *AuthHandler) HandleAuth(c *gin.Context) {
	ctx := errcode.WithLogValues(c.Request.Context(), "request_id", c.GetString("request_id"))

	var req authRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errhttp.Write(ctx, c, errcode.BadRequest("ssoToken and natsPublicKey are required",
			errcode.WithReason(errcode.AuthMissingFields)))            // 400 + reason
		return
	}
	claims, err := h.validator.Validate(ctx, req.SSOToken)
	if err != nil {
		if errors.Is(err, pkgoidc.ErrTokenExpired) {
			errhttp.Write(ctx, c, errcode.Unauthenticated("SSO token has expired, please re-login",
				errcode.WithReason(errcode.AuthTokenExpired)))         // 401 + reason
			return
		}
		// WithCause keeps the real OIDC error server-side; client sees a generic message.
		errhttp.Write(ctx, c, errcode.Unauthenticated("invalid SSO token",
			errcode.WithReason(errcode.AuthInvalidToken), errcode.WithCause(err)))
		return
	}
	natsJWT, err := h.signNATSJWT(req.NATSPublicKey, claims.PreferredUsername)
	if err != nil {
		errhttp.Write(ctx, c, fmt.Errorf("signing NATS token: %w", err))   // infra → 500 "internal"
		return
	}
	c.JSON(http.StatusOK, authResponse{NATSJWT: natsJWT /* ... */})
}
```

`errhttp.Write` runs the *same* `Classify` as the NATS path — sets the HTTP status from `Code`, marshals the `{code, reason, error}` envelope, and logs the `cause` once. Frontend reads it with the identical `reason ?? code` contract.

---

## Same return value, any transport

Three services shown, but the pattern is service-wide — pick the row that matches your transport:

| | room-service (raw core) | history-service (router) | auth-service (Gin) |
|---|---|---|---|
| Register | `nc.QueueSubscribe(subj, queue, h.natsUpdateRole)` | `natsrouter.Register(r, pat, s.GetMessageByID)` | `r.POST("/auth", h.HandleAuth)` |
| Handler sig | `func(m otelnats.Msg)` | `func(c *Context, req T) (*R, error)` | `func(c *gin.Context)` |
| Request-id | `wrappedCtx` → `RequireRequestID` (strict) | `RequestID()` middleware (auto) | `requestIDMiddleware` (resolve) |
| Reply on error | **you** call `errnats.Reply(ctx, m.Msg, err)` | router calls it **for you** | **you** call `errhttp.Write(ctx, c, err)` |
| **Business logic** | `return errcode.NotFound("...")` | `return errcode.NotFound("...")` | `errcode.NotFound("...")` |

The thing you actually write — the `errcode.X(...)` value — is **identical** in every service. Only the adapter (`errnats.Reply` vs `errhttp.Write`) and the wiring differ.

---

## Same envelope, wrapped for async jobs

room-worker's two-phase result carries the **same `code`/`reason`/`error`** inside a job wrapper (`pkg/model.AsyncJobResult` → TS mirror):

```ts
export interface AsyncJobResultEnvelope {
  requestId: string
  operation: string
  status: 'ok' | 'error'
  roomId?: string
  error?: string    // ┐
  code?: string     // ├─ populated only when status === 'error' — same errcode fields
  reason?: string   // ┘
  timestamp: number
}
```

---

## Frontend — the `reason` is the cross-language API

The string you stamp on the server is the *same* string the client keys off. One contract, two languages:

| | Go — producer | TypeScript — consumer |
|---|---|---|
| Declare | `RoomNotOwner Reason = "not_room_owner"` | `REASON_COPY.not_room_owner` |
| Emit / read | `errcode.Forbidden(msg, WithReason(RoomNotOwner))` | `formatAsyncJobError(err)` |

The transport parses the envelope into one typed error — the client mirror of `errcode.Error`:

```ts
class AsyncJobError extends Error {
  code?: ErrorCode    // closed 8-value set: 'forbidden' | 'not_found' | …
  reason?: string     // open per-service:  'not_room_owner' | 'already_owner' | …
}
```

`reason` → friendly copy in **one** map — the only place user-facing English lives client-side:

```ts
const REASON_COPY: Record<string, string> = {
  not_room_owner:          'Only owners can do that.',
  already_owner:           'That user is already an owner.',
  last_owner_cannot_leave: "You're the last owner — promote someone else first.",
  not_subscribed:          'You need to join this room first.',
  // …one line per reason in the catalog
}
// reason → copy, else fall back to the server's message (never key off it)
formatAsyncJobError(err)  //  ≈  REASON_COPY[err.reason] ?? err.message
```

A component parses nothing — it catches, formats, and shows:

```jsx
try {
  await createRoom(nats, { name, users })
} catch (err) {
  setError(formatAsyncJobError(err))   // reason → friendly copy, automatic
}
```

> **Contract:** key off `reason ?? code`; only *display* `error`. `code: "internal"` is always `"internal error"` — the real cause never leaves the server.

---

## Recap — what you write day to day

1. **`return errcode.<Category>("msg")`** — pick the name that matches the status.
2. Add **`WithReason(...)`** only when the frontend must key off the case.
3. Infra failure → **`return fmt.Errorf("doing X: %w", err)`** (becomes `internal`).
4. The **one** boundary line (`errnats.Reply`) is the router's job, or copied once per raw handler file.
5. Repeated errors → a **sentinel** in `helper.go` so `errors.Is` + `reason` stay consistent.

> Everything else in the package (`Classify`, `Permanent`, `Parse`, `Marshal`) is boundary/worker/cross-site machinery you rarely touch.
