package integrationsuite

import (
	"testing"

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
