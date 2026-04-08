# Plan 2: Auth Service

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create the `auth-service/` that implements NATS auth_callout. When a client connects to NATS with an SSO token, this service verifies the token and issues a NATS user JWT with scoped publish/subscribe permissions.

**Tech Stack:** Go 1.25, `github.com/synadia-io/callout.go v0.2.1`, `github.com/nats-io/nats.go`, `github.com/nats-io/jwt/v2`, `github.com/nats-io/nkeys`

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `auth-service/handler.go` | `TokenVerifier` interface, `AuthHandler` struct, `AuthorizerFn`-compatible callback |
| Create | `auth-service/handler_test.go` | Tests with mock `TokenVerifier`: valid token, invalid token, missing token |
| Create | `auth-service/main.go` | Env config, NATS connect, auth callout registration, graceful shutdown |
| Create | `auth-service/Dockerfile` | Multi-stage build: compile + minimal runtime image |

---

### Task 1: Auth handler with `TokenVerifier` interface

- [ ] **Step 1: Write tests** — `auth-service/handler_test.go`

```go
package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// mockVerifier implements TokenVerifier for testing.
type mockVerifier struct {
	// account is returned on success; err is returned on failure.
	account string
	err      error
}

func (m *mockVerifier) Verify(token string) (account string, err error) {
	if m.err != nil {
		return "", m.err
	}
	return m.account, nil
}

// helper: create a fresh account signing key pair for tests.
func mustAccountKP(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("create account key: %v", err)
	}
	return kp
}

// helper: create a fresh user nkey pair for tests (simulates NATS server).
func mustUserNKey(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("create user key: %v", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return pub
}

func TestHandleAuth_ValidToken(t *testing.T) {
	signingKP := mustAccountKP(t)
	verifier := &mockVerifier{username: "alice"}
	handler := NewAuthHandler(verifier, signingKP)

	userNKey := mustUserNKey(t)
	req := &jwt.AuthorizationRequest{}
	req.UserNkey = userNKey
	req.ConnectOptions.Token = "valid-sso-token"

	encoded, err := handler.Handle(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Decode the JWT and verify claims.
	claims, err := jwt.DecodeUserClaims(encoded)
	if err != nil {
		t.Fatalf("decode user claims: %v", err)
	}

	// Check subject matches the user nkey from the request.
	if claims.Subject != userNKey {
		t.Errorf("subject = %q, want %q", claims.Subject, userNKey)
	}

	// Check expiration is set (within 2 hours).
	if claims.Expires == 0 {
		t.Error("expected non-zero expiration")
	}
	expiresAt := time.Unix(claims.Expires, 0)
	if time.Until(expiresAt) > 2*time.Hour+time.Minute {
		t.Errorf("expiration too far in the future: %v", expiresAt)
	}

	// Check publish permissions: chat.user.alice.>
	wantPub := "chat.user.alice.>"
	if !containsSubject(claims.Pub.Allow, wantPub) {
		t.Errorf("publish allow = %v, want to contain %q", claims.Pub.Allow, wantPub)
	}

	// Check subscribe permissions: chat.user.alice.> and chat.room.>
	wantSubs := []string{"chat.user.alice.>", "chat.room.>"}
	for _, ws := range wantSubs {
		if !containsSubject(claims.Sub.Allow, ws) {
			t.Errorf("subscribe allow = %v, want to contain %q", claims.Sub.Allow, ws)
		}
	}

	// Check _INBOX.> is allowed for request-reply patterns.
	if !containsSubject(claims.Pub.Allow, "_INBOX.>") {
		t.Errorf("publish allow = %v, want to contain %q", claims.Pub.Allow, "_INBOX.>")
	}
	if !containsSubject(claims.Sub.Allow, "_INBOX.>") {
		t.Errorf("subscribe allow = %v, want to contain %q", claims.Sub.Allow, "_INBOX.>")
	}
}

func TestHandleAuth_InvalidToken(t *testing.T) {
	signingKP := mustAccountKP(t)
	verifier := &mockVerifier{err: errors.New("token expired")}
	handler := NewAuthHandler(verifier, signingKP)

	userNKey := mustUserNKey(t)
	req := &jwt.AuthorizationRequest{}
	req.UserNkey = userNKey
	req.ConnectOptions.Token = "expired-token"

	_, err := handler.Handle(req)
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}

	wantMsg := "token expired"
	if err.Error() != wantMsg {
		t.Errorf("error = %q, want %q", err.Error(), wantMsg)
	}
}

func TestHandleAuth_MissingToken(t *testing.T) {
	signingKP := mustAccountKP(t)
	verifier := &mockVerifier{username: "alice"}
	handler := NewAuthHandler(verifier, signingKP)

	userNKey := mustUserNKey(t)
	req := &jwt.AuthorizationRequest{}
	req.UserNkey = userNKey
	req.ConnectOptions.Token = "" // empty token

	_, err := handler.Handle(req)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}

	wantMsg := "missing auth token"
	if err.Error() != wantMsg {
		t.Errorf("error = %q, want %q", err.Error(), wantMsg)
	}
}

func TestHandleAuth_PermissionsPerUser(t *testing.T) {
	signingKP := mustAccountKP(t)

	users := []string{"alice", "bob", "charlie"}
	for _, account := range users {
		t.Run(account, func(t *testing.T) {
			verifier := &mockVerifier{username: account}
			handler := NewAuthHandler(verifier, signingKP)

			userNKey := mustUserNKey(t)
			req := &jwt.AuthorizationRequest{}
			req.UserNkey = userNKey
			req.ConnectOptions.Token = "token-for-" + account

			encoded, err := handler.Handle(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			claims, err := jwt.DecodeUserClaims(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			wantPub := fmt.Sprintf("chat.user.%s.>", account)
			if !containsSubject(claims.Pub.Allow, wantPub) {
				t.Errorf("publish allow = %v, want %q", claims.Pub.Allow, wantPub)
			}

			wantSub := fmt.Sprintf("chat.user.%s.>", account)
			if !containsSubject(claims.Sub.Allow, wantSub) {
				t.Errorf("subscribe allow = %v, want %q", claims.Sub.Allow, wantSub)
			}
		})
	}
}

func containsSubject(list jwt.StringList, subject string) bool {
	for _, s := range list {
		if s == subject {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests — expect FAIL** (handler not defined yet)

```bash
cd auth-service && go test -v -run TestHandleAuth
```

- [ ] **Step 3: Create `auth-service/handler.go`**

```go
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// TokenVerifier verifies an SSO token and returns the account.
// Implementations handle the actual SSO/OAuth verification logic.
type TokenVerifier interface {
	Verify(token string) (account string, err error)
}

