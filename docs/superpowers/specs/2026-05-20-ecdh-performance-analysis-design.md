# ECDH Ephemeral Key Performance — Analysis and Stage-2 Design Spec

**Date:** 2026-05-20
**Status:** Draft — awaiting review
**Scope:** Eliminate the per-message ECDH/ephemeral-keygen cost from
`broadcast-worker`'s encryption hot path by replacing the current ECIES-style
scheme with a versioned symmetric AES key derived once per room key version.

---

## Overview

`pkg/roomcrypto.Encode` is called on every broadcast event for an encrypted
channel room (see `broadcast-worker/handler.go:210, 254`). It generates a fresh
P-256 ephemeral key pair, performs ECDH against the room public key, HKDFs the
shared secret into a 32-byte AES key, and AEAD-seals the message under that
key. The ephemeral public key is sent on the wire so clients can repeat the
ECDH on their side and recover the same AES key.

This spec proposes replacing that scheme with a versioned symmetric AES key
derived directly from the room private key via HKDF — eliminating ECDH and
ephemeral-keygen from both server and client per-message paths. The wire
format drops the `ephemeralPublicKey` field and gains an explicit `scheme`
field so the decoder can tell new ciphertexts from legacy ones during the
migration window.

---

## Today's implementation

Per call to `roomcrypto.Encode(content, roomPublicKey, version)`:

1. Parse the 65-byte uncompressed P-256 public key.
2. Generate a fresh ephemeral P-256 key pair (`ecdh.P256().GenerateKey`).
3. ECDH between the ephemeral private key and the room public key.
4. HKDF-SHA256 the shared secret with `info="room-message-encryption"` into a
   32-byte AES key.
5. AES-256-GCM seal with a random 96-bit nonce.
6. Return `{version, ephemeralPublicKey, nonce, ciphertext}`.

Clients reverse the process: parse `ephemeralPublicKey`, ECDH against the
room private key, HKDF, GCM-open.

The room key pair (P-256 public + private) lives in Valkey under
`pkg/roomkeystore`. The server holds both halves; only the public half is
used at encrypt time. Clients receive the private half via `room-key-sender`.

---

## Performance measurement

Benchmark file: `pkg/roomcrypto/bench_test.go`. Run with
`go test -bench=. -benchmem -benchtime=2s -run=^$ ./pkg/roomcrypto/`.

Hardware used for these numbers: Intel Xeon @ 2.80 GHz, Linux amd64,
Go 1.25.10. (Reproducible in the dev container.)

| Operation                          | ns/op   | allocs/op | Share of `Encode` |
| ---------------------------------- | ------- | --------- | ----------------- |
| Full `Encode` (current)            | 87,185  | 39        | 100%              |
| P-256 ECDH (scalar mult)           | 64,823  | 2         | 74%               |
| P-256 ephemeral keygen             | 16,905  | 7         | 19%               |
| Parse uncompressed P-256 pub       | 380     | 5         | 0.4%              |
| **Symmetric-only path (proposed)** | **200** | **1**     | **0.2%**          |
| X25519 keygen (reference)          | 54,115  | 5         | —                 |
| X25519 ECDH (reference)            | 53,847  | 1         | —                 |

### Implications at sustained throughput

| Rate       | Current `Encode` CPU | Proposed CPU | CPU saved        |
| ---------- | -------------------- | ------------ | ---------------- |
| 1K msg/s   | 87 ms/s (~8.7% core) | 0.2 ms/s     | ~99.8%           |
| 10K msg/s  | 870 ms/s (~87% core) | 2 ms/s       | ~99.8%           |
| 100K msg/s | 8.7 s/s (~8.7 cores) | 20 ms/s      | effectively all  |

Allocations drop from 39/op to 1/op; at 10K msg/s that's ~390K allocs/s of
GC pressure removed from the broadcast hot path.

### Note on X25519

X25519 is conventionally faster than P-256, but Go's standard library ships
amd64 assembly for P-256 (`p256_asm_amd64.s` using ADX/BMI2) and not for
X25519, so on amd64 our measurements show P-256 ECDH (~65 µs) is **faster**
than X25519 ECDH (~54 µs). Curve switching would therefore not buy a useful
amount of performance in this deployment and is rejected on those grounds.
This is environment-specific — on arm64 or other Go targets the picture
inverts. Reconsider if the deployment substrate changes.

---

## Threat model and forward-secrecy analysis

