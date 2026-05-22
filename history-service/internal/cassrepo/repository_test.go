package cassrepo

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/msgbucket"
)

func TestNewRepository_ReactionsConcurrencyClampsToMinimum(t *testing.T) {
	bucket := msgbucket.New(1)
	for _, in := range []int{-1, 0} {
		repo := NewRepository(nil, bucket, 0, in)
		assert.Equal(t, 1, repo.reactionsConcurrency, "in=%d", in)
	}
	repo := NewRepository(nil, bucket, 0, 50)
	assert.Equal(t, 50, repo.reactionsConcurrency)
}
