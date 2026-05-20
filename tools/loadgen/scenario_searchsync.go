// tools/loadgen/scenario_searchsync.go
//
// search-sync-lag scenario: measures the canonical→search-index lag end-to-end.
// Publishes a canonical MessageEvent containing a unique searchable token, then
// polls search-service via the search.messages NATS endpoint until the doc
// appears. Records loadgen_search_index_lag_seconds.
//
// The measured lag covers four hops, all dominated in practice by the ES
// refresh_interval (default 30s on the messageCollection index template):
//
//	publish → search-sync-worker consume → bulk index → ES refresh → query hit
//
// Operators tuning search-sync should compare runs across refresh_interval
// values and bulk batch sizes; the scenario does not change either, it only
// measures the resulting visibility lag.
//
// # ACL precondition (C1)
//
// search-service AND's the caller's `roomIds` filter with a terms-lookup
// against the per-user `user-room` ES doc (search-service/query_messages.go
// `termsLookupClause`). That doc is written exclusively by search-sync-worker
// on `OutboxMemberAdded` / `OutboxMemberRemoved` events on the local INBOX
// stream (search-sync-worker/user_room.go). Loadgen's Seed only writes Mongo,
// so out of the box every search returns 0 hits — every poll times out, every
// observation is missed, the dashboard is silent.
//
// Approach: at Run start we publish a synthesized `member_added` OutboxEvent
// for each unique (account, room) fixture tuple onto the local INBOX subject
// `chat.inbox.{siteID}.member_added`. search-sync-worker's `user-room-sync`
// consumer picks them up, builds the user-room doc, and bulk-indexes it.
// After a short ACL warmup window (`--search-sync-acl-wait`, default 35s —
// covers ES refresh_interval=30s plus bulk-batch flush slack) the per-tick
// publish/poll loop starts. We do NOT mutate Mongo state here; the ES-side
// ACL is a pure additive write that the loadgen teardown leaves in place
// (the user-room index is loadgen-isolated by the search-sync-worker config
// in dev).
//
// We don't touch Seed() because seeding runs through dispatch.go (a file owned
// by a sibling agent) and threading a NATS handle through Seed's signature
// would force a cross-agent edit. Publishing from Run gets the same effect
// using the publisher we already hold.
//
// # Injection mode (C2)
//
// This scenario publishes to MESSAGES_CANONICAL_{siteID} (the stream that
// search-sync-worker consumes from). With --inject=frontdoor, loadgen's
// publisher uses core NATS — that publish has no subscriber on the canonical
// subject and silently dead-letters. NewGenerator therefore requires
// --inject=canonical and returns a clear error otherwise.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// search-sync-lag flag defaults. The poll interval is decoupled from
// raw-consistency's --raw-poll-interval default (10ms) because polling
// every 10ms when ES is refreshing every 30s burns ~3000 NATS RPCs per
// publish per pending poll for no signal. 250ms gives ~120 polls across
// the SLO window — enough granularity to characterize the curve.
//
// The 90s poll timeout is 3× the default ES refresh_interval to cover the
// long tail (compaction, GC pauses, bulk-batch flush slack). Operators that
// expect a faster SUT can lower it via the flag; nothing in the scenario
// requires it to be ≥ refresh_interval, but observations below ~25s will be
// rare in practice.
const (
	defaultSearchSyncPollInterval = 250 * time.Millisecond
	defaultSearchSyncTimeout      = 90 * time.Second
	// ACL warmup: ES refresh_interval (30s) + buffer for bulk flush. Bumping
	// this is the right knob if the operator sees high "timeout" counts in the
	// first warmup-duration of the run.
	defaultSearchSyncACLWait = 35 * time.Second
)

type searchSyncScenario struct{}

func (searchSyncScenario) Name() string          { return "search-sync-lag" }
func (searchSyncScenario) DefaultPreset() string { return "search-read" }

func (searchSyncScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	if len(deps.Fixtures().Subscriptions) == 0 {
		return nil, fmt.Errorf("search-sync-lag requires at least one fixture subscription; run seed first")
	}
	if deps.InjectMode() != InjectCanonical {
		return nil, fmt.Errorf(
			"search-sync-lag requires --inject=canonical (got %q): "+
				"the scenario publishes to MESSAGES_CANONICAL via JetStream so "+
				"search-sync-worker can consume; frontdoor mode publishes via core NATS "+
				"and the canonical subject has no core subscriber",
			deps.InjectMode())
	}
	return &searchSyncGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(searchSyncScenario{}) }