This system is **not** end-to-end encrypted. The server holds the room
private key in Valkey (`pkg/roomkeystore/roomkeystore.go:18-19`). The
encryption scheme protects ciphertexts in two places only:

1. **In-flight broadcast events** carried over NATS (including federation via
   OUTBOX/INBOX).
2. **Nothing else.** `message-worker` writes messages to Cassandra in
   plaintext; `history-service` and `search-service` do not touch
   `roomcrypto`. Legacy ciphertexts are transient — once a broadcast event is
   consumed and acknowledged it is gone.

The realistic threats this encryption blunts:

- A NATS observer who can see traffic but does not have Valkey access cannot
  read message bodies.
- A federation peer that mishandles inbound traffic cannot read message
  bodies without the sending site's Valkey contents.

The realistic threats this encryption does **not** blunt:

- Anyone with Valkey access reads every room private key and can decrypt
  every ciphertext, past and present, because the ephemeral public key
  travels alongside on the wire.

### What about forward secrecy?

Forward secrecy means *past sessions remain confidential after long-term
key compromise*. Here, the long-term key is the room private key in Valkey.
Per-message ephemeral keys give **no** forward secrecy against Valkey
compromise: an attacker with `room_priv` runs ECDH against the
`ephemeralPublicKey` already on the wire, regenerates the per-message AES
key, and decrypts.

The only FS this system actually has is at the **key-rotation boundary**:
once a `VersionedKeyPair` ages out of Valkey's previous-key slot (per
`VALKEY_KEY_GRACE_PERIOD`), ciphertexts encrypted under the retired version
can no longer be decrypted from current Valkey state. Stage 2 preserves
this property unchanged — per-version forward secrecy is exactly what the
versioned symmetric key gives you.

### What the ephemeral key actually buys today

1. **Wire-level non-determinism.** Also provided by the random GCM nonce.
2. **An AES-GCM nonce-reuse safety net.** Since the AES key is per-message,
   a nonce collision across messages cannot break confidentiality. This is
   real defense-in-depth, but indirect: it hedges against a separate bug in
   the OS CSPRNG, which would catastrophically break the whole system.
3. **A compliance/audit talking point** that each message has its own key.

Items 1 and 3 are not security properties. Item 2 is the only real value
and is discussed under Risks below.

---

## Alternatives considered

### A. Status quo

Keep paying ~87 µs per message. Easiest to ship (zero work) but the
broadcast hot path keeps burning CPU on cryptographic operations that
provide no security benefit beyond what Stage 2 provides for free.

### B. Cache ephemeral key per (room, key-version), keep wire format

Server-only change: cache the ephemeral keypair the first time a key version
is used, then reuse it for every subsequent message in that version. Wire
format and client code unchanged. Captures ~99% of the perf win with zero
client coordination. Rejected only because the user has accepted the client
coordination cost and prefers a clean end state — the design notes this
option as a viable fallback if client coordination stalls.

### C. Switch curve to X25519

Conventional wisdom says "X25519 is faster than P-256." False in this
deployment — see the X25519 note above. Rejected on benchmark evidence.

### D. Precompute a pool of ephemeral keys

Amortizes keygen but does nothing about the ECDH cost, which is 74% of
`Encode`. Doesn't move the needle enough to justify the engineering.
Rejected.

### E. **HKDF-only versioned symmetric key** (recommended)

Drop ECDH entirely. The room private key (32 bytes of high-entropy scalar)
is already perfectly good IKM. Derive the per-version AES key as
`HKDF-SHA256(room_priv_v, salt=nil, info="room-message-encryption-v2")`,
cache it on both sides keyed on version, and use it directly. Wire format
becomes `{scheme, version, nonce, ciphertext}` — no `ephemeralPublicKey`.

Per-version FS is preserved (one AES key per key version; rotation retires
it). Per-message FS is unchanged (we never had it). The hot path becomes a
nonce read + GCM seal: ~200 ns per message.

---

## Recommendation

Adopt Alternative E. Bump the scheme to version `1` and update server and
clients together.

---

## New wire format

