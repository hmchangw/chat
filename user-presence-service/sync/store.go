package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/msgraph"
)

//go:generate mockgen -source=store.go -destination=mock_test.go -package=main

// accountLister returns every account homed at this site (presence is
// site-scoped). Only the account field is projected.
type accountLister interface {
	ListSiteAccounts(ctx context.Context, siteID string) ([]string, error)
}

// userLister lists tenant users (Graph app-only). Satisfied by msgraph.Client.
type userLister interface {
	ListUsers(ctx context.Context) ([]msgraph.GraphUser, error)
}

// presenceReader reads Teams presence (Graph ROPC). Satisfied by
// msgraph.PresenceReader.
type presenceReader interface {
	GetPresencesByUserId(ctx context.Context, ids []string) ([]msgraph.Presence, error)
}

// externalApplier applies the per-account external status and reports whether
// the effective status changed. Satisfied by *presencestore.Store.
type externalApplier interface {
	SetExternal(ctx context.Context, account string, status model.PresenceStatus, ttl time.Duration) (bool, model.PresenceStatus, error)
}

// inCallIndex tracks accounts currently marked in-call so a run can clear those
// no longer in a call.
type inCallIndex interface {
	Members(ctx context.Context) ([]string, error)
	Add(ctx context.Context, account string) error
	Remove(ctx context.Context, account string) error
}

// idMapStore caches account -> azureObjectID and gates the periodic ListUsers
// refresh via a freshness marker.
type idMapStore interface {
	Fresh(ctx context.Context) (bool, error)
	Refresh(ctx context.Context, mapping map[string]string, ttl time.Duration) error
	Resolve(ctx context.Context, accounts []string) (map[string]string, error)
}

// statePublisher publishes a PresenceState change (best-effort fan-out).
type statePublisher interface {
	Publish(ctx context.Context, account string, status model.PresenceStatus)
}
