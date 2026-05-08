package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func makeTestUser() model.User {
	return model.User{ID: "u-test", Account: "user-test", SiteID: "site-local"}
}

func makeTestRoom() model.Room {
	return model.Room{ID: "room-test", SiteID: "site-local"}
}

func TestErrInvalidRate_IsSentinel(t *testing.T) {
	require.NotNil(t, ErrInvalidRate)
	require.NotEmpty(t, ErrInvalidRate.Error())

	// All three Run() entry points should return ErrInvalidRate when rate is 0,
	// not a freshly-allocated string-equal error.
	t.Run("messaging-pipeline Generator", func(t *testing.T) {
		p, _ := BuiltinPreset("small")
		f := BuildFixtures(&p, 1, "site-local")
		g := NewGenerator(&GeneratorConfig{
			Preset: &p, Fixtures: f, SiteID: "site-local",
			Rate: 0, Inject: InjectFrontdoor,
			Publisher: &recordingPublisher{}, Metrics: NewMetrics(),
			Collector: NewCollector(NewMetrics(), p.Name),
		}, 1)
		err := g.Run(context.Background())
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidRate),
			"messaging-pipeline Generator must wrap ErrInvalidRate; got %v", err)
	})

	t.Run("HistoryReadGenerator", func(t *testing.T) {
		p, _ := BuiltinPreset("history-read")
		gen := NewHistoryReadGenerator(&HistoryReadConfig{
			Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
			Rate: 0, Requester: &recordingRequester{}, Metrics: NewMetrics(),
		}, 1)
		err := gen.Run(context.Background())
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidRate),
			"HistoryReadGenerator must wrap ErrInvalidRate; got %v", err)
	})

	t.Run("SearchReadGenerator", func(t *testing.T) {
		p, _ := BuiltinPreset("search-read")
		gen := NewSearchReadGenerator(&SearchReadConfig{
			Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
			Rate: 0, Requester: &recordingRequester{}, Metrics: NewMetrics(),
		}, 1)
		err := gen.Run(context.Background())
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidRate),
			"SearchReadGenerator must wrap ErrInvalidRate; got %v", err)
	})
}

func TestErrUnknownKind_IsSentinel(t *testing.T) {
	require.NotNil(t, ErrUnknownHistoryKind)
	require.NotNil(t, ErrUnknownSearchKind)

	user := makeTestUser()
	room := makeTestRoom()
	hArgs := historyRequestArgs{User: user, Room: room, MessageID: "m-x", Limit: 50}
	_, _, err := buildHistoryRequest(historyRequestKind(99), &hArgs)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownHistoryKind),
		"buildHistoryRequest must wrap ErrUnknownHistoryKind; got %v", err)

	sArgs := searchRequestArgs{User: user, Query: "x"}
	_, _, err = buildSearchRequest(searchRequestKind(99), &sArgs)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownSearchKind),
		"buildSearchRequest must wrap ErrUnknownSearchKind; got %v", err)
}
