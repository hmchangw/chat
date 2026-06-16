package pollers

import (
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// startInProcessNATS spins an in-process NATS server bound to a
// random port (Port: -1) and returns a connected nats.Conn. Mirrors
// the room-service/memberlist_client_test.go pattern. The server +
// connection are torn down at t.Cleanup so each test runs against a
// fresh server with no cross-test pollution.
func startInProcessNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Port: -1}
	ns, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second), "nats server did not become ready")
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// publishAndFlush publishes data to subject and synchronously flushes
// so the assertion that follows sees the message guaranteed-delivered.
// nats.Conn.Flush waits for a server-side roundtrip.
func publishAndFlush(t *testing.T, nc *nats.Conn, subject string, data []byte) {
	t.Helper()
	require.NoError(t, nc.Publish(subject, data))
	require.NoError(t, nc.Flush())
}

// drainViaPollFn invokes the poller's PollFn closure and returns the
// resolved []NATSReceivedMessage from the synthesized Event payload.
// Mirrors the production call site (case_runner -> MatchShape).
func drainViaPollFn(t *testing.T, p *NATSSubscribePoller, subject string) []readers.NATSReceivedMessage {
	t.Helper()
	events := p.PollFn("", map[string]any{"subject": subject}, "")()
	require.Len(t, events, 1, "PollFn must emit exactly one synthetic event per call")
	payload, ok := events[0].Payload.(readers.NATSSubscribePayload)
	require.True(t, ok, "Event.Payload must be NATSSubscribePayload, got %T", events[0].Payload)
	assert.Equal(t, subject, payload.Subject, "Payload.Subject mirrors the subscribed pattern")
	return payload.Received
}

// ─── Test 1: Warm + publish + PollFn → 1 event with 1 received ────

func TestNATSSubscribe_WarmPublishPollFn_SingleMessage(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.solo"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))
	publishAndFlush(t, nc, subj, []byte(`{"event":"x"}`))

	deadline := time.Now().Add(2 * time.Second)
	var got []readers.NATSReceivedMessage
	for time.Now().Before(deadline) {
		got = drainViaPollFn(t, p, subj)
		if len(got) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, got, 1)
	assert.Equal(t, subj, got[0].Subject)
	assert.Equal(t, "x", got[0].BodyJSON["event"])
}

// ─── Test 2: 3 publishes drain in publish order ───────────────────

func TestNATSSubscribe_ThreePublishes_PreserveOrder(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.order"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))

	publishAndFlush(t, nc, subj, []byte(`{"n":1}`))
	publishAndFlush(t, nc, subj, []byte(`{"n":2}`))
	publishAndFlush(t, nc, subj, []byte(`{"n":3}`))

	deadline := time.Now().Add(2 * time.Second)
	var got []readers.NATSReceivedMessage
	for time.Now().Before(deadline) {
		got = drainViaPollFn(t, p, subj)
		if len(got) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, got, 3, "all three publishes must be captured")
	assert.EqualValues(t, 1, got[0].BodyJSON["n"])
	assert.EqualValues(t, 2, got[1].BodyJSON["n"])
	assert.EqualValues(t, 3, got[2].BodyJSON["n"])
}

// ─── Test 3: wildcard subscription disambiguates per-message subject ──

func TestNATSSubscribe_WildcardSubscription_PerMessageSubjectPreserved(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const pattern = "chat.room.*.event"
	require.NoError(t, p.Warm(map[string]any{"subject": pattern}))

	publishAndFlush(t, nc, "chat.room.r-engineering.event", []byte(`{"event":"room.created"}`))
	publishAndFlush(t, nc, "chat.room.r-design.event", []byte(`{"event":"room.created"}`))

	deadline := time.Now().Add(2 * time.Second)
	var got []readers.NATSReceivedMessage
	for time.Now().Before(deadline) {
		got = drainViaPollFn(t, p, pattern)
		if len(got) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, got, 2)
	subjects := []string{got[0].Subject, got[1].Subject}
	assert.Contains(t, subjects, "chat.room.r-engineering.event")
	assert.Contains(t, subjects, "chat.room.r-design.event",
		"wildcard subscriptions must expose the actual delivery subject per message")
}

