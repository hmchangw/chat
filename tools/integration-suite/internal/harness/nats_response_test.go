package harness

import (
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
)

func TestParseNATSReplyError_EmptyBodyIsNotAnError(t *testing.T) {
	assert.Equal(t, "", ParseNATSReplyError(nil))
	assert.Equal(t, "", ParseNATSReplyError([]byte{}))
}

func TestParseNATSReplyError_SuccessBodyHasNoError(t *testing.T) {
	assert.Equal(t, "", ParseNATSReplyError([]byte(`{"id":"r1","name":"general"}`)))
}

func TestParseNATSReplyError_ErrorBodyReturnsMessage(t *testing.T) {
	got := ParseNATSReplyError([]byte(`{"error":"room not found"}`))
	assert.Equal(t, "room not found", got)
}

func TestParseNATSReplyError_MalformedJSONReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", ParseNATSReplyError([]byte(`{not json`)))
}

func TestMapNATSTransportError_Nil(t *testing.T) {
	assert.Equal(t, ClassNone, MapNATSTransportError(nil))
}

func TestMapNATSTransportError_NoResponders(t *testing.T) {
	assert.Equal(t, ClassRouteNotFound, MapNATSTransportError(nats.ErrNoResponders))
}

func TestMapNATSTransportError_Timeout(t *testing.T) {
	assert.Equal(t, ClassTimeout, MapNATSTransportError(nats.ErrTimeout))
}

func TestMapNATSTransportError_ConnectionClosed(t *testing.T) {
	assert.Equal(t, ClassUnreachable, MapNATSTransportError(nats.ErrConnectionClosed))
}

func TestMapNATSTransportError_Unknown(t *testing.T) {
	assert.Equal(t, ClassUnclassified, MapNATSTransportError(errors.New("some other err")))
}