// ------------------------------------------------------------------ generator

type searchSyncGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// Run drives the open-loop publish-then-poll cycle. Per tick: pick a fixture
// subscription, publish a canonical MessageEvent with a unique token in the
// content field, then poll search.messages until that doc surfaces. Each
// terminal outcome increments loadgen_search_index_visible_total{outcome}
// AND, on success, observes the lag into loadgen_search_index_lag_seconds.
//
// Outcomes:
//   - "visible"          — search returned our doc; lag observed.
//   - "timeout"          — poll deadline passed without a hit (ES still
//     behind, search-service down, or ACL doc missing).
//   - "transport_error"  — NATS request itself failed.
//   - "publish_error"    — canonical publish failed; no poll happened.
//   - "dropped_inflight" — in-flight semaphore was full; poll was skipped to
//     avoid open-loop amplification.
//
// Operators reading a zero-observation dashboard can use the outcome counter
// to distinguish "ES slow but working" from "scenario misconfigured" from
// "search-service down".
func (g *searchSyncGenerator) Run(ctx context.Context) error {
	pollInterval := g.rf.SearchSync.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultSearchSyncPollInterval
	}
	pollTimeout := g.rf.SearchSync.Timeout
	if pollTimeout <= 0 {
		pollTimeout = defaultSearchSyncTimeout
	}
	aclWait := g.rf.SearchSync.ACLWait
	if aclWait <= 0 {
		aclWait = defaultSearchSyncACLWait
	}
	requestTimeout := g.rf.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 2 * time.Second
	}

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 1
	}

	subs := g.deps.Fixtures().Subscriptions
	siteID := g.deps.SiteID()
	publisher := g.deps.Publisher()
	requester := g.deps.Requester()
	metrics := g.deps.Metrics()

	// Pre-flight: bootstrap the user-room ES ACL by publishing one synthetic
	// member_added event per unique (account, room) fixture tuple. See the
	// package-level "ACL precondition" doc block. If the bootstrap publish
	// fails we increment outcome=bootstrap_error (so the dashboard distinguishes
	// "scenario tried but failed" from "scenario never ran") AND surface the
	// error rather than running blind — the scenario would otherwise produce
	// zero observations.
	//
	// SkipACLBootstrap short-circuits both the publishes and the wait. Set
	// when iterating fast against a warm cluster where the ACL doc is known
	// to already exist from a prior run.
	if g.rf.SearchSync.SkipACLBootstrap {
		slog.Info("search-sync-lag ACL bootstrap skipped (--search-sync-skip-acl-bootstrap)")
	} else {
		pairCount, err := bootstrapSearchSyncACL(ctx, publisher, siteID, subs)
		if err != nil {
			metrics.SearchIndexVisible.WithLabelValues("bootstrap_error").Inc()
			return fmt.Errorf("bootstrap search-sync ACL: %w", err)
		}
		slog.Info("search-sync-lag ACL bootstrap complete",
			"pairs", pairCount,
			"acl_wait", aclWait.String())
		// Wait for search-sync-worker to consume the events, bulk-index, and the
		// ES refresh to expose them. Cancellation during the wait is a clean exit.
		// Use NewTimer + Stop so a mid-wait cancel doesn't leak the timer for up
		// to aclWait (default 35s).
		aclTimer := time.NewTimer(aclWait)
		select {
		case <-ctx.Done():
			aclTimer.Stop()
			return nil
		case <-aclTimer.C:
		}
	}

	maxInFlight := g.deps.MaxInFlight()
	if maxInFlight <= 0 {
		maxInFlight = 1
	}
	sem := make(chan struct{}, maxInFlight)

	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	var wg sync.WaitGroup
	defer wg.Wait()

	var pickIdx int
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			sub := subs[pickIdx%len(subs)]
			pickIdx++

			msgID := idgen.GenerateMessageID()
			token := searchIndexToken(msgID)
			publishedAt := time.Now()

			if err := publishCanonicalForSearchSync(ctx, publisher, siteID, &sub, msgID, token, publishedAt); err != nil {
				metrics.SearchIndexVisible.WithLabelValues("publish_error").Inc()
				continue
			}

			// Bound in-flight pollers. The open-loop math is
			// rate × pollTimeout / pollInterval requests in the worst case
			// (e.g. 100rps × 60s ÷ 250ms = 1.44M RPCs); without the semaphore
			// a misbehaving SUT turns into a self-DoS.
			select {
			case sem <- struct{}{}:
			default:
				metrics.SearchIndexVisible.WithLabelValues("dropped_inflight").Inc()
				continue
			}

			lookup := newSearchIndexLookup(requester, sub.User.Account, sub.RoomID, token, msgID, requestTimeout)
			wg.Add(1)
			go func(id string, pubAt time.Time) {
				defer wg.Done()
				defer func() { <-sem }()
				outcome, err := pollUntilVisible(ctx, lookup, id, pubAt, pollInterval, pollTimeout)
				if err != nil {
					switch {
					case ctx.Err() != nil:
						// Run cancelled mid-poll; don't classify as a SUT outcome.
						return
					case errors.Is(err, ErrRAWTimeout):
						metrics.SearchIndexVisible.WithLabelValues("timeout").Inc()
					default:
						metrics.SearchIndexVisible.WithLabelValues("transport_error").Inc()
					}
					return
				}
				metrics.SearchIndexVisible.WithLabelValues("visible").Inc()
				metrics.SearchIndexLag.Observe(outcome.Lag.Seconds())
			}(msgID, publishedAt)
		}
	}
}