// ─── Test 4: Warm is idempotent — second call is a no-op ──────────

func TestNATSSubscribe_WarmIsIdempotent(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.idempotent"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))

	p.mu.Lock()
	entry, ok := p.cache[natsSubKey("", subj)]
	p.mu.Unlock()
	require.True(t, ok)
	require.NotNil(t, entry)

	publishAndFlush(t, nc, subj, []byte(`{"once":true}`))
	deadline := time.Now().Add(2 * time.Second)
	var got []readers.NATSReceivedMessage
	for time.Now().Before(deadline) {
		got = drainViaPollFn(t, p, subj)
		if len(got) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Len(t, got, 1, "exactly one subscription means exactly one delivery; idempotent Warm must not duplicate")
}

// ─── Test 5: PollFn before Warm → empty + warn-path ───────────────

func TestNATSSubscribe_PollFnBeforeWarm_ReturnsEmpty(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.no-warm"
	// Deliberately skip Warm — exercise the defensive PollFn-before-Warm branch.
	events := p.PollFn("", map[string]any{"subject": subj}, "")()
	assert.Empty(t, events, "PollFn before Warm must return zero events (defensive branch)")
}

// ─── Test 6: nil conn → Warm degrades, PollFn returns empty ───────

func TestNATSSubscribe_NilConn_DegradesGracefully(t *testing.T) {
	p := NewNATSSubscribePoller(nil)
	t.Cleanup(p.Close)

	// Warm with nil conn should NOT error (soft degrade per spec §3.5).
	require.NoError(t, p.Warm(map[string]any{"subject": "chat.test.nil-conn"}))

	events := p.PollFn("", map[string]any{"subject": "chat.test.nil-conn"}, "")()
	assert.Empty(t, events, "nil conn must produce zero events (nil-tolerant degrade)")
}

// ─── Test 7: missing args.subject → Warm errors ────────────────────

func TestNATSSubscribe_MissingSubject_WarmErrors(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	err := p.Warm(map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subject is required")

	err = p.Warm(map[string]any{"subject": ""})
	require.Error(t, err)
}

// ─── Test 8: buffer cap exceeded — overflow drops with warn ───────

func TestNATSSubscribe_BufferFull_DropsExcessWithWarn(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.overflow"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))

	// Publish more than the buffer cap. The handler's drop branch
	// fires on the (cap+1)th and beyond.
	overflow := readers.MaxQueueDepthForTesting() + 20
	for i := 0; i < overflow; i++ {
		require.NoError(t, nc.Publish(subj, []byte(`{"n":1}`)))
	}
	require.NoError(t, nc.Flush())

	// Settle: give the NATS delivery goroutine time to push everything
	// it can. The drop branch silently caps at maxQueueDepth.
	time.Sleep(150 * time.Millisecond)

	got := drainViaPollFn(t, p, subj)
	assert.LessOrEqual(t, len(got), readers.MaxQueueDepthForTesting(),
		"queue must never exceed the configured cap")
	assert.GreaterOrEqual(t, len(got), 1,
		"at least one message must land before the cap fires")
}

// ─── Test 9: Close releases subscription + no goroutine leak ──────

func TestNATSSubscribe_Close_NoGoroutineLeak(t *testing.T) {
	// IgnoreCurrent baseline: snapshot every goroutine alive BEFORE
	// Warm + publish + Close so the assertion only flags goroutines
	// the poller itself created and failed to clean up. nats-server
	// and nats.Conn keep many long-lived background loops that aren't
	// our concern.
	nc := startInProcessNATS(t)
	baseline := goleak.IgnoreCurrent()
	defer goleak.VerifyNone(t, baseline)

	p := NewNATSSubscribePoller(nc)
	const subj = "chat.test.cleanup"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))
	publishAndFlush(t, nc, subj, []byte(`{"x":1}`))
	time.Sleep(50 * time.Millisecond)

	p.Close()
	// Cache cleared, subscription unsubscribed. Subsequent Warm
	// re-opens a fresh subscription (proved by the lookup miss).
	p.mu.Lock()
	_, present := p.cache[natsSubKey("", subj)]
	p.mu.Unlock()
	assert.False(t, present, "Close must clear the cache")
}

