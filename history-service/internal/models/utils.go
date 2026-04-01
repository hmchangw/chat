package models

import (
	"fmt"
	"reflect"

	"github.com/gocql/gocql"
)

// unmarshalUDTField uses the `cql` struct tag to find the field matching the
// Cassandra column name and unmarshals data into it. Unknown names are ignored.
func unmarshalUDTField(ptr any, name string, info gocql.TypeInfo, data []byte) error {
	v := reflect.ValueOf(ptr).Elem()
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("cql") == name {
			return gocql.Unmarshal(info, data, v.Field(i).Addr().Interface())
		}
	}
	return nil
}

// marshalUDTField uses the `cql` struct tag to find the field matching the
// Cassandra column name and marshals its value. Unknown names return (nil, nil).
func marshalUDTField(ptr any, name string, info gocql.TypeInfo) ([]byte, error) {
	v := reflect.ValueOf(ptr).Elem()
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("cql") == name {
			return gocql.Marshal(info, v.Field(i).Interface())
		}
	}
	return nil, nil
}

// verifyUDTTags panics at init time if any struct field is missing a `cql` tag.
// Call this in an init() block for each UDT type to catch tag typos early.
func verifyUDTTags(samplePtr any) {
	t := reflect.TypeOf(samplePtr).Elem()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("cql") == "" {
			panic(fmt.Sprintf("models: struct %s field %s is missing a `cql` tag", t.Name(), f.Name))
		}
	}
}
