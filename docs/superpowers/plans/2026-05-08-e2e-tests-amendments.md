# E2E Test Plan — Review-Driven Amendments

> **Read alongside** `docs/superpowers/plans/2026-05-08-e2e-tests.md`. The original plan stands; this document amends specific chapters in light of the 14-reviewer consolidated findings. Every amendment cross-references the chapter it modifies. Implementers should read the original chapter first, then apply the amendment.

**Scope of this revision:** every reviewer-flagged BLOCKER and HIGH-severity issue. Architecture concerns and coverage gaps (encryption, edit/delete, threads, mentions, trace continuity) are deliberately left for follow-up plans — they do not block the v1 suite.

**Decisions locked in this revision:**
- Federation scope: chapter 12 narrows to **subscription/role federation only** (no cross-site MESSAGES_CANONICAL — confirmed by user; matches what the system actually does today).
- OIDC issuer alignment: use `host.docker.internal` everywhere (Keycloak `KC_HOSTNAME`, auth-service `OIDC_ISSUER_URL`, harness Keycloak base URL). Resolves the iss-claim mismatch without modifying any service code.
- JetStream domains: **drop `jetstream.domain` from chapter 2** (the existing two-site fixture in `broadcast-worker/deploy/test/nats/` works without domains). Federation Sources work transparently across gateway-federated clusters when no domain is set; adding `External.APIPrefix` is no longer needed.
- Auth-service OIDC contract: chapter 8's `Authenticate` sends only `{ssoToken, natsPublicKey}` — `account` is dropped from the request body (server derives it from `preferred_username`).
- Polling helpers: standardize on `require.Eventually` and a single `awaitCanonicalAcked(t, js, durable, msgID)` probe-and-drain helper.

---

## Amendment to Chapter 2 — drop `jetstream.domain`

**Why:** explicit JS domains require `External.APIPrefix` on cross-cluster Sources, but the working two-site fixture in `broadcast-worker/deploy/test/nats/` omits domains and federates fine. Use that proven shape.

**Edits to `nats-a.conf`:**

```hocon
jetstream {
  store_dir: /data/jetstream
  # NO `domain:` directive. Cross-cluster Sources resolve via the gateway.
}
```

**Same edit on `nats-b.conf`.** Background section in chapter 2 ("Background — why gateways, not leaf nodes") is correct and stays. The "Notes that matter" bullet about `jetstream.domain` is removed.

---

## Amendment to Chapter 3 — Cassandra schema init job, Keycloak healthcheck, Cassandra heap, Keycloak hostname

### 3.A — Cassandra schema init via cqlsh one-shot job (BLOCKER B8)

**Why:** the official `cassandra:5` image has **no init-script hook**. Mounting `docker-entrypoint-initdb.d` does nothing. `docker-local/compose.deps.yaml` already solves this with a profile-gated `cassandra-init` job; mirror that pattern.

**Edit `cass-a` block:** remove the `../cassandra/init:/docker-entrypoint-initdb.d:ro` mount.

**Add a new init-only service:**

```yaml
  cassandra-init-a:
    image: ${E2E_CASS_IMAGE:-cassandra:5}
    container_name: cassandra-init-a
    networks: [chat-e2e]
    depends_on:
      cass-a: { condition: service_healthy }
    volumes:
      - ../cassandra/init:/init:ro
    profiles: [init]
    entrypoint: ["/bin/sh", "-c"]
    command:
      - |
        set -eu
        for f in $$(ls /init/*.cql | sort); do
          echo "[cassandra-init-a] applying $$f"
          cqlsh cass-a -f "$$f"
        done
        echo "[cassandra-init-a] done"
    restart: "no"
```

**Add the symmetric `cassandra-init-b`** in chapter 5 with `cass-b` everywhere.

**Update Makefile (chapter 1 amendment, applied here):**

```makefile
e2e-up: $(E2E_ENV) $(E2E_SECRETS)
	docker compose -f $(E2E_COMPOSE) up -d --wait
	docker compose -f $(E2E_COMPOSE) --profile init run --rm cassandra-init-a
	docker compose -f $(E2E_COMPOSE) --profile init run --rm cassandra-init-b
```

`make e2e-down -v` already drops the volumes; on the next `e2e-up` the init job re-runs because `cass-a-data` is fresh. Idempotent across cycles.

### 3.B — Keycloak healthcheck (BLOCKER B9)

`/dev/tcp` is bash-only; the Keycloak 26 Alpine image is `sh`. Replace:

```yaml
    healthcheck:
      test: ["CMD-SHELL", "curl -fs http://localhost:9000/health/ready | grep -q '\"status\": \"UP\"'"]
      interval: 5s
      timeout: 5s
      retries: 60
      start_period: 30s
```

`curl` ships in the keycloak image. Same edit for `keycloak-b`.

### 3.C — Cassandra heap cap (HIGH H7)

Cassandra defaults to ~1/4 host RAM (≈8GB on a 32GB box, ≈16GB across two sites + 2× ES + 2× Mongo + 2× Keycloak → swap). Add to **both** `cass-a` and `cass-b`:

```yaml
    environment:
      # ... existing entries ...
      JVM_OPTS: "-Xms512m -Xmx1g"
      MAX_HEAP_SIZE: "1G"
      HEAP_NEWSIZE: "256M"
```

### 3.D — Keycloak hostname for OIDC issuer alignment (BLOCKER B7)

**Problem:** auth-service-a uses `pkg/oidc.NewValidator` which strict-compares the JWT `iss` claim against `OIDC_ISSUER_URL`. Whatever URL Keycloak considers its hostname becomes `iss`. Container-to-container (`http://keycloak-a:8080`) and host-to-container (`http://localhost:8180`) URLs differ → tokens don't validate.

**Fix:** use `host.docker.internal` as the Keycloak hostname on both sides (containers via Docker's host-gateway alias; host machine via Docker Desktop's auto-mapping or a one-time `/etc/hosts` entry on plain Linux Docker Engine).

**Edit `keycloak-a` block:**

```yaml
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: admin
      KC_BOOTSTRAP_ADMIN_PASSWORD: admin
      KC_HEALTH_ENABLED: "true"
      KC_HTTP_ENABLED: "true"
      KC_HOSTNAME: "http://host.docker.internal:8180"
      KC_HOSTNAME_STRICT: "false"
      KC_HOSTNAME_BACKCHANNEL_DYNAMIC: "false"
```

