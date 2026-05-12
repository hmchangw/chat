package integrationsuite

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
)

func TestClassifyHTTP_2xxIsNoError(t *testing.T) {
	assert.Equal(t, ClassNone, ClassifyHTTP(200, nil))
	assert.Equal(t, ClassNone, ClassifyHTTP(204, nil))
}

func TestClassifyHTTP_401And403AreAuth(t *testing.T) {
	assert.Equal(t, ClassAuth, ClassifyHTTP(401, nil))
	assert.Equal(t, ClassAuth, ClassifyHTTP(403, nil))
}

func TestClassifyHTTP_400IsValidation(t *testing.T) {
	assert.Equal(t, ClassValidation, ClassifyHTTP(400, nil))
}

func TestClassifyHTTP_404Or409IsHandlerError(t *testing.T) {
	assert.Equal(t, ClassHandlerError, ClassifyHTTP(404, nil))
	assert.Equal(t, ClassHandlerError, ClassifyHTTP(409, nil))
}

func TestClassifyHTTP_5xxIsDownstreamOrPersistence(t *testing.T) {
	// Default 5xx is Downstream. Body-shape upgrade to Persistence is tested separately.
	assert.Equal(t, ClassDownstream, ClassifyHTTP(500, nil))
	assert.Equal(t, ClassDownstream, ClassifyHTTP(502, nil))
}

func TestClassifyHTTP_5xxWithPersistenceBodyIsPersistence(t *testing.T) {
	body := []byte(`{"code":"DB_WRITE_FAILED","error":"mongo write conflict"}`)
	assert.Equal(t, ClassPersistence, ClassifyHTTP(500, body))
}

func TestClassifyHTTP_TimeoutHintInBodyIsTimeout(t *testing.T) {
	body := []byte(`{"code":"REQUEST_TIMEOUT"}`)
	assert.Equal(t, ClassTimeout, ClassifyHTTP(504, body))
}

func TestClassifyHTTP_UnknownIsUnclassified(t *testing.T) {
	assert.Equal(t, ClassUnclassified, ClassifyHTTP(418, nil))
}

func TestClassifyNATS_NilIsUnclassified(t *testing.T) {
	assert.Equal(t, ClassUnclassified, ClassifyNATS(nil))
}

func TestClassifyNATS_NoErrorIsNone(t *testing.T) {
	r := &LastResponse{Transport: "nats", Body: []byte(`{"id":"r1"}`)}
	assert.Equal(t, ClassNone, ClassifyNATS(r))
}

func TestClassifyNATS_TransportErrorDominates(t *testing.T) {
	r := &LastResponse{Transport: "nats", Err: nats.ErrTimeout, ErrorText: "should be ignored"}
	assert.Equal(t, ClassTimeout, ClassifyNATS(r))
}

func TestClassifyNATS_HandlerErrorTextHeuristics(t *testing.T) {
	cases := map[string]Class{
		"room not found":              ClassHandlerError,
		"mongo write failed":          ClassPersistence,
		"cassandra timeout":           ClassPersistence,
		"invalid roomID":              ClassValidation,
		"unauthorized: missing token": ClassAuth,
	}
	for text, want := range cases {
		got := ClassifyNATS(&LastResponse{Transport: "nats", ErrorText: text})
		assert.Equal(t, want, got, "text: %q", text)
	}
}

func TestLastResponse_Class_DispatchesByTransport(t *testing.T) {
	httpR := &LastResponse{Transport: "http", StatusCode: 404}
	assert.Equal(t, ClassHandlerError, httpR.Class())

	natsR := &LastResponse{Transport: "nats", Err: nats.ErrNoResponders}
	assert.Equal(t, ClassRouteNotFound, natsR.Class())
}

func TestLastResponse_Class_UnknownTransportIsUnclassified(t *testing.T) {
	r := &LastResponse{Transport: ""}
	assert.Equal(t, ClassUnclassified, r.Class())
}
