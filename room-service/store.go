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
	ListRoomsByIDs(ctx context.Context, ids []string) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	GetSubscriptionWithMembership(ctx context.Context, roomID, account string) (*SubscriptionWithMembership, error)
	CountMembersAndOwners(ctx context.Context, roomID string) (*RoomCounts, error)
	CountOwners(ctx context.Context, roomID string) (int, error)
	CountSubscriptions(ctx context.Context, roomID string) (int, error)
	GetRoomMembersByRooms(ctx context.Context, roomIDs []string) ([]model.RoomMember, error)
	GetAccountsByRooms(ctx context.Context, roomIDs []string) ([]string, error)
	ResolveAccounts(ctx context.Context, orgIDs, directAccounts []string, roomID string) ([]string, error)
	// ListRoomMembers returns the members of roomID. When enrich=true, the
	// returned RoomMember.Member entries carry display fields populated via
	// $lookup stages against users and subscriptions. When enrich=false,
	// display fields are left zero.
	ListRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error)
	// ListOrgMembers returns all users whose sectId equals orgID, projected
	// as OrgMember rows sorted by account ascending. Returns errInvalidOrg
	// when no users match (treated as "orgId is not valid").
	ListOrgMembers(ctx context.Context, orgID string) ([]model.OrgMember, error)
}
