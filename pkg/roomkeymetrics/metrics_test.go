package roomkeymetrics_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/roomkeymetrics"
)

func TestMetrics_AreNonNil(t *testing.T) {
	require.NotNil(t, roomkeymetrics.FanoutErrors)
	require.NotNil(t, roomkeymetrics.RPCDuration)
	require.NotNil(t, roomkeymetrics.KeyGenerated)
	require.NotNil(t, roomkeymetrics.KeyRotated)
	require.NotNil(t, roomkeymetrics.ValkeyErrors)
}
