package natsrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrUnavailable_HasCodeAndMessage(t *testing.T) {
	err := ErrUnavailable("service busy")
	assert.Equal(t, "unavailable", err.Code)
	assert.Equal(t, "service busy", err.Message)
}

func TestCodeUnavailable_Constant(t *testing.T) {
	assert.Equal(t, "unavailable", CodeUnavailable)
}