```go
// pkg/roomcrypto/roomcrypto.go
type EncryptedMessage struct {
    // Scheme identifies the encryption algorithm in use.
    //   0 (or absent): legacy ECIES with per-message ephemeral P-256 key.
    //   1:             HKDF-only versioned symmetric AES-256-GCM.
    // Decoders MUST inspect this field before interpreting other fields.
    Scheme int `json:"scheme,omitempty"`

    // Version is the room key version; matches roomkeystore VersionedKeyPair.Version.
    Version int `json:"version"`

    // EphemeralPublicKey is set only when Scheme == 0. Omitted for Scheme >= 1.
    EphemeralPublicKey []byte `json:"ephemeralPublicKey,omitempty"`

    // Nonce is the 12-byte AES-GCM nonce. Always present.
    Nonce []byte `json:"nonce"`

    // Ciphertext is content || 16-byte GCM tag. Always present.
    Ciphertext []byte `json:"ciphertext"`
}
```

Backwards compatibility: existing legacy ciphertexts already on the wire
during the rolling deploy decode correctly because Scheme defaults to 0
(`omitempty` on absence reads back as 0). Old clients that don't know about
`Scheme` ignore it and proceed to decode under the legacy path — which fails
loudly when `EphemeralPublicKey` is missing. That's acceptable only after
clients ship the new decoder; the migration plan below sequences this.

---

## New algorithm

Server-side `Encode(content, roomPrivateKey, version)`:

1. Look up (or compute and cache) `aesKey_v` for `version` from the in-process
   per-key-version cache. On cache miss:
   `aesKey_v = HKDF-SHA256(roomPrivateKey, salt=nil, info=[]byte("room-message-encryption-v2"))`,
   read 32 bytes.
2. AES-256-GCM with `aesKey_v`.
3. Generate a fresh 12-byte nonce from `crypto/rand`.
4. `gcm.Seal(nil, nonce, []byte(content), nil)`.
5. Return `EncryptedMessage{Scheme: 1, Version: version, Nonce: nonce, Ciphertext: ciphertext}`.

Two notable changes from the current API:

- **`Encode` now takes the room private key, not the public key.** The server
  already has it in `roomkeystore.VersionedKeyPair.KeyPair.PrivateKey`. The
  caller in `broadcast-worker/handler.go` swaps `key.KeyPair.PublicKey` for
  `key.KeyPair.PrivateKey` at lines 210 and 254. No new fetches required.
- **The cache lives in the `Encoder` value, not in package state.** A new
  `roomcrypto.Encoder` struct holds the per-version AES key cache, eviction
  policy, and the `crypto/rand` reader. `broadcast-worker` constructs one
  `*Encoder` in `main.go` and passes it to the handler.

```go
// pkg/roomcrypto/roomcrypto.go
type cacheKey struct {
    roomID  string
    version int
}

type Encoder struct {
    mu     sync.RWMutex
    cache  map[cacheKey]cipher.AEAD
    rand   io.Reader // crypto/rand.Reader; overridable for tests
    scheme int       // 0 or 1; set via NewEncoder option
}

func NewEncoder(opts ...EncoderOption) *Encoder

// Encode encrypts content under the AES key derived from roomPrivateKey for
// the given (roomID, version). The derived AES-GCM cipher is cached on the
// Encoder, so repeat calls for the same (roomID, version) skip key derivation.
func (e *Encoder) Encode(roomID, content string, roomPrivateKey []byte, version int) (*EncryptedMessage, error)
```

The cache is bounded by `MaxCacheEntries` (default 4096, env override
`ROOMCRYPTO_CACHE_SIZE`). Different rooms have different private keys, so
the key is `(roomID, version)` — `version` alone would alias across rooms.
Eviction policy is described under Implementation Details.

Client-side decryption (described informally — implementation in the JS and
Swift client repos):

1. Parse `{scheme, version, nonce, ciphertext}` (and `ephemeralPublicKey` if
   scheme == 0).
2. If `scheme == 0`: existing ECIES decode path (legacy).
3. If `scheme == 1`: look up cached `aesKey_v` for `(roomID, version)`; on
   cache miss, fetch the room's private key for that version from the local
   key store, derive `aesKey_v` via HKDF, cache it, then GCM-open.
4. If `scheme >= 2` or unknown: refuse to decrypt; surface a clear error so
   client telemetry catches a stale build hitting newer ciphertexts.

---

## Server changes

### `pkg/roomcrypto/roomcrypto.go`

- Add `Scheme int` field to `EncryptedMessage` (json `omitempty`).
- Make `EphemeralPublicKey` `omitempty`.
- Introduce `Encoder` struct + `NewEncoder()` + `(*Encoder).Encode(content, roomPrivateKey, version)`.
- Keep the package-level `Encode(content, roomPublicKey, version)` function
  for one release as a thin wrapper that returns scheme-0 ciphertexts —
  marked with a deprecation comment pointing to `Encoder`. Removed in a
  follow-up PR after the migration completes.
