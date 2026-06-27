package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/msgraph"
)

func TestIsInCall(t *testing.T) {
	cases := []struct {
		name string
		p    msgraph.Presence
		want bool
	}{
		{"in a call", msgraph.Presence{Availability: "Busy", Activity: "InACall"}, true},
		{"conference", msgraph.Presence{Availability: "Busy", Activity: "InAConferenceCall"}, true},
		{"presenting", msgraph.Presence{Availability: "DoNotDisturb", Activity: "Presenting"}, true},
		{"available", msgraph.Presence{Availability: "Available", Activity: "Available"}, false},
		{"meeting not call", msgraph.Presence{Availability: "Busy", Activity: "InAMeeting"}, false},
		{"away", msgraph.Presence{Availability: "Away", Activity: "Away"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isInCall(tc.p))
		})
	}
}

func TestAccountFromEmail(t *testing.T) {
	cases := []struct {
		name                string
		email, domain, want string
		ok                  bool
	}{
		{"exact match", "alice@corp.com", "corp.com", "alice", true},
		{"case-insensitive domain", "Bob@CORP.com", "corp.com", "Bob", true},
		{"wrong domain", "carol@other.com", "corp.com", "", false},
		{"no domain", "nodomain", "corp.com", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := accountFromEmail(tc.email, tc.domain)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.ok, ok)
		})
	}
}

func newTestReconciler(t *testing.T) (*reconciler, *MockaccountLister, *MockuserLister, *MockpresenceReader, *MockexternalApplier, *MockinCallIndex, *MockidMapStore, *MockstatePublisher) {
	ctrl := gomock.NewController(t)
	accts := NewMockaccountLister(ctrl)
	users := NewMockuserLister(ctrl)
	pres := NewMockpresenceReader(ctrl)
	app := NewMockexternalApplier(ctrl)
	idx := NewMockinCallIndex(ctrl)
	idm := NewMockidMapStore(ctrl)
	pub := NewMockstatePublisher(ctrl)
	cfg := reconcileConfig{SiteID: "site-a", EmailDomain: "corp.com", ExternalTTL: time.Minute, IDMapRefreshTTL: time.Hour}
	return newReconciler(accts, users, pres, app, idx, idm, pub, cfg), accts, users, pres, app, idx, idm, pub
}

func TestReconcile_FreshCache_SetsAndClears(t *testing.T) {
	r, accts, users, pres, app, idx, idm, pub := newTestReconciler(t)
	ctx := context.Background()

	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice", "bob"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil) // cache warm: no ListUsers
	idm.EXPECT().Resolve(ctx, []string{"alice", "bob"}).Return(map[string]string{"alice": "ida", "bob": "idb"}, nil)
	pres.EXPECT().GetPresencesByUserId(ctx, gomock.Len(2)).Return([]msgraph.Presence{
		{ID: "ida", Activity: "InACall"},
		{ID: "idb", Activity: "Available"},
	}, nil)
	idx.EXPECT().Members(ctx).Return([]string{"bob"}, nil) // bob was in-call last run

	app.EXPECT().SetExternal(ctx, "alice", model.StatusInCall, time.Minute).Return(true, model.StatusInCall, nil)
	idx.EXPECT().Add(ctx, "alice").Return(nil)
	pub.EXPECT().Publish(ctx, "alice", model.StatusInCall)

	app.EXPECT().SetExternal(ctx, "bob", model.StatusNone, time.Minute).Return(true, model.StatusOnline, nil)
	idx.EXPECT().Remove(ctx, "bob").Return(nil)
	pub.EXPECT().Publish(ctx, "bob", model.StatusOnline)

	users.EXPECT().ListUsers(gomock.Any()).Times(0)

	require.NoError(t, r.run(ctx))
}

