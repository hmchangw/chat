package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidate_AllReferenced(t *testing.T) {
	c := &Catalog{
		Verbs:    []Verb{{Name: "nats_request", Executor: "NATSRequestExecutor"}},
		Readers:  []Reader{{Location: "mongo.rooms", Owners: []string{"room-service"}, Executor: "MongoRoomsReader"}},
		Services: []Service{{Name: "room-service"}},
	}
	knownExecutors := map[string]bool{
		"NATSRequestExecutor": true,
		"MongoRoomsReader":    true,
	}
	knownServices := map[string]bool{"room-service": true}

	errs := Validate(c, knownExecutors, knownServices)
	assert.Empty(t, errs)
}

func TestValidate_UnknownExecutor(t *testing.T) {
	c := &Catalog{
		Verbs: []Verb{{Name: "nats_request", Executor: "NonexistentExecutor"}},
	}
	errs := Validate(c, map[string]bool{}, map[string]bool{})
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0], "NonexistentExecutor")
}

func TestValidate_ReaderOwnerNotAService(t *testing.T) {
	c := &Catalog{
		Readers:  []Reader{{Location: "mongo.rooms", Owners: []string{"not-a-service"}, Executor: "X"}},
		Services: []Service{{Name: "room-service"}},
	}
	knownExecutors := map[string]bool{"X": true}
	knownServices := map[string]bool{"room-service": true}
	errs := Validate(c, knownExecutors, knownServices)
	assert.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "not-a-service")
}
