# Add-Member Cross-Site Channel Sources — Design

**Date:** 2026-04-20
**Status:** Draft
**Service:** `room-service`
**Related specs:**
- `2026-04-14-add-member-design.md` (base add-member feature)
- `2026-04-20-room-members-list-design.md` (member.list endpoint this reuses)

## Summary

The current add-member flow accepts `Channels []string` (room IDs) as a source of members to copy into the target room. Expansion is done entirely against the local MongoDB via `GetRoomMembersByRooms` / `GetAccountsByRooms`, so source channels that live on a **different site** silently return zero members.

This spec changes the shape of channel refs to carry their home site and uses the existing `member.list` NATS endpoint to fetch members of cross-site channels. Same-site refs continue to resolve locally but via the same `ListRoomMembers` method that powers `member.list`, giving one uniform code path for channel expansion. The rest of `handleAddMembers` — dedup, `ResolveAccounts`, capacity check, canonical publish — is unchanged and runs only after the full member list is assembled.

## Scope

Covers:
- Wire-format change on `AddMembersRequest.Channels` and `MembersAdded.Channels` from `[]string` to `[]ChannelRef{RoomID, SiteID}`.
- A new site-aware expansion function in `room-service/handler.go` replacing the current `expandChannels`.
- A `MemberListClient` interface with a NATS-backed implementation for cross-site `member.list` calls (enrich=false, no limit/offset).
- Symmetric authorization: the requester must hold a subscription to every source channel, whether it lives on the local site or a remote site.
- Removal of the now-dead store methods `GetRoomMembersByRooms` and `GetAccountsByRooms`.
- New config `MEMBER_LIST_TIMEOUT` (default `5s`) for cross-site request/reply.
- Unit tests for the expansion function and the new client; integration tests covering same-site, subscription-fallback, authorization, and an end-to-end two-site scenario.

Out of scope:
- Any change to `member.list` itself.
- Any change to the canonical `member.add` event schema, `room-worker`, `inbox-worker`, `message-worker`, or `broadcast-worker`.
- Caching, retries, or parallel fan-out of cross-site lookups.
- Cross-site routing for add-member itself — the target room's site is already determined by the request subject's `{siteID}` segment and NATS gateways. Only the *source channel* routing is new.

## NATS Subjects

No new subjects. The cross-site call reuses the existing `member.list` subject shipped on this branch:

```text
chat.user.{requester}.request.room.{roomID}.{siteID}.member.list
```

Built via `subject.MemberList(account, roomID, siteID)` in `pkg/subject/subject.go`. The `{siteID}` is the source channel's site; gateways route to that site's `room-service`.

## Data Models

### `pkg/model/member.go`

```go
type ChannelRef struct {
    RoomID string `json:"roomId" bson:"roomId"`
    SiteID string `json:"siteId" bson:"siteId"`
}

type AddMembersRequest struct {
    RoomID           string        `json:"roomId"           bson:"roomId"`
    Users            []string      `json:"users"            bson:"users"`
    Orgs             []string      `json:"orgs"             bson:"orgs"`
    Channels         []ChannelRef  `json:"channels"         bson:"channels"` // CHANGED from []string
    History          HistoryConfig `json:"history"          bson:"history"`
    RequesterID      string        `json:"requesterId"      bson:"requesterId"`
    RequesterAccount string        `json:"requesterAccount" bson:"requesterAccount"`
    Timestamp        int64         `json:"timestamp"        bson:"timestamp"`
}

type MembersAdded struct {
    Individuals     []string     `json:"individuals"`
    Orgs            []string     `json:"orgs"`
    Channels        []ChannelRef `json:"channels"` // CHANGED from []string
    AddedUsersCount int          `json:"addedUsersCount"`
}
```

`AddMembersRequest` keeps both JSON and BSON tags (its existing convention on `main`), since tests and any future store reads are free to round-trip it through MongoDB. `MembersAdded` is wire-only and keeps json-only tags, matching its current shape on `main`: `room-worker` JSON-marshals it into `Message.SysMsgData` (see `room-worker/handler.go`), and `message-worker` persists that as a BLOB into Cassandra's `sys_msg_data` column. No MongoDB code path reads `MembersAdded`, so adding BSON tags to it would be cargo-culting — `ChannelRef` itself carries both sets of tags so either host struct can opt in later.