**Edit `keycloak-b`:** identical except `KC_HOSTNAME: "http://host.docker.internal:8181"`.

**Add a one-time host check to `setup-e2e.sh`** (chapter 4 amendment) so non-Docker-Desktop users get a clear error if `host.docker.internal` doesn't resolve. The harness assumes it does; setup-e2e.sh prints a remediation line if not.

```bash
if ! getent hosts host.docker.internal >/dev/null 2>&1; then
  echo "[setup-e2e] WARNING: host.docker.internal does not resolve on this machine."
  echo "  On Docker Desktop (Mac/Win) it's automatic. On Linux Docker Engine, run:"
  echo "    echo '127.0.0.1 host.docker.internal' | sudo tee -a /etc/hosts"
fi
```

---

## Amendment to Chapter 4 — setup-e2e.sh rewrite, NATS auth wiring, OTEL/search/host-gateway env

### 4.A — Rewrite `setup-e2e.sh` using proven extraction from `docker-local/setup.sh` (BLOCKERS B1, B2)

**Why:** the original chapter-4 script invents an nsc key store path (`keys/keys/A/*/A*.nk`) that doesn't exist, and uses `nats.signing_keys.0` (the signing key public) where `resolver_preload` requires the account's identity (`sub` field). The repo's existing `docker-local/setup.sh` extracts both correctly.

**Replace the whole `setup-e2e.sh`** with a fork of `docker-local/setup.sh` adjusted to:
- Output to `docker-local/e2e/secrets/` instead of `docker-local/`
- Generate ALSO a `auth.conf` fragment for `$include` from each NATS config
- Generate ALSO a `e2e-secrets.env` fragment for the `auth-service-a` / `auth-service-b` `env_file` directive
- Idempotent: skip if `secrets/operator.jwt` exists

The reference logic to copy (verbatim, not paraphrased) is in `/home/user/chat/docker-local/setup.sh` lines 30–95: the block that runs `nats-box` with `nsc add operator … nsc add account … nsc describe operator/account/SYS --raw`, then on the host extracts `OPERATOR_PUB_KEY`, `ACCOUNT_PUB_KEY`, `ACCOUNT_SEED`, `SYS_PUB_KEY` via `nsc env --field`, `nsc describe --field sub`, and globbing `~/.local/share/nats/nsc/keys/keys/<type>/<2-char>/<key>.nk`. The proven path is `keys/keys/<type>/<2-char-prefix>/<full-pubkey>.nk` — NOT `keys/keys/A/*/A*.nk`.

After extraction, the script writes `auth.conf` exactly as `docker-local/setup.sh` writes `nats.conf`'s `operator + resolver + resolver_preload + system_account` block:

```bash
cat > "$SECRETS_DIR/auth.conf" <<EOF
operator: ${OPERATOR_JWT}
system_account: ${SYS_PUB_KEY}
resolver: MEMORY
resolver_preload {
  ${ACCOUNT_PUB_KEY}: ${ACCOUNT_JWT}
  ${SYS_PUB_KEY}: ${SYS_JWT}
}
EOF
```

Note the addition of `system_account: ${SYS_PUB_KEY}` — required for operator-mode JetStream to function (closes the BLOCKER B2 concern about operator-mode NATS conflicts).

And `e2e-secrets.env`:

```bash
cat > "$SECRETS_DIR/e2e-secrets.env" <<EOF
AUTH_SIGNING_KEY=${ACCOUNT_SEED}
EOF
chmod 600 "$SECRETS_DIR/e2e-secrets.env" "$SECRETS_DIR/backend.creds"
```

The host-side `host.docker.internal` warning from amendment 3.D is appended at the bottom.

### 4.B — Site A service env amendments

Apply these blanket edits to **every** chapter-4 service block:

```yaml
    environment:
      # ... existing entries ...
      OTEL_SDK_DISABLED: "true"   # H3: prevents fatal init / span buffering
```

`auth-service-a` additionally:

```yaml
    environment:
      # ... existing ...
      OIDC_ISSUER_URL: http://host.docker.internal:8180/realms/chatapp   # B7
    extra_hosts:
      - "host.docker.internal:host-gateway"   # B7: auth-service-a reaches keycloak via the host gateway
```

`search-service-a` additionally:

```yaml
    environment:
      # ... existing ...
      SEARCH_USER_ROOM_INDEX: user-room-siteA   # H4: aligns with sync-worker's per-siteID write target
```

`search-sync-worker-a` additionally — set the ES index template at startup so the test-side refresh interval is fast. Easiest place is a one-shot init job analogous to `cassandra-init-a`:

```yaml
  search-init-a:
    image: curlimages/curl:8
    container_name: search-init-a
    networks: [chat-e2e]
    depends_on:
      es-a: { condition: service_healthy }
    profiles: [init]
    restart: "no"
    entrypoint: ["/bin/sh", "-c"]
    command:
      - |
        set -e
        curl -fsS -X PUT http://es-a:9200/_index_template/messages-e2e \
          -H 'Content-Type: application/json' \
          -d '{
            "index_patterns": ["messages-siteA-v1-*", "messages-siteB-v1-*"],
            "template": { "settings": { "index": { "refresh_interval": "200ms" } } },
            "priority": 200
          }'
        echo "[search-init-a] template applied"
```

Add to the `e2e-up` Makefile target:

```makefile
	docker compose -f $(E2E_COMPOSE) --profile init run --rm search-init-a
```

