package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

type recordedPublish struct {
	requester string
	roomID    string
	accounts  []string
	corrID    string
}

type stubMemberPublisher struct {
	mu    sync.Mutex
	calls []recordedPublish
	fail  bool
}

func (s *stubMemberPublisher) Publish(_ context.Context, requester, roomID string,
	req *model.AddMembersRequest, corrID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errStubPublish
	}
	s.calls = append(s.calls, recordedPublish{
		requester: requester,
		roomID:    roomID,
		accounts:  append([]string(nil), req.Users...),
		corrID:    corrID,
	})
	return nil
}

var errStubPublish = errStub("stub publish failure")

type errStub string

func (e errStub) Error() string { return string(e) }

func TestSustainedMembersGenerator_PublishesAtRateRoundRobin(t *testing.T) {
	p, _ := BuiltinMembersPreset("members-small")
	f, pools := BuildMembersFixtures(&p, 42, "site-A")
	owners := OwnersByRoom(&f)

	pub := &stubMemberPublisher{}
	metrics := NewMetrics()
	collector := NewMemberCollector(metrics, p.Name, "frontdoor")

	cfg := SustainedMembersConfig{
		Preset:         &p,
		Fixtures:       &f,
		Pools:          pools,
		Owners:         owners,
		SiteID:         "site-A",
		Rate:           50,
		UsersPerAdd:    2,
		Inject:         InjectFrontdoor,
		Shape:          ShapeUsers,
		Publisher:      pub,
		Metrics:        metrics,
		Collector:      collector,
		WarmupDeadline: time.Now(),
		MaxInFlight:    10,
	}
	gen := NewSustainedMembersGenerator(&cfg, 7)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	pub.mu.Lock()
	defer pub.mu.Unlock()
	assert.GreaterOrEqual(t, len(pub.calls), 10)
	for _, c := range pub.calls {
		assert.Len(t, c.accounts, 2)
		assert.NotEmpty(t, c.corrID)
		assert.Equal(t, owners[c.roomID], c.requester)
	}
}

func TestSustainedMembersGenerator_AbortsOnPoolExhaustion(t *testing.T) {
	f := Fixtures{
		Users: []model.User{
			{ID: "u1", Account: "u-1"}, {ID: "u2", Account: "u-2"},
			{ID: "u3", Account: "u-3"},
		},
		Rooms: []model.Room{
			{ID: "r1", Name: "r1", Type: model.RoomTypeChannel, SiteID: "site-A"},
		},
		Subscriptions: []model.Subscription{
			{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "u-1"},
				RoomID: "r1", SiteID: "site-A", Roles: []model.Role{model.RoleOwner}},
		},
	}
	pools := CandidatePools{"r1": {"u-2", "u-3"}}

	pub := &stubMemberPublisher{}
	metrics := NewMetrics()
	collector := NewMemberCollector(metrics, "test", "frontdoor")
	cfg := SustainedMembersConfig{
		Preset:   &MembersPreset{Name: "test", Users: 3, Rooms: 1, BaselineSize: 1, CandidatePool: 2},
		Fixtures: &f, Pools: pools, Owners: OwnersByRoom(&f),
		SiteID: "site-A", Rate: 100, UsersPerAdd: 2,
		Inject: InjectFrontdoor, Shape: ShapeUsers,
		Publisher: pub, Metrics: metrics, Collector: collector,
		WarmupDeadline: time.Now(), MaxInFlight: 10,
	}
	gen := NewSustainedMembersGenerator(&cfg, 7)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := gen.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "candidate pool exhausted")

	pub.mu.Lock()
	defer pub.mu.Unlock()
	assert.Equal(t, 1, len(pub.calls), "only one request fits before exhaustion")
}

var _ = fmt.Sprintf // ensure fmt is used if any subtests need formatting later