**Client vs. canonical fields:** clients send only `users`, `orgs`, `channels`, and `history`. Room-service populates `roomId`, `requesterId`, `requesterAccount`, and `timestamp` at step 9 (normalize-and-publish) from the parsed subject and the subscription lookup, so `room-worker` can consume them off the canonical subject `chat.room.canonical.{siteID}.member.add`, which encodes none of them.

### Wire example (client → `chat.user.{account}.request.room.{roomID}.{siteID}.member.add`)

```json
{
  "users": ["alice"],
  "orgs": ["sect-42"],
  "channels": [
    {"roomId": "room-eng", "siteId": "site-us"},
    {"roomId": "room-qa",  "siteId": "site-eu"}
  ],
  "history": {"mode": "none"}
}
```

### Breaking change note

This is a wire-incompatible change. The add-member feature is on a feature branch (`claude/add-room-member-feature-5FPbS`) and has not merged to `main`, so there is no deployed schema and no migration is needed. No backwards-compat shim on the Go side.

## Expansion Function

### Contract

Replaces `expandChannels` in `room-service/handler.go`.

```go
// expandChannelRefs returns the union of orgs + accounts across refs; fail-fast on any error.
func (h *Handler) expandChannelRefs(
    ctx context.Context,
    requester string,
    refs []model.ChannelRef,
) (orgIDs, accounts []string, err error) {
    for _, ref := range refs {
        var members []model.RoomMember

        if ref.SiteID == h.siteID {
            // Symmetric auth: requester must be subscribed to the source channel.
            if _, err := h.store.GetSubscription(ctx, requester, ref.RoomID); err != nil {
                if errors.Is(err, model.ErrSubscriptionNotFound) {
                    return nil, nil, errNotChannelMember // unwrapped so sanitizeError forwards it
                }
                return nil, nil, fmt.Errorf("subscription check %s: %w", ref.RoomID, err)
            }
            members, err = h.store.ListRoomMembers(ctx, ref.RoomID, nil, nil, false)
            if err != nil {
                return nil, nil, fmt.Errorf("local list-members %s: %w", ref.RoomID, err)
            }
        } else {
            members, err = h.memberListClient.ListMembers(ctx, requester, ref)
            if err != nil {
                return nil, nil, fmt.Errorf("remote list-members %s@%s: %w", ref.RoomID, ref.SiteID, err)
            }
        }

        for i := range members {
            m := &members[i].Member
            switch m.Type {
            case model.RoomMemberOrg:
                orgIDs = append(orgIDs, m.ID)
            case model.RoomMemberIndividual:
                accounts = append(accounts, m.Account)
            }
        }
    }
    return orgIDs, accounts, nil
}
```

### Updated `handleAddMembers`

Step 5 call-site changes from `expandChannels` to `expandChannelRefs`:

```go
channelOrgIDs, channelAccounts, err := h.expandChannelRefs(ctx, requester, req.Channels)
if err != nil {
    return nil, fmt.Errorf("expand channels: %w", err)
}
```

**Processing order is preserved.** `handleAddMembers` runs strictly sequentially:

1. Parse subject
2. `GetSubscription(requester, roomID)` — verify requester in target room
3. `GetRoom`; guard room type and `Restricted` flag
4. Unmarshal `AddMembersRequest`
5. `expandChannelRefs` — **all refs resolved fully before proceeding; fail-fast on any error**
6. `dedup` orgs + users with channel-sourced additions
7. `ResolveAccounts` — returns net-new accounts
8. `CountSubscriptions` — capacity check
9. Publish normalized request to `chat.room.canonical.{siteID}.member.add`
10. Reply `{"status":"accepted"}`

No subscription writes, no events, no outbox publishes happen before step 9. A failed channel expansion aborts before any state mutation.

## `MemberListClient`

New file: `room-service/memberlist_client.go`.

### Interface

`requester` is the only free parameter; the channel itself is a `ChannelRef` so there's one argument per logical concept. `requester`, `ch.RoomID`, and `ch.SiteID` all end up in the subject — the JSON body carries only request-scoped flags (`Enrich`/`Limit`/`Offset`), none of which this client sets, so the wire body is `{}`.

```go
type MemberListClient interface {
    ListMembers(ctx context.Context, requester string, ch model.ChannelRef) ([]model.RoomMember, error)
}
```