- Per-version cache: `map[cacheKey]cipher.AEAD` where `cacheKey = struct{ roomID string; version int }`,
  protected by `sync.RWMutex`, bounded by `MaxCacheEntries` with simple
  lowest-version eviction. (Bound is loose; this collection is small and
  warm.)

### `pkg/roomcrypto/bench_test.go`

- Already added as part of this analysis. Update to also benchmark
  `(*Encoder).Encode` so the new hot path's perf is tracked in CI runs.

### `pkg/roomcrypto/roomcrypto_test.go`

- New table-driven test `TestEncoder_Encode` covering: happy path, empty
  content, invalid private key length, scheme field set to 1 on output,
  `EphemeralPublicKey` is empty on output.
- New round-trip test `TestEncoder_RoundTrip` that encodes with the new
  encoder and decodes inline using the same HKDF derivation logic against
  the room private key.
- New `TestEncoder_CacheHit` that asserts encoding twice for the same
  `(roomID, version)` derives the AES key only once. Use a counting
  `randReader` or instrument cache misses via a test-only accessor.
- Keep all existing scheme-0 tests intact — they validate the legacy code
  path that must continue to work for the deprecation window.

### `broadcast-worker/handler.go`

- Hold `*roomcrypto.Encoder` on the handler struct, constructed in
  `main.go` via `roomcrypto.NewEncoder()`.
- At lines 210 and 254, replace
  `roomcrypto.Encode(content, key.KeyPair.PublicKey, key.Version)` with
  `h.encoder.Encode(roomID, content, key.KeyPair.PrivateKey, key.Version)`.
  Add `roomID` to the encoder signature so the cache key is correct.
- Add config gate `BROADCAST_ENCRYPTION_SCHEME` (envDefault `0`) that
  forces scheme 0 when set to 0 and scheme 1 when set to 1. Default 0
  during initial deploy; flipped to 1 only after clients are caught up.
  Encoder accepts the requested scheme through a constructor option:
  `NewEncoder(WithScheme(1))`.

### `broadcast-worker/main.go`

- Parse the new config field, construct the encoder with the right scheme.
- Pass the encoder to the handler.

### `broadcast-worker/handler_test.go`

- Mock expectations updated to inject a real `*roomcrypto.Encoder` (it has
  no external dependencies, no mocking required).
- New test case: scheme is correctly set on the published event JSON.

### `pkg/model/event.go`

- No struct changes. `EncryptedMessage` (the inner ciphertext type in
  `pkg/roomcrypto`) is what changes; `RoomEvent.EncryptedMessage` remains a
  `json.RawMessage` and is opaque to `pkg/model`.

### `docs/client-api.md`

- Update §4.1 and the encryption section to describe the new scheme
  field, the new wire layout, and the migration semantics. Per project
  rule (CLAUDE.md "Before Committing"), this doc update is part of the
  same PR as the broadcast-worker change.

### Mock regeneration

- `make generate SERVICE=broadcast-worker` after the handler interface
  change. `pkg/roomcrypto` does not use mockgen.

---

## Client changes (informational, out of scope for this branch)

Client changes are tracked separately because they live in other repos. The
client work this spec assumes:

1. JS and Swift decoders are updated to:
   - Inspect `scheme` first.
   - If `scheme == 0` (or absent): existing ECIES decode.
   - If `scheme == 1`: derive AES key from local room private key via
     HKDF-SHA256 with `info="room-message-encryption-v2"`, cache per
     `(roomID, version)`, then GCM-open.
   - Any other scheme: surface a clear error.
2. Client decoder is shipped to production and reaches a stable
   client-population coverage (target: ≥99%) **before** the server flips to
   emit scheme 1.

---

## Migration plan

Four phases. Each phase is a separate deploy.

**Phase 1 — Server: scheme-aware encoder, default off.**

- Land all server changes above. Server can emit either scheme based on
  config. Default `BROADCAST_ENCRYPTION_SCHEME=0`. No production behavior
  change yet.

**Phase 2 — Clients ship scheme-1 decoder.**

- JS and Swift decoders updated and deployed. Telemetry on
  "decoded scheme=1 message count" stays at zero until phase 3.

**Phase 3 — Server flips to scheme 1.**

