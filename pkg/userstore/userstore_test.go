package userstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

// fakeQuerier is a test fake for userQuerier.
type fakeQuerier struct {
	findByIDFn       func(ctx context.Context, id string) (*model.User, error)
	findByAccountsFn func(ctx context.Context, accounts []string) ([]model.User, error)
}

func (f *fakeQuerier) findByID(ctx context.Context, id string) (*model.User, error) {
	return f.findByIDFn(ctx, id)
}

func (f *fakeQuerier) findByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	return f.findByAccountsFn(ctx, accounts)
}

func newStore(q userQuerier) *mongoStore {
	return &mongoStore{q: q}
}

func TestMongoStore_FindUserByID_Unit(t *testing.T) {
	user := &model.User{ID: "u-1", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"}

	tests := []struct {
		name         string
		querier      *fakeQuerier
		id           string
		wantUser     *model.User
		wantErr      bool
		wantNotFound bool
	}{
		{
			name: "found",
			querier: &fakeQuerier{
				findByIDFn: func(_ context.Context, id string) (*model.User, error) {
					return user, nil
				},
			},
			id:       "u-1",
			wantUser: user,
		},
		{
			name: "not found wraps ErrUserNotFound",
			querier: &fakeQuerier{
				findByIDFn: func(_ context.Context, id string) (*model.User, error) {
					return nil, fmt.Errorf("find user %s: %w", id, ErrUserNotFound)
				},
			},
			id:           "nonexistent",
			wantErr:      true,
			wantNotFound: true,
		},
		{
			name: "DB error propagated",
			querier: &fakeQuerier{
				findByIDFn: func(_ context.Context, id string) (*model.User, error) {
					return nil, errors.New("mongo: connection refused")
				},
			},
			id:      "u-1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(tt.querier)
			got, err := store.FindUserByID(context.Background(), tt.id)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantNotFound {
					assert.True(t, errors.Is(err, ErrUserNotFound))
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUser, got)
		})
	}
}

func TestMongoStore_FindUsersByAccounts_Unit(t *testing.T) {
	users := []model.User{
		{ID: "u-1", Account: "alice"},
		{ID: "u-2", Account: "bob"},
	}

	tests := []struct {
		name      string
		querier   *fakeQuerier
		accounts  []string
		wantCount int
		wantErr   bool
	}{
		{
			name: "returns matching users",
			querier: &fakeQuerier{
				findByAccountsFn: func(_ context.Context, accounts []string) ([]model.User, error) {
					return users, nil
				},
			},
			accounts:  []string{"alice", "bob"},
			wantCount: 2,
		},
		{
			name: "empty accounts returns nil without calling querier",
			querier: &fakeQuerier{
				findByAccountsFn: func(_ context.Context, _ []string) ([]model.User, error) {
					panic("should not be called for empty accounts")
				},
			},
			accounts:  []string{},
			wantCount: 0,
		},
		{
			name: "DB error propagated",
			querier: &fakeQuerier{
				findByAccountsFn: func(_ context.Context, _ []string) ([]model.User, error) {
					return nil, errors.New("mongo: timeout")
				},
			},
			accounts: []string{"alice"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(tt.querier)
			got, err := store.FindUsersByAccounts(context.Background(), tt.accounts)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
		})
	}
}