func TestReconcile_StaleCache_RefreshesIdMap(t *testing.T) {
	r, accts, users, pres, app, idx, idm, pub := newTestReconciler(t)
	ctx := context.Background()

	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(false, nil) // stale -> refresh
	users.EXPECT().ListUsers(ctx).Return([]msgraph.GraphUser{
		{ID: "ida", Mail: "alice@corp.com"},
		{ID: "idz", Mail: "stranger@other.com"}, // filtered: wrong domain
	}, nil)
	idm.EXPECT().Refresh(ctx, map[string]string{"alice": "ida"}, time.Hour).Return(nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(map[string]string{"alice": "ida"}, nil)
	pres.EXPECT().GetPresencesByUserId(ctx, []string{"ida"}).Return([]msgraph.Presence{
		{ID: "ida", Activity: "InACall"},
	}, nil)
	idx.EXPECT().Members(ctx).Return(nil, nil)
	app.EXPECT().SetExternal(ctx, "alice", model.StatusInCall, time.Minute).Return(true, model.StatusInCall, nil)
	idx.EXPECT().Add(ctx, "alice").Return(nil)
	pub.EXPECT().Publish(ctx, "alice", model.StatusInCall)

	require.NoError(t, r.run(ctx))
}

func TestReconcile_NoChange_NoPublish(t *testing.T) {
	r, accts, users, pres, app, idx, idm, pub := newTestReconciler(t)
	ctx := context.Background()
	_ = users
	_ = pub

	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(map[string]string{"alice": "ida"}, nil)
	pres.EXPECT().GetPresencesByUserId(ctx, []string{"ida"}).Return([]msgraph.Presence{
		{ID: "ida", Activity: "InACall"},
	}, nil)
	idx.EXPECT().Members(ctx).Return([]string{"alice"}, nil) // already in-call
	app.EXPECT().SetExternal(ctx, "alice", model.StatusInCall, time.Minute).Return(false, model.StatusInCall, nil)
	idx.EXPECT().Add(ctx, "alice").Return(nil)
	// changed=false -> no Publish

	require.NoError(t, r.run(ctx))
}

var errBoom = errors.New("boom")

func TestReconcile_ListSiteAccountsError(t *testing.T) {
	r, accts, _, _, _, _, _, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return(nil, errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_FreshError(t *testing.T) {
	r, accts, _, _, _, _, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(false, errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_ListUsersError(t *testing.T) {
	r, accts, users, _, _, _, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(false, nil)
	users.EXPECT().ListUsers(ctx).Return(nil, errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_RefreshError(t *testing.T) {
	r, accts, users, _, _, _, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(false, nil)
	users.EXPECT().ListUsers(ctx).Return([]msgraph.GraphUser{{ID: "ida", Mail: "alice@corp.com"}}, nil)
	idm.EXPECT().Refresh(ctx, map[string]string{"alice": "ida"}, time.Hour).Return(errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_ResolveError(t *testing.T) {
	r, accts, _, _, _, _, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(nil, errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_GetPresencesError(t *testing.T) {
	r, accts, _, pres, _, _, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(map[string]string{"alice": "ida"}, nil)
	pres.EXPECT().GetPresencesByUserId(ctx, []string{"ida"}).Return(nil, errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_MembersError(t *testing.T) {
	r, accts, _, pres, _, idx, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(map[string]string{"alice": "ida"}, nil)
	pres.EXPECT().GetPresencesByUserId(ctx, []string{"ida"}).Return([]msgraph.Presence{{ID: "ida", Activity: "InACall"}}, nil)
	idx.EXPECT().Members(ctx).Return(nil, errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_SetExternalError(t *testing.T) {
	r, accts, _, pres, app, idx, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(map[string]string{"alice": "ida"}, nil)
	pres.EXPECT().GetPresencesByUserId(ctx, []string{"ida"}).Return([]msgraph.Presence{{ID: "ida", Activity: "InACall"}}, nil)
	idx.EXPECT().Members(ctx).Return(nil, nil)
	app.EXPECT().SetExternal(ctx, "alice", model.StatusInCall, time.Minute).Return(false, model.StatusOffline, errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_IndexAddError(t *testing.T) {
	r, accts, _, pres, app, idx, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(map[string]string{"alice": "ida"}, nil)
	pres.EXPECT().GetPresencesByUserId(ctx, []string{"ida"}).Return([]msgraph.Presence{{ID: "ida", Activity: "InACall"}}, nil)
	idx.EXPECT().Members(ctx).Return(nil, nil)
	app.EXPECT().SetExternal(ctx, "alice", model.StatusInCall, time.Minute).Return(true, model.StatusInCall, nil)
	idx.EXPECT().Add(ctx, "alice").Return(errBoom)
	require.Error(t, r.run(ctx))
}

func TestReconcile_IndexRemoveError_ClearPath(t *testing.T) {
	r, accts, _, pres, app, idx, idm, _ := newTestReconciler(t)
	ctx := context.Background()
	accts.EXPECT().ListSiteAccounts(ctx, "site-a").Return([]string{"alice"}, nil)
	idm.EXPECT().Fresh(ctx).Return(true, nil)
	idm.EXPECT().Resolve(ctx, []string{"alice"}).Return(map[string]string{"alice": "ida"}, nil)
	// Teams reports nobody in a call, but the index still has bob -> clear path.
	pres.EXPECT().GetPresencesByUserId(ctx, []string{"ida"}).Return([]msgraph.Presence{{ID: "ida", Activity: "Available"}}, nil)
	idx.EXPECT().Members(ctx).Return([]string{"bob"}, nil)
	app.EXPECT().SetExternal(ctx, "bob", model.StatusNone, time.Minute).Return(true, model.StatusOffline, nil)
	idx.EXPECT().Remove(ctx, "bob").Return(errBoom)
	require.Error(t, r.run(ctx))
}
