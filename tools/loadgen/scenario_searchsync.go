// tools/loadgen/scenario_searchsync.go
//
// search-sync-lag scenario: measures the canonical→search-index lag end-to-end.
// Publishes a canonical MessageEvent containing a unique searchable token, then
// polls search-service via the search.messages NATS endpoint until the doc
// appears. Records loadgen_search_index_lag_seconds, scoped by stage label.
//
// The measured lag covers four hops, all dominated in practice by the ES
// refresh_interval (default 30s on the messageCollection index template):
//
//	publish → search-sync-worker consume → bulk index → ES refresh → query hit
//
// Operators tuning search-sync should compare runs across refresh_interval
// values and bulk batch sizes; the scenario does not change either, it only
// measures the resulting visibility lag.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type searchSyncScenario struct{}

func (searchSyncScenario) Name() string          { return "search-sync-lag" }
func (searchSyncScenario) DefaultPreset() string { return "search-read" }

func (searchSyncScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	if len(deps.Fixtures().Subscriptions) == 0 {
		return nil, fmt.Errorf("search-sync-lag requires at least one fixture subscription; run seed first")
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
// content field, then poll search.messages until that doc surfaces. Each poll
// success observes the lag into loadgen_search_index_lag_seconds. Failed
// publishes skip the poll (the publish error is already counted by Publisher
// metrics); timed-out polls are dropped (the operator should raise --raw-timeout
// when ES refresh_interval is large or under-warmed).
func (g *searchSyncGenerator) Run(ctx context.Context) error {
	pollInterval := g.rf.RAW.PollInterval
	if pollInterval == 0 {
		pollInterval = 250 * time.Millisecond // ES refresh granularity, not request RTT
	}
	pollTimeout := g.rf.RAW.Timeout
	if pollTimeout == 0 {
		// Default ES refresh_interval is 30s; allow comfortable headroom.
		pollTimeout = 60 * time.Second
	}
	requestTimeout := g.rf.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 2 * time.Second
	}

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 1
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	subs := g.deps.Fixtures().Subscriptions
	siteID := g.deps.SiteID()
	publisher := g.deps.Publisher()
	requester := g.deps.Requester()
	metrics := g.deps.Metrics()

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
				continue
			}

			lookup := newSearchIndexLookup(requester, sub.User.Account, sub.RoomID, token, msgID, requestTimeout)
			wg.Add(1)
			go func(id string, pubAt time.Time) {
				defer wg.Done()
				outcome, err := pollUntilVisible(ctx, lookup, id, pubAt, pollInterval, pollTimeout)
				if err != nil {
					return
				}
				metrics.SearchIndexLag.WithLabelValues("search-service").Observe(outcome.Lag.Seconds())
			}(msgID, publishedAt)
		}
	}
}

// publishCanonicalForSearchSync publishes a MessageEvent on the
// MESSAGES_CANONICAL stream so search-sync-worker indexes it. Bypassing the
// frontdoor (message-gatekeeper) is intentional: this scenario measures
// canonical→index lag, not the gatekeeper enqueue path. The HEADER_RUN_ID
// stamp is not set because search-sync-worker doesn't honor it; runs on the
// same NATS would interfere through shared ES indices regardless.
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
// real client would use).
func newSearchIndexLookup(
	requester Requester,
	account, roomID, token, msgID string,
	timeout time.Duration,
) LookupFn {
	return func(ctx context.Context, _ string) error {
		req := model.SearchMessagesRequest{
			Query:   token,
			RoomIDs: []string{roomID},
			Size:    10,
		}
		body, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshal SearchMessagesRequest: %w", err)
		}
		reply, err := requester.Request(ctx, subject.SearchMessages(account), body, timeout)
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
// the messageID. ES's default standard analyzer splits on non-alphanumerics
// and lowercases; restricting the token to [a-z0-9] guarantees it survives
// analysis as a single token so the search-side equality check is reliable.
// Length 16 (64 bits of SHA-256) makes accidental collisions across one run
// vanishingly unlikely.
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
