//go:build integration

package main

import (
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

type mockVerifier struct {
	username string
	err      error
}

func (v *mockVerifier) Verify(token string) (string, error) {
	return v.username, v.err
}

func TestAuthHandler_Integration(t *testing.T) {
	// Generate test signing key
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("create account key: %v", err)
	}

	userKP, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("create user key: %v", err)
	}
	userPub, _ := userKP.PublicKey()

	verifier := &mockVerifier{username: "testuser"}
	handler := NewAuthHandler(verifier, kp)

	req := &jwt.AuthorizationRequest{
		UserNkey:       userPub,
		ConnectOptions: jwt.ConnectOptions{Token: "valid-token"},
	}

	encoded, err := handler.Handle(req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Decode and verify the JWT
	claims, err := jwt.DecodeUserClaims(encoded)
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}

	// Verify publish permissions contain user namespace
	found := false
	for _, p := range claims.Pub.Allow {
		if p == "chat.user.testuser.>" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing publish permission for testuser, got: %v", claims.Pub.Allow)
	}
}
