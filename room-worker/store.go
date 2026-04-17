package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . SubscriptionStore

// UserWithOrgMembership is the result of the GetUserWithOrgMembership aggregation pipeline.
type UserWithOrgMembership struct {
	model.User       `bson:",inline"`
	HasOrgMembership bool `bson:"hasOrgMembership"`
}

// OrgMemberStatus is one element returned by GetOrgMembersWithIndividualStatus.
type OrgMemberStatus struct {
	Account                 string `bson:"account"`
	SiteID                  string `bson:"siteId"`
	SectName                string `bson:"sectName"`
	HasIndividualMembership bool   `bson:"hasIndividualMembership"`
}

type SubscriptionStore interface {
	// --- existing methods (invite flow) ---
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	GetUser(ctx context.Context, account string) (*model.User, error)
	AddRole(ctx context.Context, account, roomID string, role model.Role) error
	RemoveRole(ctx context.Context, account, roomID string, role model.Role) error

	// --- aggregation pipelines (remove flow) ---
	GetUserWithOrgMembership(ctx context.Context, roomID, account string) (*UserWithOrgMembership, error)
	GetOrgMembersWithIndividualStatus(ctx context.Context, roomID, orgID string) ([]OrgMemberStatus, error)

	// --- write operations (remove flow) ---
	DeleteSubscription(ctx context.Context, roomID, account string) (int64, error)
	DeleteSubscriptionsByAccounts(ctx context.Context, roomID string, accounts []string) (int64, error)
	DeleteRoomMember(ctx context.Context, roomID string, memberType model.RoomMemberType, memberID string) error
	DecrementUserCount(ctx context.Context, roomID string, count int) error
}
