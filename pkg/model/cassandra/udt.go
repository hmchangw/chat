package cassandra

import (
	"fmt"
	"reflect"

	"github.com/gocql/gocql"
)

// unmarshalUDTField uses the `cql` struct tag to find the field matching the
// Cassandra column name and unmarshals data into it. Unknown names are ignored.
func unmarshalUDTField(ptr any, name string, info gocql.TypeInfo, data []byte) error {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("unmarshal UDT field %q: expected non-nil pointer, got %T", name, ptr)
	}
	v := rv.Elem()
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("unmarshal UDT field %q: expected pointer to struct, got pointer to %s", name, v.Kind())
	}
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("cql") == name {
			if err := gocql.Unmarshal(info, data, v.Field(i).Addr().Interface()); err != nil {
				return fmt.Errorf("unmarshal UDT field %q: %w", name, err)
			}
			return nil
		}
	}
	return nil
}

// marshalUDTField uses the `cql` struct tag to find the field matching the
// Cassandra column name and marshals its value. Unknown names return (nil, nil).
func marshalUDTField(ptr any, name string, info gocql.TypeInfo) ([]byte, error) {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil, fmt.Errorf("marshal UDT field %q: expected non-nil pointer, got %T", name, ptr)
	}
	v := rv.Elem()
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("marshal UDT field %q: expected pointer to struct, got pointer to %s", name, v.Kind())
	}
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("cql") == name {
			data, err := gocql.Marshal(info, v.Field(i).Interface())
			if err != nil {
				return nil, fmt.Errorf("marshal UDT field %q: %w", name, err)
			}
			return data, nil
		}
	}
	return nil, nil
}

// verifyUDTTags panics at init time if any struct field is missing a `cql` tag.
// Call this in an init() block for each UDT type to catch tag typos early.
func verifyUDTTags(samplePtr any) {
	rv := reflect.TypeOf(samplePtr)
	if rv.Kind() != reflect.Ptr {
		panic(fmt.Sprintf("cassandra: verifyUDTTags requires a pointer, got %T", samplePtr))
	}
	t := rv.Elem()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("cassandra: verifyUDTTags requires a pointer to struct, got pointer to %s", t.Kind()))
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("cql") == "" {
			panic(fmt.Sprintf("cassandra: struct %s field %s is missing a `cql` tag", t.Name(), f.Name))
		}
	}
}
