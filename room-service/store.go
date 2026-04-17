package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore

// SubscriptionWithMembership is the result of the GetSubscriptionWithMembership
// aggregation — the target's subscription joined with both the individual and
// org membership sources so the handler can decide whether the target is
// removable individually.
type SubscriptionWithMembership struct {
	Subscription            *model.Subscription
	HasIndividualMembership bool
	HasOrgMembership        bool
}

// RoomCounts is the result of CountMembersAndOwners — member and owner counts
// for a single room, computed in one aggregation.
type RoomCounts struct {
	MemberCount int
	OwnerCount  int
}

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	GetSubscriptionWithMembership(ctx context.Context, roomID, account string) (*SubscriptionWithMembership, error)
	CountMembersAndOwners(ctx context.Context, roomID string) (*RoomCounts, error)
	CountOwners(ctx context.Context, roomID string) (int, error)
}
