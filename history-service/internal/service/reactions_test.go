package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service/mocks"
	"github.com/hmchangw/chat/pkg/model/cassandra"
)

func TestHydrateReactions_EmptyInput_SkipsCassandra(t *testing.T) {
	ctrl := gomock.NewController(t)
	reader := mocks.NewMockMessageReader(ctrl)
	reader.EXPECT().GetReactionsByMessageIDs(gomock.Any(), gomock.Any()).Times(0)

	svc := &HistoryService{msgReader: reader}
	require.NoError(t, svc.hydrateReactions(context.Background(), nil))
	require.NoError(t, svc.hydrateReactions(context.Background(), []models.Message{}))
}

func TestHydrateReactions_PopulatesMatchingMessages(t *testing.T) {
	ctrl := gomock.NewController(t)
	reader := mocks.NewMockMessageReader(ctrl)
	alice := cassandra.Participant{ID: "u1", Account: "alice"}

	reader.EXPECT().
		GetReactionsByMessageIDs(gomock.Any(), []string{"m1", "m2", "m3"}).
		Return(map[string]cassrepo.ReactionMap{
			"m1": {"👍": {alice}},
			// m2 has no reactions (omitted)
			"m3": {"❤️": {alice}},
		}, nil)

	svc := &HistoryService{msgReader: reader}
	msgs := []models.Message{
		{MessageID: "m1"},
		{MessageID: "m2"},
		{MessageID: "m3"},
	}
	require.NoError(t, svc.hydrateReactions(context.Background(), msgs))

	assert.Equal(t, map[string][]cassandra.Participant{"👍": {alice}}, msgs[0].Reactions)
	assert.Nil(t, msgs[1].Reactions, "messages with no reactions stay nil")
	assert.Equal(t, map[string][]cassandra.Participant{"❤️": {alice}}, msgs[2].Reactions)
}

func TestHydrateReactions_RepoError_Wraps(t *testing.T) {
	ctrl := gomock.NewController(t)
	reader := mocks.NewMockMessageReader(ctrl)
	sentinel := errors.New("cassandra unreachable")
	reader.EXPECT().GetReactionsByMessageIDs(gomock.Any(), gomock.Any()).Return(nil, sentinel)

	svc := &HistoryService{msgReader: reader}
	err := svc.hydrateReactions(context.Background(), []models.Message{
		{MessageID: "m1"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}
