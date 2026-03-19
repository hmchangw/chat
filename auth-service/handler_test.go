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
	// username is returned on success; err is returned on failure.
	username string
	err      error
}

func (m *mockVerifier) Verify(token string) (username string, err error) {
	if m.err != nil {
		return "", m.err
	}
	return m.username, nil
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
	for _, username := range users {
		t.Run(username, func(t *testing.T) {
			verifier := &mockVerifier{username: username}
			handler := NewAuthHandler(verifier, signingKP)

			userNKey := mustUserNKey(t)
			req := &jwt.AuthorizationRequest{}
			req.UserNkey = userNKey
			req.ConnectOptions.Token = "token-for-" + username

			encoded, err := handler.Handle(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			claims, err := jwt.DecodeUserClaims(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			wantPub := fmt.Sprintf("chat.user.%s.>", username)
			if !containsSubject(claims.Pub.Allow, wantPub) {
				t.Errorf("publish allow = %v, want %q", claims.Pub.Allow, wantPub)
			}

			wantSub := fmt.Sprintf("chat.user.%s.>", username)
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