### NATS-backed implementation

```go
type natsMemberListClient struct {
    nc      *nats.Conn
    timeout time.Duration
}

func NewNATSMemberListClient(nc *nats.Conn, timeout time.Duration) MemberListClient {
    return &natsMemberListClient{nc: nc, timeout: timeout}
}

func (c *natsMemberListClient) ListMembers(ctx context.Context, requester string, ch model.ChannelRef) ([]model.RoomMember, error) {
    // Body marshals to {}; requester, roomID, siteID travel in the subject.
    body, err := json.Marshal(model.ListRoomMembersRequest{})
    if err != nil {
        return nil, fmt.Errorf("marshal member.list body: %w", err)
    }

    reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()

    // RequestMsgWithContext keeps a nats.Header so trace + correlation propagation can be added later.
    out := &nats.Msg{
        Subject: subject.MemberList(requester, ch.RoomID, ch.SiteID),
        Data:    body,
        Header:  nats.Header{},
    }
    reply, err := c.nc.RequestMsgWithContext(reqCtx, out)
    if err != nil {
        return nil, fmt.Errorf("member.list request to %s: %w", ch.SiteID, err)
    }

    // TryParseError distinguishes ReplyError bodies from success bodies that have no `error` field.
    // The `remote member.list:` prefix is whitelisted by sanitizeError, so the remote site's
    // user-safe message propagates verbatim to the caller.
    if errResp, ok := natsutil.TryParseError(reply.Data); ok {
        return nil, fmt.Errorf("remote member.list: %s", errResp.Error)
    }

    var resp model.ListRoomMembersResponse
    if err := json.Unmarshal(reply.Data, &resp); err != nil {
        return nil, fmt.Errorf("unmarshal member.list reply: %w", err)
    }
    return resp.Members, nil
}
```

**Error-reply helper (new, required by this spec):** `pkg/natsutil/reply.go` does not yet expose a parser for `model.ErrorResponse`; this spec adds one. Exact signature:

```go
// TryParseError returns the ErrorResponse iff data decodes cleanly with a non-empty Error.
func TryParseError(data []byte) (model.ErrorResponse, bool) {
    var r model.ErrorResponse
    // false when Unmarshal fails (malformed/foreign body) OR when Error is empty (success body);
    // true only when decode succeeds AND Error is non-empty (genuine ReplyError body).
    if err := json.Unmarshal(data, &r); err != nil || r.Error == "" {
        return model.ErrorResponse{}, false
    }
    return r, true
}
```

This is not a plan-phase deferment: without it, an error reply silently decodes into `ListRoomMembersResponse{Members: nil}` and the expansion returns "zero members" instead of failing, which is a correctness bug for the add-member flow. The helper gets a unit test in `pkg/natsutil/reply_test.go` covering: success body → `(_, false)`, error body → `(resp, true)` with the message preserved, malformed JSON → `(_, false)`, empty `{}` → `(_, false)`.

### `//go:generate mockgen`

At the top of `memberlist_client.go`:

```go
//go:generate mockgen -source=memberlist_client.go -destination=mock_memberlist_client_test.go -package=main
```

Mock is regenerated by `make generate SERVICE=room-service` and committed with the change.

### Handler wiring

`Handler` struct gains `memberListClient MemberListClient`. `NewHandler` takes it as an explicit constructor parameter alongside existing dependencies. `main.go` parses the new config and constructs the client:

```go
memberListClient := NewNATSMemberListClient(nc.NatsConn(), cfg.MemberListTimeout)
h := NewHandler(store, keyStore, memberListClient, cfg.SiteID, cfg.MaxRoomSize, cfg.MaxBatchSize, publishToStream)
```

### Configuration

New field on `Config` in `room-service/main.go`:

```go
MemberListTimeout time.Duration `env:"MEMBER_LIST_TIMEOUT" envDefault:"5s"`
```

Non-critical config — `envDefault` satisfies CLAUDE.md §6 (never default secrets, always default non-critical). Use `time.Duration` so callers can tune with `1500ms`, `10s`, etc.

## Authorization

For both same-site and cross-site channel refs, the requester must hold a subscription to the source channel. Rationale:

- **Semantic consistency** — "you can only import a channel's members if you're a member of that channel."
- **Same-site:** `expandChannelRefs` calls `h.store.GetSubscription(ctx, requester, ref.RoomID)`; on `ErrSubscriptionNotFound`, returns an error that `sanitizeError` translates to a user-safe message.
- **Cross-site:** the remote `member.list` handler performs this check on its own site. The local client surfaces the error through the response body (`model.ErrorResponse`) and `expandChannelRefs` wraps it with context.

### `sanitizeError` whitelist

Introduce a new sentinel in `room-service/helper.go` (do not reuse `errNotRoomMember` — its "only room members can list members" wording is specific to `member.list` and would be misleading in the add-member flow):

```go
errNotChannelMember = errors.New("only channel members can use a channel as a source")
```

Two concrete edits to `room-service/helper.go` are required to make this work — stating both explicitly so the implementation can't miss either:

1. **Add `errNotChannelMember` to the `errors.Is` whitelist in `sanitizeError`**, alongside `errNotRoomMember`, `errInvalidRole`, `errPromoteRequiresIndividual`, etc. Without this, the `errors.Is` switch drops into the default branch and the user sees `"internal error"` instead of the sentinel message. Follows the exact pattern PR #118 already uses for `errPromoteRequiresIndividual`.
2. **Add `"remote member.list:"` to the substring fallback list** in `sanitizeError`'s default branch, so remote user-facing messages returned by the peer site's `member.list` (via `natsutil.ReplyError(model.ErrorResponse{Error: …})`) propagate through to the caller as `"remote member.list: <remote-msg>"`.

In `expandChannelRefs`, when `GetSubscription` returns `model.ErrSubscriptionNotFound`, return `errNotChannelMember` (no wrapping) so the `errors.Is` whitelist hit forwards the user-facing message unchanged. Any other `GetSubscription` error is wrapped via `fmt.Errorf("check subscription for channel %q: %w", ref.RoomID, err)` and mapped to `"internal error"` by `sanitizeError`'s default branch.

Transport failures (timeout, no responder) surface as `"internal error"` by default, which is acceptable — the caller doesn't need to know whether the remote site was unreachable or simply slow.

## Dead Code Removal

After this change, the following are only referenced by the pre-existing (now-removed) `expandChannels`:

- `RoomStore.GetRoomMembersByRooms(ctx, roomIDs []string) ([]model.RoomMember, error)`
- `RoomStore.GetAccountsByRooms(ctx, roomIDs []string) ([]string, error)`

Remove from `room-service/store.go` and `store_mongo.go`. Regenerate `mock_store_test.go`. Delete their unit-test cases in `handler_test.go` and integration-test cases in `integration_test.go`. Their fallback semantics (probe `room_members`, fall back to `subscriptions`) live inside `ListRoomMembers` — no behavior regression.

## Testing

### Unit tests — `room-service/handler_test.go`

New table-driven `TestHandler_AddMembers_ChannelExpansion` using `NewMockRoomStore` and a new `NewMockMemberListClient`:

| # | Scenario | Mock setup | Expected |
|---|----------|------------|----------|
| 1 | Single same-site channel, individuals only | `GetSubscription(req, ch1)` ok; `ListRoomMembers(ch1, nil, nil, false)` returns 2 individuals | 2 accounts, no orgs, no client call |
| 2 | Single same-site channel, orgs only | `ListRoomMembers` returns 2 orgs | 2 org IDs, no accounts |
| 3 | Single same-site channel, mixed | 1 org + 1 individual | 1 org, 1 account |
| 4 | Single cross-site channel | `memberListClient.ListMembers(req, ChannelRef{ch1, site-eu})` returns 1 org + 2 individuals; no local calls | 1 org + 2 accounts |
| 5 | Mixed same-site + cross-site | two refs — one local, one remote | union returned |
| 6 | Requester not subscribed to same-site source | `GetSubscription` → `model.ErrSubscriptionNotFound` | returns `errNotChannelMember` sentinel (unwrapped); `ListRoomMembers` and client never called; `sanitizeError` forwards the sentinel's message |
| 6b | Same-site `GetSubscription` generic error | `GetSubscription` → generic infra err | error wraps `"subscription check"`; `ListRoomMembers` and client never called |
| 7 | Same-site `ListRoomMembers` error | store returns generic err | error wraps `"local list-members"` |
| 8 | Cross-site client error | `client.ListMembers` returns err | error wraps `"remote list-members"`; fail-fast |
| 9 | Fail-fast ordering | two refs, first remote fails | second ref `.Times(0)` |
| 10 | Empty refs | `req.Channels = nil` | returns `nil, nil, nil`; no calls |
| 11 | Unknown `Member.Type` on returned row | row with `Type=""` | silently skipped |