// bootstrapSearchSyncACL publishes one synthetic OutboxMemberAdded event per
// unique (account, roomID) tuple in the fixture subscriptions, onto the local
// INBOX subject. search-sync-worker's user-room-sync consumer fans these out
// into one ES user-room doc per account, granting the scenario's queries the
// ACL gate that search.messages requires.
//
// We use Timestamp=now and HistorySharedSince=nil (unrestricted) to match the
// "normal" join path. RoomName/RoomType come from the subscription; missing
// values fall back to safe defaults (the user-room collection doesn't index
// roomName/roomType, only `rooms[]` membership).
// bootstrapSearchSyncACL publishes one synthetic OutboxMemberAdded event per
// unique (account, roomID) tuple. Returns the number of unique pairs published
// so the caller can log positive confirmation. Honors ctx cancellation between
// publishes so a shutdown during a large-fixture bootstrap doesn't sit
// publishing into a dying connection.
func bootstrapSearchSyncACL(
	ctx context.Context,
	publisher Publisher,
	siteID string,
	subs []model.Subscription,
) (int, error) {
	type pair struct{ account, room string }
	seen := make(map[pair]struct{}, len(subs))
	subj := subject.InboxMemberAdded(siteID)
	now := time.Now().UTC().UnixMilli()
	for i := range subs {
		// Bail out promptly on shutdown so a 10k-fixture bootstrap doesn't
		// keep firing publishes after ctx cancellation.
		if err := ctx.Err(); err != nil {
			return len(seen), fmt.Errorf("bootstrap cancelled: %w", err)
		}
		s := subs[i]
		key := pair{account: s.User.Account, room: s.RoomID}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		inner := model.InboxMemberEvent{
			RoomID:    s.RoomID,
			RoomType:  s.RoomType,
			SiteID:    siteID,
			Accounts:  []string{s.User.Account},
			JoinedAt:  now,
			Timestamp: now,
		}
		innerData, err := json.Marshal(inner)
		if err != nil {
			return len(seen), fmt.Errorf("marshal InboxMemberEvent for %s/%s: %w", s.User.Account, s.RoomID, err)
		}
		evt := model.OutboxEvent{
			Type:      model.OutboxMemberAdded,
			SiteID:    siteID,
			Payload:   innerData,
			Timestamp: now,
		}
		data, err := json.Marshal(evt)
		if err != nil {
			return len(seen), fmt.Errorf("marshal OutboxEvent for %s/%s: %w", s.User.Account, s.RoomID, err)
		}
		if err := publisher.Publish(ctx, subj, data); err != nil {
			return len(seen), fmt.Errorf("publish ACL member_added for %s/%s: %w", s.User.Account, s.RoomID, err)
		}
	}
	return len(seen), nil
}

