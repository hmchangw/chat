# Room Key Sender Integration Test Design

**Date:** 2026-03-31
**Status:** Approved

## Summary

An integration test in `pkg/roomkeysender/integration_test.go` that validates the end-to-end room key distribution and message decryption flow. A Go "server" publishes a room key via `roomkeysender`, then publishes an encrypted message (via `roomcrypto.Encode`) with the key version ID in an `X-Room-Key-Version` NATS header. A TypeScript "client" running in a Node container connects to NATS over WebSocket, receives the key, and decrypts the message using Web Crypto APIs.

## Scope

- Integration test only — no production code changes
- Exercises `roomkeysender.Send`, `roomcrypto.Encode`, NATS pub/sub, and TypeScript WebSocket client decryption
- Validates the real-world flow: browser client receives a room key over NATS WS, then uses it to decrypt subsequent messages

## Infrastructure

### NATS container

- Image: `nats:2-alpine`
- Exposed ports: TCP 4222 (Go publisher), WebSocket 8080 (TypeScript client)
- WebSocket requires a config file:
  ```
  websocket {
    port: 8080
    no_tls: true
  }
  ```
- Config file is written to a temp directory and bind-mounted or copied into the container
- Wait condition: `wait.ForLog("Server is ready")` or similar NATS ready log line

### Node container

- Image: `node:20-alpine`
- Kept alive with `sleep 600` (same pattern as `pkg/roomcrypto` integration test)
- Wait condition: `wait.ForExec([]string{"node", "--version"})`
- Setup installs `tsx` and `nats` npm packages
- `pkg/roomkeysender/testdata/client.ts` is copied into the container

## NATS Subjects

| Purpose | Subject | Transport |
|---------|---------|-----------|
| Room key delivery | `chat.user.{account}.event.room.key` | Via `roomkeysender.Send` |
| Encrypted message | `test.room.{roomID}.msg` | Direct `nats.Conn.PublishMsg` with header |

## Message Header

Encrypted messages carry the key version in a NATS header:
- Key: `X-Room-Key-Version`
- Value: the `versionId` from the `RoomKeyEvent`

## Test Flow

1. Start NATS container (TCP + WS)
2. Start Node container, install `tsx` + `nats`, copy `client.ts`
3. Go connects to NATS over TCP
4. Generate a fresh P-256 key pair
5. Run `client.ts` in Node container, passing: NATS WS URL, account, roomID
6. Brief delay for TypeScript subscriptions to establish
7. Go publishes room key via `roomkeysender.Send(account, &model.RoomKeyEvent{...})`
8. Go encrypts a plaintext message via `roomcrypto.Encode(content, publicKey)`
9. Go publishes the encrypted message JSON to `test.room.{roomID}.msg` with `X-Room-Key-Version` header
10. TypeScript client receives key, receives encrypted message, decrypts, prints plaintext to stdout
11. Go reads stdout, asserts it equals the original plaintext

## TypeScript Client Script

**File:** `pkg/roomkeysender/testdata/client.ts`

**CLI args:** `<nats-ws-url> <username> <roomID>`

**Behavior:**
1. Connect to NATS via WebSocket using the `nats` npm package
2. Subscribe to `chat.user.{account}.event.room.key`
3. Subscribe to `test.room.{roomID}.msg`
4. On key event: parse JSON as `RoomKeyEvent`, store key pair indexed by `versionId`
5. On encrypted message: read `X-Room-Key-Version` header, look up stored key by version, decrypt using Web Crypto (ECDH + HKDF-SHA256 + AES-256-GCM — same algorithm as existing `decrypt.ts`)
6. Write decrypted plaintext to stdout
7. Drain connection and exit

**Decryption algorithm** (matches `pkg/roomcrypto/testdata/decrypt.ts`):
- Import room private key as JWK for ECDH
- Import ephemeral public key from encrypted message
- ECDH to derive shared secret
- HKDF-SHA256 with info `"room-message-encryption"` and empty salt to derive AES-256 key
- AES-256-GCM decrypt with nonce from encrypted message, no AAD

## Go Test Structure

**File:** `pkg/roomkeysender/integration_test.go`

```go
//go:build integration

package roomkeysender_test

func setupNATS(t *testing.T) (*nats.Conn, string)        // returns Go conn + WS URL
func setupNode(t *testing.T, natsWSURL string) testcontainers.Container
func TestRoomKeySender_TypeScriptClient(t *testing.T)
```

- `setupNATS`: starts NATS container with WS config, connects Go client over TCP, returns connection and WS URL for TypeScript
- `setupNode`: starts Node container, installs `tsx` + `nats`, copies `client.ts`, returns container
- Test function: orchestrates the full flow, reads TypeScript stdout, asserts plaintext match

## File Layout

```
pkg/roomkeysender/
    roomkeysender.go              # (existing) Sender implementation
    roomkeysender_test.go         # (existing) Unit tests
    integration_test.go           # (new) Integration test
    testdata/
        client.ts                 # (new) TypeScript NATS WS client
```

## Assertions

- TypeScript script exits with code 0
- stdout from TypeScript equals the original plaintext string
- On failure: stderr + stdout are included in the test failure message for diagnostics

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| WebSocket for TypeScript client | Matches real-world browser client transport |
| TCP for Go publisher | Simpler, no WS config needed for server-side |
| `test.room.{roomID}.msg` subject | Decouples test from real message subject conventions |
| `X-Room-Key-Version` header | Explicit, self-documenting header key |
| Reuse `decrypt.ts` algorithm | Proven Web Crypto decryption logic, same ECDH+HKDF+AES-GCM flow |
| Single TypeScript script | Simple coordination — subscribe, receive, decrypt, exit |
| Node container pattern | Follows existing `pkg/roomcrypto` integration test pattern |