Existing `TestHandler_AddMembers_*` cases are updated to pass `[]model.ChannelRef`. Cases that asserted on `GetRoomMembersByRooms` / `GetAccountsByRooms` are deleted.

### Unit tests — `room-service/memberlist_client_test.go` (new)

Uses an embedded `nats-server/v2/test` instance:

| # | Scenario | Expected |
|---|----------|----------|
| 1 | Happy path | Responder replies `ListRoomMembersResponse`; client returns decoded members |
| 2 | Remote returns `model.ErrorResponse` | Client returns error wrapping the remote message |
| 3 | Invalid JSON reply | `"unmarshal member.list reply"` error |
| 4 | Request times out (no responder) | `"member.list request to <siteID>"` error within `timeout + epsilon` |
| 5 | Body shape | Responder decodes the body and asserts `Limit == nil`, `Offset == nil`, `Enrich == false`. All three are `omitempty` and we send zero values, so the body marshals to `{}` — the remote handler treats this as the bare, unpaginated default. |
| 6 | Subject correctness | Responder subscribes on `subject.MemberList(requester, ch.RoomID, ch.SiteID)` — roomID and siteID are taken from the `ChannelRef` argument, not the body |
| 7 | Context cancellation | Caller cancels `ctx`; client returns context-canceled error |

### Integration tests — `room-service/integration_test.go` (`//go:build integration`)

Uses the existing `setupMongo(t)` testcontainers helper. Cases 1–3 use a single in-process NATS; case 4 uses two in-process NATS servers + two `room-service` instances.

| # | Scenario | Setup | Expected |
|---|----------|-------|----------|
| 1 | Add-member via same-site channel (room_members path) | Seed source channel with 2 individuals + 1 org in `room_members`; requester subscribed to both rooms | Target room gains subscriptions for the expanded members |
| 2 | Add-member via same-site channel (subscriptions fallback path) | Source has zero `room_members`, three subscriptions | Three accounts resolved and added |
| 3 | Add-member where requester is NOT subscribed to same-site source | Seed source channel; requester has no subscription to it | Request rejected with subscription error; target unchanged |
| 4 | **Two-site end-to-end** (required) | Boot two in-process NATS servers + two `room-service` instances wired to two Mongo DBs. Target room on `site-a`; source channel on `site-b`. Requester has a subscription on both sides (simulating cross-site subscription replication). `NATSMemberListClient` on site-a is wired to a `*nats.Conn` that can reach site-b — either via a gateway link (`server.Options.Gateway`) or by connecting the site-a client directly to the site-b server for this test only | Add-member on site-a pulls the source channel's members from site-b via `member.list`, then publishes a complete resolved payload to site-a's canonical stream. Target room on site-a gains the expected subscriptions. |
| 5 | Cross-site timeout | Second NATS server up, but no `member.list` responder on it | Add-member fails with wrapped timeout error within `MemberListTimeout + epsilon`; target room unchanged |

**Two-site wiring — implementation note:** Either approach satisfies the requirement. The simpler path (connect the test's site-a client directly to the site-b `nats.Conn` since gateway topology isn't the subject under test) is the default; the plan will spike this first. If it proves non-trivial, the developer raises it before deferring — full multi-site coverage is a must for this spec.

### Coverage

- Minimum 80% across `room-service`; target 90%+ on `expandChannelRefs` and `natsMemberListClient.ListMembers` (CLAUDE.md §4).
- TDD Red-Green-Refactor: update model tests → expansion-function unit tests → client unit tests → integration tests → implementation.
- `make generate SERVICE=room-service` after updating `store.go` and adding the client interface.
- `make test` runs with `-race`.

## Files Changed