// publishCanonicalForSearchSync publishes a MessageEvent on the
// MESSAGES_CANONICAL stream so search-sync-worker indexes it. Bypassing the
// frontdoor (message-gatekeeper) is intentional: this scenario measures
// canonical→index lag, not the gatekeeper enqueue path.
//
// The HEADER_RUN_ID stamp is not set because search-sync-worker doesn't honor
// it. Concurrent loadgen runs against the same NATS/ES still don't false-match
// our search lookup — the per-poll filter uses the msgID (UUIDv7-derived,
// globally unique), and the token is a SHA-256 prefix of that same msgID. The
// real cross-run interference is ES/search-service load contention, not
// document identity collisions.
func publishCanonicalForSearchSync(
	ctx context.Context,
	publisher Publisher,
	siteID string,
	sub *model.Subscription,
	msgID, token string,
	publishedAt time.Time,
) error {
	evt := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID:          msgID,
			RoomID:      sub.RoomID,
			UserID:      sub.User.ID,
			UserAccount: sub.User.Account,
			Content:     searchSyncMessageContentWithToken(token),
			CreatedAt:   publishedAt.UTC(),
		},
		SiteID:    siteID,
		Timestamp: publishedAt.UnixMilli(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal MessageEvent: %w", err)
	}
	if err := publisher.Publish(ctx, subject.MsgCanonicalCreated(siteID), data); err != nil {
		return fmt.Errorf("publish canonical: %w", err)
	}
	return nil
}

// newSearchIndexLookup builds a LookupFn that polls search.messages for the
// given (token, msgID, roomID) tuple. The lookup returns:
//
//   - nil                       — message is indexed and our msgID is in the hit list
//   - ErrRAWNotVisible          — search returned 0 hits or only hits with other msgIDs
//   - <transport error>         — NATS request failed; surfaced verbatim
//
// Scoping by roomID keeps search-service's ES query selective; scoping by
// account keeps the request subject realistic (the same subject pattern the
// real client would use). The request body and target subject are precomputed
// once and reused across every poll iteration — closing over them avoids
// per-poll json.Marshal + fmt.Sprintf allocations on a hot path that can fire
// hundreds of times per published message.
func newSearchIndexLookup(
	requester Requester,
	account, roomID, token, msgID string,
	timeout time.Duration,
) LookupFn {
	req := model.SearchMessagesRequest{
		Query:   token,
		RoomIDs: []string{roomID},
		Size:    10,
	}
	body, marshalErr := json.Marshal(req)
	subj := subject.SearchMessages(account)
	return func(ctx context.Context, _ string) error {
		if marshalErr != nil {
			return fmt.Errorf("marshal SearchMessagesRequest: %w", marshalErr)
		}
		reply, err := requester.Request(ctx, subj, body, timeout)
		if err != nil {
			return fmt.Errorf("search.messages: %w", err)
		}
		var resp model.SearchMessagesResponse
		if err := json.Unmarshal(reply, &resp); err != nil {
			return fmt.Errorf("unmarshal SearchMessagesResponse: %w", err)
		}
		for i := range resp.Messages {
			if resp.Messages[i].MessageID == msgID {
				return nil
			}
		}
		return ErrRAWNotVisible
	}
}

// searchIndexToken derives a deterministic, lowercase-alphanumeric token from
// the messageID. search-sync-worker indexes message content with the
// `custom_analyzer` defined at search-sync-worker/messages.go:160 — a custom
// chain (pattern tokenizer + word_delimiter_graph + lowercase) whose
// load-bearing setting for us is `split_on_numerics=false`: that keeps
// alphanumeric tokens like `lgst1a2b3c…` intact instead of fracturing into
// `lgst` + `1a2b3c`. We additionally restrict to [a-z0-9] so we never depend
// on the tokenizer's exact treatment of separators, and lowercase up front
// since the `lowercase` filter would normalize anyway.
//
// Length 16 (4-char tag + 12 hex = 48 bits of SHA-256) makes accidental
// collisions across one run vanishingly unlikely.
func searchIndexToken(msgID string) string {
	sum := sha256.Sum256([]byte(msgID))
	return "lgst" + hex.EncodeToString(sum[:6]) // 4-char prefix + 12 hex chars = 16 chars
}

// searchSyncMessageContent is exposed for tests; production callers use
// searchSyncMessageContentWithToken to avoid recomputing the hash.
func searchSyncMessageContent(msgID string) string {
	return searchSyncMessageContentWithToken(searchIndexToken(msgID))
}

func searchSyncMessageContentWithToken(token string) string {
	// Prefix so the token is human-recognizable in ES debug queries. The body
	// padding mimics realistic message length so analyzer-side cost reflects
	// typical traffic (one alpha word + a token).
	return "search-sync-probe " + token
}
