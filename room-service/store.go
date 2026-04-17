package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore

// IndividualRemoveValidation is the result of the ValidateIndividualRemove aggregation
// pipeline used by room-service to authorize and guard individual remove requests.
type IndividualRemoveValidation struct {
	Subscription            *model.Subscription
	HasIndividualMembership bool
	HasOrgMembership        bool
	MemberCount             int
	OwnerCount              int
	RequesterIsOwner        bool
}

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	ValidateIndividualRemove(ctx context.Context, roomID, targetAccount, requesterAccount string) (*IndividualRemoveValidation, error)
	CountOwners(ctx context.Context, roomID string) (int, error)
}