// AuthHandler processes NATS auth_callout requests.
type AuthHandler struct {
	verifier   TokenVerifier
	signingKey nkeys.KeyPair
}

// NewAuthHandler creates an AuthHandler with the given token verifier
// and NATS account signing key for issuing user JWTs.
func NewAuthHandler(verifier TokenVerifier, signingKey nkeys.KeyPair) *AuthHandler {
	return &AuthHandler{
		verifier:   verifier,
		signingKey: signingKey,
	}
}

// Handle processes an authorization request from the NATS server.
// It extracts the SSO token from ConnectOptions, verifies it, and
// returns a signed user JWT with scoped permissions.
func (h *AuthHandler) Handle(req *jwt.AuthorizationRequest) (string, error) {
	token := req.ConnectOptions.Token
	if token == "" {
		return "", errors.New("missing auth token")
	}

	account, err := h.verifier.Verify(token)
	if err != nil {
		return "", err
	}

	uc := jwt.NewUserClaims(req.UserNkey)
	uc.Audience = "$G"
	uc.Expires = time.Now().Add(2 * time.Hour).Unix()

	// Publish permissions: user's own namespace + inbox for request-reply.
	uc.Pub.Allow.Add(fmt.Sprintf("chat.user.%s.>", account))
	uc.Pub.Allow.Add("_INBOX.>")

	// Subscribe permissions: user's own namespace, all rooms, and inbox.
	uc.Sub.Allow.Add(fmt.Sprintf("chat.user.%s.>", account))
	uc.Sub.Allow.Add("chat.room.>")
	uc.Sub.Allow.Add("_INBOX.>")

	return uc.Encode(h.signingKey)
}

// Authorizer returns an AuthorizerFn compatible with the callout.go library.
// This bridges the AuthHandler to the callout service registration.
func (h *AuthHandler) Authorizer() func(req *jwt.AuthorizationRequest) (string, error) {
	return h.Handle
}
```

- [ ] **Step 4: Create `auth-service/go.mod`**

The auth service is a separate Go module under the repo root.

```
module github.com/hmchangw/chat/auth-service

