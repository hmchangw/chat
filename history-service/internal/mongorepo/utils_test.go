package mongorepo

import (
	"testing"
)

func TestNewCollection_Compiles(t *testing.T) {
	var _ *Collection[struct{ Name string }]
}

func TestCollection_TypeSafety(t *testing.T) {
	type User struct {
		ID   string `bson:"_id"`
		Name string `bson:"name"`
	}
	type Room struct {
		ID   string `bson:"_id"`
		Name string `bson:"name"`
	}
	var _ *Collection[User]
	var _ *Collection[Room]
}