// ─── Test 10: JSON-decodable payload → BodyJSON populated ─────────

func TestNATSSubscribe_JSONPayload_PopulatesBodyJSON(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.json"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))
	publishAndFlush(t, nc, subj, []byte(`{"event":"room.created","roomId":"r-x"}`))

	deadline := time.Now().Add(2 * time.Second)
	var got []readers.NATSReceivedMessage
	for time.Now().Before(deadline) && len(got) == 0 {
		got = drainViaPollFn(t, p, subj)
		if len(got) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Len(t, got, 1)
	assert.Equal(t, "room.created", got[0].BodyJSON["event"])
	assert.Equal(t, "r-x", got[0].BodyJSON["roomId"])
	assert.Empty(t, got[0].BodyRaw, "BodyRaw must be empty when BodyJSON is set")
}

// ─── Test 11: Non-JSON payload → BodyRaw populated ────────────────

func TestNATSSubscribe_NonJSONPayload_PopulatesBodyRaw(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.raw"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))
	publishAndFlush(t, nc, subj, []byte(`plain text payload`))

	deadline := time.Now().Add(2 * time.Second)
	var got []readers.NATSReceivedMessage
	for time.Now().Before(deadline) && len(got) == 0 {
		got = drainViaPollFn(t, p, subj)
		if len(got) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Len(t, got, 1)
	assert.Equal(t, "plain text payload", got[0].BodyRaw)
	assert.Nil(t, got[0].BodyJSON, "BodyJSON must be nil when payload isn't valid JSON")
}

// ─── Test 12: Header propagation ──────────────────────────────────

func TestNATSSubscribe_HeaderPropagation(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.headers"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))

	msg := nats.NewMsg(subj)
	msg.Data = []byte(`{}`)
	msg.Header = nats.Header{}
	msg.Header.Set("X-Request-Id", "01970000-0000-7000-8000-000000000099")
	require.NoError(t, nc.PublishMsg(msg))
	require.NoError(t, nc.Flush())

	deadline := time.Now().Add(2 * time.Second)
	var got []readers.NATSReceivedMessage
	for time.Now().Before(deadline) && len(got) == 0 {
		got = drainViaPollFn(t, p, subj)
		if len(got) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Len(t, got, 1)
	assert.Equal(t, []string{"01970000-0000-7000-8000-000000000099"},
		got[0].Header["X-Request-Id"], "header must be deep-copied verbatim")
}

// ─── Test 13: monotonic accumulation across PollFn calls ──────────

func TestNATSSubscribe_MonotonicAccumulationAcrossPollFnCalls(t *testing.T) {
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	const subj = "chat.test.monotonic"
	require.NoError(t, p.Warm(map[string]any{"subject": subj}))

	publishAndFlush(t, nc, subj, []byte(`{"n":1}`))
	deadline := time.Now().Add(2 * time.Second)
	var first []readers.NATSReceivedMessage
	for time.Now().Before(deadline) && len(first) < 1 {
		first = drainViaPollFn(t, p, subj)
		if len(first) < 1 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Len(t, first, 1)

	// Publish a second message; the second PollFn call must return
	// BOTH the first call's event AND the new one. This is the
	// crucial property that lets Gomega's Eventually loop see the
	// observation window grow until the matcher is satisfied.
	publishAndFlush(t, nc, subj, []byte(`{"n":2}`))
	deadline = time.Now().Add(2 * time.Second)
	var second []readers.NATSReceivedMessage
	for time.Now().Before(deadline) && len(second) < 2 {
		second = drainViaPollFn(t, p, subj)
		if len(second) < 2 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Len(t, second, 2, "second PollFn must include the first message + the new one")
	assert.EqualValues(t, 1, second[0].BodyJSON["n"])
	assert.EqualValues(t, 2, second[1].BodyJSON["n"])
}

// ─── Test 14: cross-case isolation — same subject reused, both see buffer ──

func TestNATSSubscribe_SharedSubscriptionAcrossArgs(t *testing.T) {
	// Cache shape is per-subject. A second Warm with the same subject
	// is idempotent (covered by Test 4); a second Warm with a
	// DIFFERENT subject opens a separate subscription. Verify the
	// distinct-subject path here so the cache shape is fully pinned.
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc)
	t.Cleanup(p.Close)

	require.NoError(t, p.Warm(map[string]any{"subject": "chat.test.subjA"}))
	require.NoError(t, p.Warm(map[string]any{"subject": "chat.test.subjB"}))

	publishAndFlush(t, nc, "chat.test.subjA", []byte(`{"label":"A"}`))
	publishAndFlush(t, nc, "chat.test.subjB", []byte(`{"label":"B"}`))

	deadline := time.Now().Add(2 * time.Second)
	var gotA, gotB []readers.NATSReceivedMessage
	for time.Now().Before(deadline) && (len(gotA) < 1 || len(gotB) < 1) {
		gotA = drainViaPollFn(t, p, "chat.test.subjA")
		gotB = drainViaPollFn(t, p, "chat.test.subjB")
		if len(gotA) < 1 || len(gotB) < 1 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Len(t, gotA, 1)
	require.Len(t, gotB, 1)
	assert.Equal(t, "A", gotA[0].BodyJSON["label"])
	assert.Equal(t, "B", gotB[0].BodyJSON["label"])
}

// ─── Bonus: confirm Warmer compile-time interface ──────────────────

func TestNATSSubscribe_ImplementsWarmer(t *testing.T) {
	var _ Warmer = (*NATSSubscribePoller)(nil)
	var _ Poller = (*NATSSubscribePoller)(nil)
	// Compile-time only; no runtime assertion needed.
	_ = sync.Mutex{}
}

// --- P1: per-credential subscribe ---
// nats_subscribe with args.credential: {jwt, nkey, account} opens a
// new NATS connection authenticated as that user, instead of the
// shared admin connection. Different credentials get separate cache
// entries so subscriptions don't cross-contaminate.

// fakeConnOpener captures the credential each call asked for and
// returns the same in-process conn (we're testing the cache + dispatch
// logic, not real-NATS auth which the suite verifies end-to-end).
type fakeConnOpener struct {
	mu          sync.Mutex
	calls       []credCall
	returnConn  *nats.Conn
	returnError error
}

type credCall struct {
	Site, Account, JWT, NkeySeed string
}

func (f *fakeConnOpener) Open(site, account, jwt, nkeySeed string) (*nats.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, credCall{site, account, jwt, nkeySeed})
	if f.returnError != nil {
		return nil, f.returnError
	}
	return f.returnConn, nil
}

func TestNATSSubscribe_NoCredentialInArgs_UsesSharedAdminConn(t *testing.T) {
	// Regression: existing scenarios that don't carry args.credential
	// continue to use the shared admin conn. No call to the opener.
	nc := startInProcessNATS(t)
	opener := &fakeConnOpener{returnConn: nc}

	p := NewNATSSubscribePoller(nc)
	p.SetConnOpener(opener.Open)
	require.NoError(t, p.Warm(map[string]any{"subject": "test.x"}))

	assert.Empty(t, opener.calls, "no credential ⇒ no per-cred opener call; shared conn reused")
}

func TestNATSSubscribe_WithCredentialInArgs_OpensPerCredConn(t *testing.T) {
	nc := startInProcessNATS(t)
	opener := &fakeConnOpener{returnConn: nc}

	p := NewNATSSubscribePoller(nc)
	p.SetConnOpener(opener.Open)
	cred := map[string]any{
		"account": "bob",
		"jwt":     "eyJ.bob",
		"nkey":    "SUAH.bob",
	}
	require.NoError(t, p.Warm(map[string]any{
		"subject":    "chat.user.bob.event.room",
		"credential": cred,
	}))

	require.Len(t, opener.calls, 1)
	assert.Equal(t, "bob", opener.calls[0].Account)
	assert.Equal(t, "eyJ.bob", opener.calls[0].JWT)
	assert.Equal(t, "SUAH.bob", opener.calls[0].NkeySeed)
}

func TestNATSSubscribe_DifferentCredentialsGetSeparateConns(t *testing.T) {
	nc := startInProcessNATS(t)
	opener := &fakeConnOpener{returnConn: nc}

	p := NewNATSSubscribePoller(nc)
	p.SetConnOpener(opener.Open)
	bob := map[string]any{"account": "bob", "jwt": "j-bob", "nkey": "n-bob"}
	alice := map[string]any{"account": "alice", "jwt": "j-alice", "nkey": "n-alice"}

	require.NoError(t, p.Warm(map[string]any{"subject": "chat.x", "credential": bob}))
	require.NoError(t, p.Warm(map[string]any{"subject": "chat.x", "credential": alice}))

	require.Len(t, opener.calls, 2, "same subject + different creds ⇒ two opener calls")
	assert.Equal(t, "bob", opener.calls[0].Account)
	assert.Equal(t, "alice", opener.calls[1].Account)
}

func TestNATSSubscribe_SameCredentialRepeatedWarm_OpensOnce(t *testing.T) {
	// Idempotency: warming the same (subject, credential) twice opens
	// the per-cred conn exactly once — matches the no-credential idempotency.
	nc := startInProcessNATS(t)
	opener := &fakeConnOpener{returnConn: nc}

	p := NewNATSSubscribePoller(nc)
	p.SetConnOpener(opener.Open)
	cred := map[string]any{"account": "bob", "jwt": "j", "nkey": "n"}
	require.NoError(t, p.Warm(map[string]any{"subject": "chat.x", "credential": cred}))
	require.NoError(t, p.Warm(map[string]any{"subject": "chat.x", "credential": cred}))

	assert.Len(t, opener.calls, 1, "repeat Warm with same (subject, cred) ⇒ single opener call")
}

func TestNATSSubscribe_CredentialAuthFailure_SurfacesError(t *testing.T) {
	// A failing conn open propagates so the scenario sees the auth
	// failure as a verdict (it might be ASSERTING auth fails).
	nc := startInProcessNATS(t)
	opener := &fakeConnOpener{returnError: assertErr("auth failure: not authorized")}

	p := NewNATSSubscribePoller(nc)
	p.SetConnOpener(opener.Open)
	cred := map[string]any{"account": "evil", "jwt": "bad", "nkey": "bad"}
	err := p.Warm(map[string]any{"subject": "chat.x", "credential": cred})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth failure")
}

func TestNATSSubscribe_NoConnOpenerSet_CredentialInArgsErrors(t *testing.T) {
	// Defense: if the poller was built without a ConnOpener (legacy
	// path) but args.credential is set, Warm returns an explicit error
	// instead of silently falling back to the admin conn (which would
	// invalidate the auth-boundary test the author was trying to write).
	nc := startInProcessNATS(t)
	p := NewNATSSubscribePoller(nc) // no SetConnOpener
	cred := map[string]any{"account": "bob", "jwt": "j", "nkey": "n"}
	err := p.Warm(map[string]any{"subject": "chat.x", "credential": cred})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential")
}

// assertErr is a tiny error type for tests where errors.New / fmt.Errorf
// would add an import without value.
type assertErrString string

func (e assertErrString) Error() string { return string(e) }
func assertErr(s string) error          { return assertErrString(s) }