(One init job is enough — the template targets both siteA and siteB index patterns and is cluster-local to es-a; site B's es-b needs its own job.)

### 4.C — NATS server config: keep `include "/etc/nats/auth.conf"` syntax

Confirmed: NATS 2.x uses `include` (no `$`). The chapter-2 configs as amended (no `jetstream.domain`) plus `include "/etc/nats/auth.conf"` (which now contains operator + system_account + resolver + resolver_preload) form a valid operator-mode NATS config. Verify on first run with `docker logs nats-a 2>&1 | grep -E 'jetstream|gateway|operator'` showing no error.

---

## Amendment to Chapter 5 — apply chapter 4 amendments to site B

Mirror every chapter-4 amendment for the `-b` services:
- `OTEL_SDK_DISABLED=true` on every service block
- `auth-service-b`: `OIDC_ISSUER_URL=http://host.docker.internal:8181/realms/chatapp`, `extra_hosts: ["host.docker.internal:host-gateway"]`
- `search-service-b`: `SEARCH_USER_ROOM_INDEX=user-room-siteB`
- Add `cassandra-init-b` (chapter 3.A)
- Add `search-init-b` analogous to `search-init-a` but pointing at `http://es-b:9200`. Make sure both init jobs run via the e2e-up Makefile target.

---

## Amendment to Chapter 7 — `composeFilePath` walks `os.Getwd()` for `go.mod`

**Why:** `runtime.Caller(0)` returns the source file path captured at compile time. Under `-trimpath` or when the test binary moves between machines, the path resolves to a non-existent location.

**Replace `composeFilePath`:**

```go
func composeFilePath() (string, error) {
	if env := os.Getenv("E2E_COMPOSE_FILE"); env != "" {
		return env, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			path := filepath.Join(dir, "docker-local", "e2e", "compose.e2e.yaml")
			if _, err := os.Stat(path); err != nil {
				return "", fmt.Errorf("compose file %s: %w", path, err)
			}
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find go.mod walking up from cwd; set E2E_COMPOSE_FILE")
		}
		dir = parent
	}
}
```

The `runtime` and `path/filepath` imports stay; `runtime.Caller` is no longer used.

---

## Amendment to Chapter 8 — `/auth` request shape + dead-import cleanup

### 8.A — `/auth` request only carries `{ssoToken, natsPublicKey}`

`auth-service/handler.go` derives `account` from `claims.PreferredUsername`. The `account` field in chapter 8's `Authenticate` POST body is silently ignored. Drop it:

```go
SetBody(map[string]string{
    "ssoToken":      ssoToken,
    "natsPublicKey": pub,
}).
```

The chapter's "alice"/"bob" account names still flow correctly because they're the `preferred_username` in `realm-export.json`.

### 8.B — Remove the `var _ = fmt.Sprintf` tombstone

The chapter-8 snippet ends with a tombstone declaration assuming `fmt` is imported but unused. Once the implementer removes the dead `fmt` import the tombstone disappears too.

### 8.C — Keycloak base URL uses `host.docker.internal`

`siteA.KeycloakURL` (chapter 7 endpoints) becomes `http://host.docker.internal:8180`; `siteB.KeycloakURL` becomes `http://host.docker.internal:8181`. The harness uses these to mint SSO tokens whose `iss` claim now matches what `auth-service-a` / `-b` expect.

---

## Amendment to Chapter 9 — Bootstrap OUTBOX streams + drop `External.APIPrefix`

### 9.A — Harness ensures OUTBOX streams exist before federation wiring (BLOCKER B11)

**Why:** No service in the repo bootstraps `OUTBOX_{siteID}` despite room-worker being the primary publisher. Chapter 9's `BootstrapFederation` calls `UpdateStream` on `INBOX_siteB` with `Sources: [{Name: "OUTBOX_siteA"}]` — this fails at the JetStream level if the source stream doesn't exist (or messages get queued waiting for it).

**Add to `harness/federation.go` before `updateInbox`:**

```go
// ensureOutbox creates OUTBOX_{siteID} on the given site if it doesn't exist.
// The harness owns this stream's existence in e2e (no service bootstraps it).
// Schema (Name + Subjects) only — federation is layered separately.
func ensureOutbox(ctx context.Context, site SiteEndpoints) error {
	conn, err := nats.Connect(site.NATSURL, nats.UserCredentials(site.NATSCredsFile))
	if err != nil {
		return fmt.Errorf("connect %s: %w", site.NATSURL, err)
	}
	defer conn.Close()

	js, err := jetstream.New(conn)
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}

	cfg := stream.Outbox(site.SiteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     cfg.Name,
		Subjects: cfg.Subjects,
	})
	if err != nil {
		return fmt.Errorf("create/update %s: %w", cfg.Name, err)
	}
	return nil
}
```

Call sequence in `BootstrapFederation`:

```go
if err := ensureOutbox(ctx, stack.SiteA); err != nil { return ... }
if err := ensureOutbox(ctx, stack.SiteB); err != nil { return ... }
if err := updateInbox(ctx, stack.SiteA, stack.SiteB.SiteID); err != nil { return ... }
if err := updateInbox(ctx, stack.SiteB, stack.SiteA.SiteID); err != nil { return ... }
```

### 9.B — No `External.APIPrefix` needed (because chapter 2 dropped `jetstream.domain`)

The chapter-9 `updateInbox` `StreamSource` stays as-is. The amendment to chapter 2 (drop domains) closes BLOCKER B10 without code change here.

---

## Amendment to Chapter 10 — Subject/type audit, gatekeeper request/reply contract, polling

### 10.A — Real subject builders and request types (BLOCKER B3)

The chapter-10 illustrative names are wrong. Replace before any test code lands:

| Plan reference | Actual symbol |
|---|---|
| `subject.MessageSend(account, roomID, siteID)` | `subject.MsgSend(account, roomID, siteID)` |
| `subject.RoomCreate(account, siteID)` | `subject.RoomCreate(account, siteID)` ✓ correct |
| `subject.RoomAddMember(account, siteID)` | `subject.MemberAdd(account, roomID, siteID)` (note: roomID required) |
| `subject.RoomBroadcastWildcard(siteID)` | `subject.RoomEventWildcard()` (no siteID arg; subject is `chat.room.*.event`) |
| `subject.LoadHistory(account, roomID, siteID)` | construct via `fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.history", ...)`; `pkg/subject` only exposes `MsgHistoryPattern` (handler-side). Suggest adding a `MsgHistory(account, roomID, siteID)` builder in a tiny prereq PR; otherwise inline the format string. |
| `model.AddMemberRequest{RoomID, MemberAccount}` | `model.AddMembersRequest{Users []string}` (plural) — payload becomes `{Users: []string{bob.Account}}`; RoomID is in the subject |
| `model.CreateRoomRequest{ID, Type, Name}` | `model.CreateRoomRequest{Name, Users, ...}` — no ID, no Type. Server returns `model.CreateRoomReply{RoomID}`; the test must read the reply for the actual room ID and use that for subsequent calls |
| `model.SendMessageRequest{RoomID, MessageID, Body}` | `model.SendMessageRequest{ID, Content, RequestID, ...}` — `RequestID` is **mandatory** (see 10.B) |
| `model.LoadHistoryRequest{RoomID}` | `model.LoadHistoryRequest{Before *int64, Limit int}` — RoomID is in subject |
| `model.BroadcastMessage` | `model.RoomEvent` (with `Type=RoomEventNewMessage` and `Message` populated) |

`ids.RoomID = idgen.GenerateID()` is no longer assigned by the test — it's read from `CreateRoomReply.RoomID` after the create call. `TestIDs` in `harness/ids.go` (chapter 8) drops the eager `RoomID` mint in favor of a lazy assigner.

### 10.B — message-gatekeeper uses async out-of-band reply (BLOCKER B5)

Sending a message is **not** classic `nc.Request`. The gatekeeper publishes the reply on `subject.UserResponse(account, requestID)`. Test code must:

1. Generate a requestID (`idgen.GenerateRequestID()` or `nats.NewInbox()` — anything unique).
2. Subscribe to `subject.UserResponse(alice.Account, requestID)` BEFORE publishing.
3. Publish (NOT `nc.Request`) to the send subject; the gatekeeper consumes via JetStream.
4. Read the reply from the response subscription.

Replace `requestReply` (chapter 10's helper) with a more honest `sendAndAwaitReply`:

```go
// sendAndAwaitReply publishes a JetStream-captured message-send request and
// waits for the gatekeeper's async reply on subject.UserResponse(account, requestID).
// Used only for the msg.send subject (which is JS-published + async-replied).
// For classic NATS request/reply (room.create, member.add, history, etc.),
// requestReply (the original helper) is correct.
func sendAndAwaitReply(t *testing.T, conn *nats.Conn, account, requestID, sendSubj string, payload any, timeout time.Duration) error {
	t.Helper()
	respSub, err := conn.SubscribeSync(subject.UserResponse(account, requestID))
	require.NoError(t, err)
	defer respSub.Unsubscribe()

	body, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, conn.Publish(sendSubj, body))

	msg, err := respSub.NextMsg(timeout)
	if err != nil {
		return fmt.Errorf("await msg.send reply: %w", err)
	}
	var errResp model.ErrorResponse
	if err := json.Unmarshal(msg.Data, &errResp); err == nil && errResp.Error != "" {
		return fmt.Errorf("gatekeeper rejected: %s", errResp.Error)
	}
	return nil
}
```

Keep `requestReply` from the original chapter for room/member/history handlers.

### 10.C — Replace `awaitConsumerReady` with probe-and-drain (HIGH H2)

`NumWaiting > 0` is racy and doesn't filter by durable. Replace with `awaitDurableReady` that polls for the named durable's existence, plus a probe-and-drain helper for the actual message-arrival assertion downstream.

```go
// awaitDurableReady polls until a named durable consumer exists on a stream.
// Confirms the worker's startup completed CreateOrUpdateConsumer; does NOT
// confirm the worker has parked on a fetch. For send-and-wait-for-acked
// behavior, follow up with awaitCanonicalAcked.
func awaitDurableReady(t *testing.T, ctx context.Context, js jetstream.JetStream, streamName, durable string) {
	t.Helper()
	require.Eventually(t, func() bool {
		s, err := js.Stream(ctx, streamName)
		if err != nil {
			return false
		}
		_, err = s.Consumer(ctx, durable)
		return err == nil
	}, 20*time.Second, 200*time.Millisecond, "durable %q on %s", durable, streamName)
}

// awaitCanonicalAcked waits until the named durable's AckFloor reaches
// publishSeq. Use after publishing a known event to confirm the worker has
// processed it (e.g. after MsgSend, wait for message-worker to ack before
// reading from Cassandra via LoadHistory).
func awaitCanonicalAcked(t *testing.T, ctx context.Context, js jetstream.JetStream, streamName, durable string, publishSeq uint64) {
	t.Helper()
	require.Eventually(t, func() bool {
		s, err := js.Stream(ctx, streamName)
		if err != nil {
			return false
		}
		c, err := s.Consumer(ctx, durable)
		if err != nil {
			return false
		}
		info, err := c.Info(ctx)
		if err != nil {
			return false
		}
		return info.AckFloor.Stream >= publishSeq
	}, 15*time.Second, 100*time.Millisecond, "%s ack floor >= %d on %s", durable, publishSeq, streamName)
}
```

### 10.D — Use `awaitCanonicalAcked` to fix the LoadHistory race (HIGH H1)

The chapter-10 happy-path test:

1. Get the canonical stream's pre-publish `LastSequence`.
2. `sendAndAwaitReply` (publishes and waits for the gatekeeper response).
3. Call `awaitCanonicalAcked(t, ctx, js, "MESSAGES_CANONICAL_siteA", "message-worker", lastSeq+1)`.
4. THEN call LoadHistory.

This closes the race that previously made step 6's `Len == 1` flaky.

### 10.E — Test reads roomID from reply, not from harness IDs

`harness.NewTestIDs(t)` no longer pre-mints a roomID (server-generated). Test reads `CreateRoomReply.RoomID` after `RoomCreate` and threads it through subsequent calls.

---

## Amendment to Chapter 11 — Subject fixes, DM idempotency, bad-JWT scope down

### 11.A — Search subject is `.request.search.messages`, not `.request.search` (HIGH H4 subject part)

`awaitSearchHit` in the chapter-11 search test polls `chat.user.{account}.request.search`. The actual subject is `chat.user.{account}.request.search.messages` (`pkg/subject/subject.go`). Use `subject.SearchMessages(account)` (verify exact name on implementation; otherwise inline the format string). Same `requestReply` helper, just the right subject.

### 11.B — DM idempotency assertion handles the ErrorResponse path (HIGH H9)

room-service replies to a duplicate DM with `model.ErrorResponse{Error: "dm already exists", RoomID: existingRoomID}` — that's the **error** reply path, not a normal success. Update the test:

```go
// First create.
var reply1 model.CreateRoomReply
require.NoError(t, requestReply(alice.Conn(), subject.RoomCreate(alice.Account, site.SiteID), createReq, 5*time.Second, &reply1))

// Second create — must return ErrorResponse with the same RoomID.
err := requestReply(alice.Conn(), subject.RoomCreate(alice.Account, site.SiteID), createReq, 5*time.Second, &model.CreateRoomReply{})
require.Error(t, err)
require.Contains(t, err.Error(), "dm already exists")
// requestReply needs to expose the underlying ErrorResponse.RoomID; either
// modify it to attach via errors.As or split into requestRawReply that returns
// the raw bytes for the test to parse out RoomID.
```

Lowest-friction approach: change `requestReply` to return `(*model.ErrorResponse, error)` so the test can read `RoomID` off the response. Updated signature percolates to chapter-10 callers (small change).

### 11.C — Channel create requires non-empty member list (HIGH H8)

room-service rejects channel-create with no Users/Orgs/Channels (`errEmptyCreateRequest`). The chapter-10 channel-create call must pass at least `Users: [bob.Account]`:

```go
createReq := model.CreateRoomRequest{
    Name:  "e2e-" + t.Name(),
    Users: []string{bob.Account},
}
```

This also makes the subsequent `MemberAdd` call to invite bob redundant — drop it. The test now: alice creates channel with bob → alice sends → bob receives broadcast.

### 11.D — Drop the malformed-payload error-reply assertion (BLOCKER B5 sub-issue)

On invalid JSON, message-gatekeeper logs and acks without replying (it can't extract RequestID from a malformed body). Asserting an error reply is impossible. Two options:
- **Drop** the malformed-payload test case. Recommend this — the behavior is "no reply on malformed input" which is hard to assert positively.
- Keep the test, but assert the timeout: `assert.Error(t, sendAndAwaitReply(...))` with a short timeout, expecting it to fail with a NATS timeout error.

Use the second option; one-line assertion.

### 11.E — Replace "JWT signed by wrong account" with "missing creds file" (HIGH H11)

Synthesizing a valid NATS user JWT with the wrong signing key requires `jwt/v2` claim construction (`jwt.NewUserClaims`, encode, sign with rogue nkey) — non-trivial and the existing test budget didn't account for it. Replace with a simpler-but-still-meaningful negative test:

```go
func TestErrors_BadCredsRejected(t *testing.T) {
    // Connect with a bogus creds file path -> expect connect error.
    _, err := nats.Connect(stack.SiteA.NATSURL, nats.UserCredentials("/tmp/does-not-exist.creds"))
    require.Error(t, err)
}
```

Tests the same boundary (NATS doesn't accept unauthenticated connections in operator mode) without needing a JWT-forging helper.

### 11.F — Sanitization assertion: soften, don't enforce

Per the gatekeeper review, error messages from the gatekeeper are NOT sanitized (returns raw `err.Error()` like `"user X is not subscribed to room Y"`). CLAUDE.md says they SHOULD be. Sanitization is a separate code-fix concern. The chapter-11 test should assert the error reply is a `model.ErrorResponse` with `Error != ""` — NOT that the message is "sanitized" or matches a particular pattern. Note this in the test as a TODO.

---

## Amendment to Chapter 12 — Narrow scope, fix catch-up shape

### 12.A — Drop `TestFederation_CrossSiteMessageDelivery` (BLOCKER B6)

No federation path exists for messages. broadcast-worker only consumes local `MESSAGES_CANONICAL`; message-worker only persists local-site messages. Cross-site channel/DM message delivery doesn't happen in the system today.

**Action:** delete task 12.1 entirely. Update the chapter-map "headline federation test" framing — the new headline is `TestFederation_CrossSiteInvite`.

### 12.B — Rescope catch-up test to invites/role updates, not messages (BLOCKER B6 corollary)

Chapter 12's task 12.3 sends "50 messages" — those events don't reach siteB. Rescope to events that DO federate (member_added, member_removed, role_update):

```
1. Pause inbox-worker-b via ServiceContainer().Stop (NOT pause; see 12.C).
2. alice (siteA) invites bob, then carol, then dave, ... 20 cross-site
   members into 20 different channel rooms.
3. Verify OUTBOX_siteA accumulates 20 events; verify INBOX_siteB sources
   them too (gateway sourcing is independent of inbox-worker).
4. Restart inbox-worker-b via ServiceContainer().Start.
5. Wait via require.Eventually for the durable's NumPending == 0.
6. Assert mongo-b's subscriptions collection has all 20 records.
```

Wall-clock budget: ~15s (Stop + 20 publishes + Start + drain). 20 events is enough to demonstrate catch-up without being a "test the test" trap.

### 12.C — Use `Stop`/`Start`, not `pause`/`unpause` (e2e-expert)

`docker compose pause` keeps server-side ack timers running and TCP sockets open. `Stop`/`Start` is closer to a real container outage. The chapter-12 task body already mentions Stop/Start; remove the "pause" framing in the goal sentence for consistency.

### 12.D — Cross-site invite test needs user-seeding (HIGH H6)

`TestFederation_CrossSiteInvite` requires `mongo-a` to know bob is on siteB. `auth-service` creates user records on the **authenticating** site only. If the test authenticates bob on siteA first, mongo-a marks bob as siteId=siteA, mis-routes the OUTBOX target.

**Fix in the test setup:**

```go
// Authenticate bob on siteB FIRST so mongo-b has bob with siteId=siteB.
bobOnB := stack.SiteB.Authenticate(t, ctx, "bob")
_ = bobOnB

// Pre-seed mongo-a's users collection with bob marked as siteId=siteB so
// alice@siteA's invite path resolves correctly.
seedRemoteUser(t, ctx, stack.SiteA.MongoDB(t), bobOnB.Account, "siteB")
```

Add a small helper `seedRemoteUser(t, ctx, db, account, siteID)` to `harness/clients.go` that inserts a stub `users` record (just enough fields to satisfy room-worker's lookup). Document this as a "test-only seeding step that simulates cross-site user discovery" — production has a separate user-replication mechanism out of scope here.

---

## Amendment to Chapter 11 (additions) — Notification-worker assertion

Per the notification-worker reviewer, no scenario asserts notification-worker fired. Add a piggyback assertion to the existing `TestMessage_SendAndBroadcast_SingleSite` in chapter 10 (this is the cheapest place):

```go
notifSub, err := bob.Conn().SubscribeSync(subject.Notification(bob.Account))
require.NoError(t, err)
defer notifSub.Unsubscribe()

// ... existing send + receive assertions ...

notifMsg, err := notifSub.NextMsg(5*time.Second)
require.NoError(t, err)
var notif model.NotificationEvent
require.NoError(t, json.Unmarshal(notifMsg.Data, &notif))
assert.Equal(t, ids.MessageID, notif.MessageID)
```

Verify `subject.Notification(account)` is the actual builder name (otherwise inline the format string `chat.user.{account}.notification`). One assertion, exercises notification-worker end to end, no new test file.

---

## Summary of files this revision touches when implemented

| Chapter file | Lines/blocks affected |
|---|---|
| `Makefile` | new `--profile init` lines + host check |
| `docker-local/e2e/.env.example` | unchanged |
| `docker-local/e2e/nats/nats-{a,b}.conf` | drop `jetstream.domain` |
| `docker-local/e2e/setup-e2e.sh` | full rewrite using `docker-local/setup.sh` patterns + system_account directive |
| `docker-local/e2e/compose.e2e.yaml` | Cassandra heap, Cassandra init job, search init job, Keycloak healthcheck + KC_HOSTNAME, OTEL_SDK_DISABLED everywhere, OIDC_ISSUER_URL + extra_hosts on auth-service-{a,b}, SEARCH_USER_ROOM_INDEX |
| `e2e/harness/stack.go` | `composeFilePath` walks for go.mod |
| `e2e/harness/auth.go` | drop `account` from /auth body |
| `e2e/harness/federation.go` | `ensureOutbox` before `updateInbox` |
| `e2e/harness/clients.go` | add `seedRemoteUser` |
| `e2e/scenarios/*` | subject/type audit (B3); `sendAndAwaitReply` for msg.send; `awaitDurableReady` + `awaitCanonicalAcked`; channel create uses Users; DM idempotency reads RoomID off ErrorResponse; bad-JWT replaced by bad-creds; drop cross-site message test; rescope catch-up to invites |

## Out of scope for this revision (filed as follow-ups)

- Trace-span continuity assertions (OpenTelemetry e2e check)
- Log-level happy-path assertion (no `slog.Error` during a passing test)
- Encryption-on coverage (`ENCRYPTION_ENABLED=true` path)
- Edit/delete message paths (`MsgEdit`, `MsgDelete`, `EventUpdated`/`EventDeleted` through search-sync-worker)
- Time-window pagination, threads
- Throughput baseline (one test asserting p99 < 2s for a 100-message burst)
- Connection-recovery test (kill nats-a mid-send, assert producer reconnects)
- Per-test JS state snapshot in `CaptureLogs`
- t.Parallel() opt-in for read-only single-site tests
- Two-account federation (separate operator-per-site with cross-signing) for trust-isolation realism
- Cassandra image version drift between `pkg/testutil/testimages` (4.1.3) and e2e plan (5)
- OIDC discovery URL decoupling in auth-service (would simplify B7)
- Graceful shutdown verification (drain ordering, no message loss)

---

## Revision 2 — additional amendments from second review pass

The first revision missed three families of issues that surfaced when chapters 1+2 landed and got fresh eyes on them. This section layers on top.

### R2.A — Chapter 3 host-port allocation table (latent bug: collision with `make deps-up`)

**Problem:** the original chapter 3 plan (lines 361, 390, 411, 442, 471, 492 of the spec) host-publishes site-A ports as `4222:4222`, `27017:27017`, `9042:9042`, `9200:9200`, `6379:6379`, `8180:8080`, `8222:8222` — exactly the ports `docker-local/compose.deps.yaml` already binds. Chapter 5 site-B uses a +100 band (`4322`, `27018`, `9142`, `9300`, `6479`, `8181`). A developer with `make deps-up` running can't `make e2e-up`. The plan never specified a port table.

**Fix:** Move site-A to a `+10000` band, site-B to a `+20000` band. deps-up keeps stock ports. Each band preserves the last 4 digits of the original (so ports remain mnemonic).

| Service | Internal | deps-up host | site-A host (chapter 3) | site-B host (chapter 5) |
|---|---|---|---|---|
| NATS client | 4222 | 4222 | **14222** | **24222** |
| NATS HTTP monitoring | 8222 | 8222 | **18222** | **28222** |
| NATS gateway | 7222 | — | (network-only) | (network-only) |
| NATS cluster | 6222 | — | (network-only) | (network-only) |
| Mongo | 27017 | 27017 | **37017** | **47017** |
| Cassandra | 9042 | 9042 | **19042** | **29042** |
| Elasticsearch | 9200 | 9200 | **19200** | **29200** |
| Valkey | 6379 | 6379 | **16379** | **26379** |
| Keycloak | 8080 | 8180 | **18180** | **28180** |
| auth-service | 8080 | — | **18080** | **28080** |
| search-service metrics | 9090 | — | **19090** | (drop, per existing chapter 12 amendment) |

**Rule:** internal container-to-container traffic uses the original ports (4222, 8080, etc.) via the `chat-e2e` docker network. Only the harness on the host uses the +10000/+20000 mapped ports.

**Edits required when chapter 3 lands:**
- Replace every `"4222:4222"` etc. with `"14222:4222"` etc. on site-A blocks.
- Cross-reference amendment 3.D (Keycloak `KC_HOSTNAME`): must become `http://host.docker.internal:18180` (was `:8180`). Site-B mirror in chapter 5 becomes `:28180` (was `:8181`).
- Cross-reference amendment 4.B (auth-service `OIDC_ISSUER_URL`): `http://host.docker.internal:18180/realms/chatapp` for site-A; `:28180` for site-B.
- Cross-reference amendment 4.B (search-init job): `http://es-a:9200` is unchanged (internal docker-network address).
- Update chapter 7 `harness/stack.go` `SiteEndpoints` to point at the new host ports (`http://localhost:14222`, etc.).

**Defensive belt-and-braces:** also add a precheck to `e2e-up`:

```makefile
e2e-up: $(E2E_ENV)
	@docker compose -f docker-local/compose.deps.yaml ps -q nats >/dev/null 2>&1 \
	  && { echo "ERROR: 'make deps-up' is running. Stop it first ('make deps-down') — even with the new port band, sharing two stacks on one box is unsupported."; exit 1; } \
	  || true
	docker compose -f $(E2E_COMPOSE) --profile init up -d --wait
	docker compose -f $(E2E_COMPOSE) --profile init run --rm cassandra-init-a
	docker compose -f $(E2E_COMPOSE) --profile init run --rm cassandra-init-b
	docker compose -f $(E2E_COMPOSE) --profile init run --rm search-init-a
	docker compose -f $(E2E_COMPOSE) --profile init run --rm search-init-b
```

(Even with non-overlapping ports, dual-stack on a 32GB box risks OOM. Hard guard is cheaper than debugging mystery failures.)

### R2.B — `.env` regeneration when `.env.example` changes (latent #4)

**Problem:** the `$(E2E_ENV)` rule has no prerequisites. Once `docker-local/e2e/.env` exists, edits to `.env.example` do not propagate. When chapter 4's amendment adds new `E2E_*` vars (or chapter 3 adds image refs), existing devs see cryptic compose errors like `variable is not set` rather than a useful prompt.

**Fix:** purely documentary. Append to chapter 1's `.env.example` itself:

```
# When you pull updates that add new vars to this file, you must
# manually refresh your local .env by running:
#     rm docker-local/e2e/.env && make e2e-up
# This re-copies the latest defaults. The Makefile rule will not
# auto-overwrite an existing .env (by design — it preserves your
# local mirror overrides).
```

No Makefile change. The auto-create rule's "preserve local overrides" semantics are the desired behavior; we only need to make the refresh story discoverable.

### R2.C — Coverage adds folded into chapters 10–12 (per coverage audit)

The amendments doc's "Out of scope" list dropped DM-broadcast and mention-resolution as deferred. That was wrong — they're cheap to add and they exercise distinct code paths the suite otherwise misses entirely. Plus the audit surfaced one fully-undocumented omission (`MessageRead`) and asymmetric federation coverage (only `member_added`, missing `role_update` and `member_removed`).

**Add to chapter 10** (single-site happy path):

1. **DM broadcast variant** — clone `TestMessage_SendAndBroadcast_SingleSite` as `TestMessage_DMSendAndBroadcast_SingleSite`. Create the room via `idgen.BuildDMRoomID(alice.User.ID, bob.User.ID)` and `model.CreateRoomRequest{Type: model.RoomTypeDM, Users: []string{bob.Account}}`. Send via `subject.MsgSend` (same builder); the broadcast channel for DMs is `subject.UserRoomEvent(account)` not `subject.RoomEvent(roomID)` — bob's `SubscribeSync` target changes accordingly. Cost: ~30 lines; one new test.

2. **Strengthen the chapter-10 history assertion** — after `LoadHistory`, assert:
   ```go
   require.Len(t, hist.Messages, 1)
   m := hist.Messages[0]
   assert.Equal(t, ids.MessageID, m.ID)
   assert.Equal(t, alice.Account, m.Sender)
   assert.NotZero(t, m.CreatedAt)
   assert.Equal(t, stack.SiteA.SiteID, m.SiteID)
   ```
   Catches a class of "worker writes garbage extra fields" or "Sender gets stripped" regressions the equality-only check misses.

3. **Mention-resolution exercise** — change the message body to include `@bob`, then assert the notification's `Mentions` field (or whatever the actual field name on `model.NotificationEvent` is — verify before writing) contains bob's account. Catches the high-frequency "mention parser broke and no one notices" bug. The current chapter 10 amendment (notification piggyback) only proves notification-worker fired; it doesn't prove the mention path lit up.

**Add to chapter 11** (single-site coverage):

4. **Strengthen search assertion** — replace the chapter-11 generic search test's body with a unique two-token string like `"pumpernickel xylophone"`, search for `pumpernickel`, assert the response contains exactly one hit AND that hit's message ID matches the one alice sent. One-line content change + one extra assertion. Turns a smoke test into a real test.

5. **`TestRoom_MarkMessageRead` happy path** — read receipts are a wired RPC at `room-service/handler.go:81` (`subject.MessageReadWildcard`) and a documented client API in `docs/client-api.md` section 3.1. Subject builder: verify whether it's `subject.MessageRead(account, roomID)` or similar (read `pkg/subject/subject.go` first). Test: alice sends → alice acks read → `requestReply` returns success; verify Mongo subscription document's `LastSeenAt` updated. Cost: ~25 lines.

**Add to chapter 12** (federation):

6. **Negative-isolation assertion** — after `TestFederation_CrossSiteInvite`, separately invite carol@A into a siteA-only channel. Subscribe `subject.UserSubscriptionUpdate(carol.Account)` on **siteB's** NATS. Assert the subscription event does NOT arrive within 2s. Catches the "we accidentally over-broadcast cross-site" regression class. ~15 lines.

7. **Cycle through OUTBOX event types** — extend chapter 12's catch-up rescope (currently 20 `member_added` events) into a small table-driven test covering each OUTBOX subject: `member_added`, `member_removed`, `role_update`. Each is a separate handler in `inbox-worker`; bug in one wouldn't show via the others. Cost: ~40 lines, +2 sub-tests.

### R2.D — Out-of-scope list updated

Two items removed from "out of scope" because R2.C now covers them:
- ~~DM broadcast path~~ — added in R2.C item 1.
- ~~Mentions~~ — added in R2.C item 3 (resolution path), though full mention parser unit-test coverage still belongs to `pkg/mention` per its own integration tests.

One item added to "out of scope":
- **Read receipt edge cases** (re-reading already-read messages, reading older messages out of order, multi-device sync). R2.C item 5 covers only the happy-path RPC; the edge cases warrant a follow-up plan.

### R2.E — Cross-cutting consistency (cleanup of chapter-1/2 latent bugs)

The chapter-1 Makefile is amended in this revision (committed alongside this doc):
- `e2e-only` gets a `$(E2E_ENV)` prerequisite and a precheck that `nats-a` is up. Fixes the confusing `lstat ./e2e/: no such file or directory` failure on a fresh clone.
- The `e2e-up` precheck for `make deps-up` running (R2.A above) lands when chapter 3 modifies the target.

The `pkg/testutil/testimages/testimages.go` Mongo pin is bumped from 4.4.15 → 8 in this revision (committed alongside this doc) to match the prod-local stack and the e2e suite. The original 4.4.15 comment described a divergence-protection pattern that the codebase doesn't actually use today (no `partialFilterExpression` with `$in`); the comment was preventative, not reactive. Sandbox lacks Docker so behavioral verification of integration tests against mongo:8 is deferred to CI.

---

## Revision 3 — codebase-survey corrections

After committing Revision 2, an `Explore`-agent survey of `pkg/`, services, and `docker-local/` found two real BLOCKERS where the amendments referenced fields/behavior that don't exist in the current codebase, plus one HIGH-severity item that needs a 4-line code change to one shared package. This revision corrects them. Survey also confirmed that most other amendment claims (subjects, model types, stream ownership, Keycloak realm, Cassandra init layout) are accurate as written.

### R3.A — `NotificationEvent` field-name corrections (BLOCKER)

**Problem:** Revision 2's R2.C item 3 (mention assertion) and the chapter-11 piggyback assertion in the original amendments both reference `notif.MessageID` and `notif.Mentions`. Verified against `pkg/model/event.go:70`:

```go
type NotificationEvent struct {
    Type      string  `json:"type"`        // "new_message"
    RoomID    string  `json:"roomId"`
    Message   Message `json:"message"`     // value, not pointer
    Timestamp int64   `json:"timestamp" bson:"timestamp"`
}
```

There is **no** top-level `MessageID` and **no** top-level `Mentions`. They live one level down on `Message` (`pkg/model/message.go:9`):

```go
type Message struct {
    ID          string         `json:"id"          bson:"_id"`
    UserAccount string         `json:"userAccount" bson:"userAccount"`   // NOT "Sender"
    Content     string         `json:"content"     bson:"content"`
    Mentions    []Participant  `json:"mentions,omitempty" bson:"mentions,omitempty"`
    CreatedAt   time.Time      `json:"createdAt"   bson:"createdAt"`     // time.Time, NOT int64
    // ... no SiteID
}
```

`Participant.Account` (`pkg/model/event.go:121`) is the field to compare against `bob.Account` for the mention check.

**Corrections to apply when chapter 10 / 11 land:**

The chapter-10 piggyback assertion in the original amendments doc (the "Notification-worker assertion" section near the end of Revision 1) becomes:

```go
notifSub, err := bob.Conn().SubscribeSync(subject.Notification(bob.Account))
require.NoError(t, err)
defer notifSub.Unsubscribe()

// ... existing send + receive assertions ...

notifMsg, err := notifSub.NextMsg(5*time.Second)
require.NoError(t, err)
var notif model.NotificationEvent
require.NoError(t, json.Unmarshal(notifMsg.Data, &notif))
assert.Equal(t, ids.MessageID, notif.Message.ID)         // .Message.ID, NOT .MessageID
```

R2.C item 2 (strengthen-history) becomes:

```go
require.Len(t, hist.Messages, 1)
m := hist.Messages[0]
assert.Equal(t, ids.MessageID, m.ID)
assert.Equal(t, alice.Account, m.UserAccount)            // .UserAccount, NOT .Sender
assert.False(t, m.CreatedAt.IsZero())                    // time.Time, use .IsZero()
// SiteID assertion DROPPED -- Message struct has no SiteID. The site is
// scoped via the NATS subject + history-service connection, not the document.
```

R2.C item 3 (mention resolution) becomes:

```go
// Message body: "hello @bob, ping?"
// ...send and receive notification...
require.NotEmpty(t, notif.Message.Mentions)              // .Message.Mentions, NOT .Mentions
mentionAccounts := make([]string, 0, len(notif.Message.Mentions))
for _, p := range notif.Message.Mentions {
    mentionAccounts = append(mentionAccounts, p.Account)
}
assert.Contains(t, mentionAccounts, bob.Account)
```

### R3.B — Honor `OTEL_SDK_DISABLED` in `pkg/otelutil` (HIGH; small code change)

**Problem:** R1 amendment H3 set `OTEL_SDK_DISABLED=true` on every service container, expecting the standard OTel SDK env var to short-circuit tracer initialization. Survey confirmed `pkg/otelutil/InitTracer` calls `otlptracegrpc.New(ctx)` unconditionally — the env var has no effect today. Without an OTLP collector container in the e2e compose, every service spams export-attempt errors in its log every few seconds.

The OTel Go SDK's `otlptracegrpc.New` is *lazy* (returns immediately, connects on first export), so this is **not** a startup blocker — services run fine, just noisy. Reclassified from BLOCKER to HIGH.

**Fix (committed alongside this doc):** add a 3-line check at the top of `pkg/otelutil.InitTracer`:

```go
if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
    return noopShutdown, nil
}
```

This is a one-time change to one shared package — well within the spirit of "modify pkg only when it cleanly unblocks e2e without leaking e2e concerns into production code." The check uses the standard OTel SDK env var name, so it's not e2e-specific; production deployments can use it too if they want to disable tracing in a particular environment.

`InitMeter` is intentionally NOT changed: the Prometheus exporter is in-process (no network), so no log noise. Disabling it would also disable production metrics scraping in non-e2e environments where the same env var might one day be set for trace-only disabling.

### R3.C — `SEARCH_USER_ROOM_INDEX` is auto-derived (no action; survey false-alarm)

Survey worried that R1 amendment H4 explicitly setting `SEARCH_USER_ROOM_INDEX=user-room-siteA` might be redundant. Verified: yes, it's redundant — `search-sync-worker/main.go:93-95` auto-derives `user-room-{siteID}` if the env var is empty, and `search-service` reads the same env var. Both services already produce the right per-site index name without any explicit override.

The amendment's explicit setting is harmless (sets the same value the default would compute), and explicit-over-implicit is reasonable for a test environment where someone reading the compose file shouldn't have to know about service-internal defaults. **Keep amendment H4 as written.**

### R3.D — Unchanged but worth re-confirming for downstream chapters

The survey separately confirmed (no action needed):

- All subject builders referenced in amendments R1 chapter 10/11 exist with the claimed signatures (`MsgSend`, `RoomCreate`, `MemberAdd`, `RoomEventWildcard`, `UserResponse`, `SearchMessages`, `Notification`, `MessageRead`).
- `MsgHistory(account, roomID, siteID)` builder is the **only** missing one — already flagged in R1 amendment 10.A. Implementer should add it as a tiny prereq to chapter 10, not inline `fmt.Sprintf` (one new builder is cheaper than scattered format-strings).
- All model types referenced match (`AddMembersRequest{Users []string}`, `CreateRoomRequest{Name,Users,...}` with no ID/Type, `SendMessageRequest{ID,Content,RequestID,...}` with handler-enforced RequestID, `LoadHistoryRequest{Before,Limit}`, `RoomEvent`, `CreateRoomReply{RoomID}`, `ErrorResponse{Error,RoomID}`, `RoomTypeChannel`/`RoomTypeDM`).
- Stream-ownership claim is accurate: `OUTBOX_{siteID}` is genuinely orphaned (no service's `bootstrap.go` creates it). Amendment 9.A's harness `ensureOutbox` is necessary.
- `docker-local/cassandra/init/*.cql` exists with 10 sortable files; the `for f in $(ls /init/*.cql | sort)` shape from amendment 3.A works.
- `docker-local/keycloak/realm-export.json` (actually at `auth-service/deploy/keycloak/realm-export.json`) exists with realm `chatapp`, client `nats-chat`, users `alice`/`bob`. Compose mount path in chapter 3 / 5 must reflect the actual location — note this when chapter 3 lands.
