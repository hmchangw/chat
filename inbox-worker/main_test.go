package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDispatchAckPolicy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ackAction
	}{
		{"nil → ack", nil, ackActionAck},
		{"permanent → ack", newPermanent("bad"), ackActionAck},
		{"wrapped permanent → ack", fmt.Errorf("ctx: %w", newPermanent("bad")), ackActionAck},
		{"transient → nak", errors.New("mongo timeout"), ackActionNak},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dispatchAckPolicy(tt.err))
		})
	}
}