| File | Change |
|------|--------|
| `pkg/model/member.go` | Add `ChannelRef`; change `AddMembersRequest.Channels` and `MembersAdded.Channels` to `[]ChannelRef` |
| `pkg/model/model_test.go` | `ChannelRef` JSON + BSON round-trip; update existing `TestAddMembersRequestJSON` and `TestMembersAddedJSON` cases to use `[]ChannelRef` |
| `room-service/memberlist_client.go` | New: `MemberListClient` interface + `natsMemberListClient` impl + `//go:generate` directive |
| `room-service/memberlist_client_test.go` | New: client unit tests against embedded NATS |
| `room-service/mock_memberlist_client_test.go` | Generated |
| `room-service/handler.go` | Replace `expandChannels` with `expandChannelRefs`; `Handler` + `NewHandler` gain `memberListClient` |
| `pkg/natsutil/reply.go` | Add `TryParseError(data []byte) (model.ErrorResponse, bool)` — required to distinguish `ReplyError` bodies from success bodies that have no `error` field |
| `pkg/natsutil/reply_test.go` | New cases for `TryParseError`: success body, error body, malformed JSON, empty `{}` |
| `room-service/helper.go` | Add `errNotChannelMember` sentinel AND add it to `sanitizeError`'s `errors.Is` whitelist; add `"remote member.list:"` to the substring fallback list |
| `room-service/handler_test.go` | New `TestHandler_AddMembers_ChannelExpansion`; update existing cases to `[]ChannelRef`; delete cases for removed store methods |
| `room-service/store.go` | Remove `GetRoomMembersByRooms` and `GetAccountsByRooms` from `RoomStore` |
| `room-service/store_mongo.go` | Remove their implementations |
| `room-service/mock_store_test.go` | Regenerated |
| `room-service/integration_test.go` | New cases 1–5; delete obsolete cases for removed store methods |
| `room-service/main.go` | Add `MemberListTimeout` config; construct `NATSMemberListClient`; pass to `NewHandler` |
| `room-worker/handler_test.go` | Update existing `AddMembersRequest` fixtures to use `[]ChannelRef` (no logic changes; compilation fix only) |

No logic changes to `room-worker/handler.go` — its `Channels: req.Channels` line stays identical; only the underlying type changes. No changes to `inbox-worker`, `message-worker`, `broadcast-worker`, `pkg/subject`, or the canonical event schema.

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| `ChannelRef{RoomID, SiteID}` (wire-breaking) | Clean model over parallel-array hacks. Feature hasn't shipped past the branch, so no migration cost. |
| Branch by site in `expandChannelRefs` | Keep the same-site hot path in-process; use NATS only when we must. |
| Same-site reuses `ListRoomMembers` | One source of truth for channel expansion; fallback (`room_members` → `subscriptions`) already implemented there; eliminates duplicate store methods. |
| Requester-must-subscribe (symmetric) | Same rule for both sites; "you need to be a member of any channel you use as a source" is the natural semantic. |
| Fail-fast on any channel expansion error | Add-member is a privileged write; partial success is confusing. Client retries or drops the bad ref. |
| Full channel expansion before side effects | Guarantees no partial writes; all of `dedup` / `ResolveAccounts` / capacity / publish happen only once the full member list is assembled. |
| `MemberListClient` interface | Clean mock seam; cross-site behavior is easy to regress without test isolation. |
| Sequential cross-site fan-out | Realistic channel counts per request are small; parallel (`errgroup`) is a future perf pass only if needed. |
| `enrich=false`, no `limit`/`offset` | We need only `Type`, `ID`, `Account`. Enrichment is wasted work; we must get the full list to avoid truncating additions. |
| Remove `GetRoomMembersByRooms` / `GetAccountsByRooms` | Only callers go away; `ListRoomMembers` covers their behavior; keeping them would be dead code. |
| `MEMBER_LIST_TIMEOUT` env (default `5s`) | Bounded wait for cross-site reply. Overridable without rebuild. |
| `TryParseError` helper in `pkg/natsutil` | Error and success bodies are both valid JSON for their respective target structs; without an explicit disambiguator that checks for a non-empty `error` field, a remote error silently becomes `{Members: nil}`. Correctness-critical, so the helper is defined in this spec — not deferred. |
| `RequestMsgWithContext` over `RequestWithContext` | Outgoing `*nats.Msg` carries a `nats.Header`, preserving the seam for OTel trace-context propagation (via `natsutil.NewHeaderCarrier`) and correlation-ID forwarding. Both are codebase-wide follow-ups out of scope here, but the seam is zero-cost to keep open. |
| Two-site integration test is required | Design-level guarantee. If the wiring proves hard, the plan raises it; we do not defer. |
