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
	// return the authenticated username.
	return "", fmt.Errorf("SSO token verification not implemented")
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