- Set `BROADCAST_ENCRYPTION_SCHEME=1` on broadcast-worker in one site,
  monitor decode-error telemetry from clients. Roll out per-site.

**Phase 4 — Cleanup.**

- After a stabilization period (suggested: 4 weeks at scheme 1), open a PR
  to remove the package-level `roomcrypto.Encode` wrapper and the scheme-0
  branch of `(*Encoder).Encode`. The decoder side (client) keeps scheme-0
  support for a longer window or indefinitely; the actual call sites in
  `broadcast-worker` no longer reach the scheme-0 path.

Rollback at any phase is a config flip — set the env var back to 0 and
restart.

Encrypted ciphertexts are transient (never persisted by message-worker or
elsewhere), so once a scheme-1 broadcast is delivered or expires from
JetStream there are no orphan scheme-1 ciphertexts left in the system to
worry about. This is what makes the migration tractable.

---

## Implementation details

### Cache key

The per-version AES key derivation depends on `(roomID, version)` because
different rooms have different private keys. Cache key:

```go
type cacheKey struct {
    roomID  string
    version int
}
```

`(*Encoder).Encode` takes `roomID, content, roomPrivateKey, version` and
looks up the cache under `cacheKey{roomID, version}`.

### Cache size and eviction

- Default `MaxCacheEntries = 4096` (override via env var
  `ROOMCRYPTO_CACHE_SIZE` parsed in `broadcast-worker/main.go` and passed
  into the encoder constructor).
- Eviction: on insert over the limit, drop the entry with the lowest
  `version` value across all rooms. This is a deliberately simple policy —
  hot rooms keep their entries because they hit and refresh, and rare rooms
  with old versions are the right thing to drop. Constant-factor work per
  eviction is acceptable at this cache size; a min-heap is overkill.
- Cache entries are `cipher.AEAD` values, not raw bytes — derived once via
  `aes.NewCipher` + `cipher.NewGCM` and reused for every `Seal`. AES-GCM
  Go internals do not retain per-call state; reusing the `AEAD` is safe and
  is what the stdlib documents.

### Key-version rotation correctness

When a room rotates its key:

1. `roomkeystore.Rotate` increments the version and demotes the prior
   `VersionedKeyPair` to the previous-key slot.
2. `broadcast-worker` reads the new `VersionedKeyPair` via
   `keyStore.Get(ctx, roomID)` on its next message handle.
3. `(*Encoder).Encode` looks up `(roomID, newVersion)`, misses, derives the
   new AES key, caches it.
4. The old `(roomID, oldVersion)` cache entry is harmless — it ages out
   under the size-bounded eviction policy and is never used again because
   no encrypt path will request the old version.

### Nonce hygiene

With one AES key per (room, version) instead of one per message, nonce
collision becomes a real concern. The Go `crypto/rand` 96-bit nonce yields
collision probability ~2⁻³² after ~2³² messages **per key version**. Two
mitigations:

1. **Document the rotation cadence as a security parameter.** The room key
   rotation policy (currently operationally driven) must keep
   "messages per key version" comfortably below 2³² ≈ 4.3B. At 10K msg/s
   sustained on a single room that's ~5 days between mandatory rotations.
   For realistic chat workloads this is trivially satisfied.
2. **Prefer a deterministic-counter nonce scheme if rotation cadence is
   ever in doubt.** Out of scope here — flagged as future work below.

### Test reproducibility

`pkg/roomcrypto/bench_test.go` is committed and run via the standard
`go test -bench` invocation. No load harness changes required for this
spec. The benchmark numbers in this document were captured on the dev
container's Xeon @ 2.80 GHz; production silicon (typically newer Skylake
or Sapphire Rapids in our fleet) is likely faster on P-256 ASM and faster
still on AES-NI for the symmetric path. The ratios should hold.

---

## Testing

### Unit tests (in `pkg/roomcrypto/`)

- Existing scheme-0 tests stay green throughout the migration.
- `TestEncoder_Encode` — table-driven: happy path, empty content,
  too-short private key (31 bytes), too-long private key (33 bytes), scheme
  field set to 1, ephemeral field empty, version stamped.
- `TestEncoder_RoundTrip` — encode + decode inline using HKDF-derived AES
  key, content matches.
- `TestEncoder_CacheHit` — encode twice for the same `(roomID, version)`,
  assert AES key derivation happened only once (instrument via a test hook
  on the encoder).
- `TestEncoder_CacheEviction` — fill the cache past `MaxCacheEntries`, add
  one more, assert the lowest-version entry is evicted.