go 1.25

require (
	github.com/hmchangw/chat v0.0.0
	github.com/nats-io/jwt/v2 v2.7.4
	github.com/nats-io/nats.go v1.41.1
	github.com/nats-io/nkeys v0.4.12
	github.com/synadia-io/callout.go v0.2.1
)

replace github.com/hmchangw/chat => ../
```

- [ ] **Step 5: Run `go mod tidy`** in `auth-service/` to resolve indirect deps

```bash
cd auth-service && go mod tidy
```

- [ ] **Step 6: Run tests — expect PASS**

```bash
cd auth-service && go test -v -run TestHandleAuth
```

- [ ] **Step 7: Commit**

```bash
git add auth-service/handler.go auth-service/handler_test.go auth-service/go.mod auth-service/go.sum && git commit -m "feat(auth-service): add auth handler with TokenVerifier interface and tests"
```

---

### Task 2: Service entrypoint with NATS auth callout registration

- [ ] **Step 1: Create `auth-service/main.go`**

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	callout "github.com/synadia-io/callout.go"

	"github.com/hmchangw/chat/pkg/shutdown"
)

func main() {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	credsPath := os.Getenv("NATS_CREDS")
	signingKeySeed := os.Getenv("AUTH_SIGNING_KEY")

	if signingKeySeed == "" {
		log.Fatal("AUTH_SIGNING_KEY is required")
	}

	// Parse the account signing key from the seed.
	signingKP, err := nkeys.FromSeed([]byte(signingKeySeed))
	if err != nil {
		log.Fatalf("parse signing key: %v", err)
	}

	// Connect to NATS with optional credentials file.
	var opts []nats.Option
	if credsPath != "" {
		opts = append(opts, nats.UserCredentials(credsPath))
	}
	opts = append(opts, nats.Name("auth-service"))

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()
	log.Printf("connected to NATS at %s", natsURL)

	// Create the auth handler with a real token verifier.
	// TODO: Replace SSOTokenVerifier with actual SSO implementation.
	verifier := &SSOTokenVerifier{}
	handler := NewAuthHandler(verifier, signingKP)

	// Register the auth callout service.
	svc, err := callout.NewAuthorizationService(nc,
		callout.Authorizer(handler.Authorizer()),
		callout.ResponseSignerKey(signingKP),
	)
	if err != nil {
		log.Fatalf("create auth callout service: %v", err)
	}
	log.Println("auth callout service started")

	// Graceful shutdown.
	shutdown.Wait(context.Background(), func(ctx context.Context) error {
		log.Println("stopping auth callout service...")
		if err := svc.Stop(); err != nil {
			return fmt.Errorf("stop callout service: %w", err)
		}
		nc.Close()
		return nil
	})
}

// SSOTokenVerifier implements TokenVerifier using actual SSO validation.
// This is a placeholder — replace with real SSO/OAuth token verification.
type SSOTokenVerifier struct{}

func (v *SSOTokenVerifier) Verify(token string) (string, error) {
	// TODO: Implement actual SSO token verification.
	// This should validate the token against the SSO provider and
	// return the authenticated account.
	return "", fmt.Errorf("SSO token verification not implemented")
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
```

- [ ] **Step 2: Verify compiles**

```bash
cd auth-service && go build .
```

- [ ] **Step 3: Commit**

```bash
git add auth-service/main.go && git commit -m "feat(auth-service): add main entrypoint with NATS auth callout registration"
```

---

### Task 3: Dockerfile

- [ ] **Step 1: Create `auth-service/Dockerfile`**

```dockerfile
# Stage 1: Build
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

# Copy the parent module (for replace directive).
COPY go.mod go.sum ./
COPY pkg/ pkg/

# Copy the auth-service module.
COPY auth-service/go.mod auth-service/go.sum ./auth-service/
WORKDIR /src/auth-service
RUN go mod download

COPY auth-service/*.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /auth-service .

# Stage 2: Runtime
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /auth-service /usr/local/bin/auth-service

ENTRYPOINT ["auth-service"]
```

- [ ] **Step 2: Commit**

```bash
git add auth-service/Dockerfile && git commit -m "feat(auth-service): add multi-stage Dockerfile"
```