- `TestEncoder_NonDeterminism` — two encodes with identical inputs produce
  different nonces and different ciphertexts (under the same cached AES
  key, this is now solely a property of the random nonce — the test must
  still pass).
- `TestEncoder_RandReaderError` — inject a failing reader; nonce
  generation surfaces a wrapped error.

### Handler test (in `broadcast-worker/handler_test.go`)

- Existing cases updated so the handler holds a real `*roomcrypto.Encoder`.
- New case: published event's `encryptedMessage` JSON has `scheme: 1` and
  no `ephemeralPublicKey` field when the encoder is constructed
  `WithScheme(1)`. Same case with `WithScheme(0)` asserts the legacy
  fields are still present.

### Integration test

- `pkg/roomcrypto/integration_test.go` exists today and covers the JS
  decode side via testcontainers. Add scheme-1 cases that emit a
  scheme-1 ciphertext and assert a parallel JS decoder script can recover
  the plaintext. (This is non-trivial wiring — flagged in the plan
  as a discrete task.)

### Coverage target

`pkg/roomcrypto` ≥90% per project core-library standard.
`broadcast-worker` ≥80% per project default.

### Benchmark gate

`make test` does not run benchmarks by default. The intent is not to gate
CI on absolute benchmark numbers — only that the benchmark file builds and
runs cleanly. Periodic manual runs are the policy. (No new perf-CI rule
proposed here.)

---

## Risks and open questions

### R1. PRNG failure mode

If `crypto/rand.Read` returns identical bytes on two calls under the same
AES key, GCM authenticity is broken and the auth key can be recovered. The
current scheme hides this behind the per-message key derivation; the new
scheme is exposed to it directly. Mitigation: this is a system-wide
catastrophe under either scheme (JWT generation, NATS NKey signing, every
other use of `crypto/rand` is equally affected), so localizing the concern
here is misleading. Accept the risk.

### R2. Forgetting to rotate

If a room never rotates, its AES key encrypts the room's entire history
under one key. Mitigation: document the rotation cadence as a security
parameter (see Nonce hygiene above). If rotation discipline is a real
concern, follow-up work can add an automated rotation policy in
`roomkeystore` — out of scope for this spec.

### R3. Migration coordination

Phase 3 (flipping the server to scheme 1) must wait for client decoder
coverage. If we flip too early, in-flight clients on the old build fail to
decode messages and surface decryption errors in the UI. Mitigation: the
config flag is per-site; roll out to one site first, watch
client-reported decode errors, then proceed.

### R4. `EncryptedMessage` is also used for edits

`broadcast-worker/handler.go:210` encrypts edited content the same way as
new messages. Confirmed in this spec — the same encoder is used in both
sites with no special handling needed.

### R5. The "previous key" grace window

`roomkeystore` supports a previous-key slot so messages encrypted just
before a rotation can still be decoded. Under the new scheme this works
unchanged on the client (the client looks up the room private key for the
specific `version` on the message and derives the AES key from it). No
spec changes required.

### R6. Backwards compatibility on `EncryptedMessage` JSON

Old clients with the existing decoder receive a scheme-1 payload and fail
to decode it. This is expected and is exactly what the phased rollout
protects against. No JSON-level shim is provided; the `scheme` field is
the only signal.

---

## Future work (deferred)

- **Counter-based nonces.** If sustained per-room throughput approaches
  2³² messages between rotations, switch from random 96-bit nonces to a
  deterministic counter + random prefix scheme (per NIST SP 800-38D §8.2.1).
  Not needed at current scale.
- **Automated key rotation policy.** Tie rotation to a max-messages or
  max-age policy enforced in `roomkeystore`. Currently manual.
- **Removing the deprecated `roomcrypto.Encode` wrapper.** Follow-up PR
  after Phase 4 stabilization.
- **Re-evaluating curve choice on non-amd64 architectures.** If we ever
  deploy to arm64 the X25519 vs P-256 picture inverts; the scheme-0 code
  path is then a candidate for removal regardless of scheme-1 success.

---

## Out of scope

- Client decoder implementation (lives in JS and Swift repos).
- Re-encrypting historical messages (none are persisted encrypted).
- Changes to `roomkeystore`, `room-key-sender`, or NATS subjects.
- Changes to `message-worker`, `history-service`, `search-service`.
- Performance work outside `roomcrypto` and its caller in `broadcast-worker`.
