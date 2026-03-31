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

func init() {
	verifyUDTTags(&Participant{})
	verifyUDTTags(&File{})
	verifyUDTTags(&Card{})
	verifyUDTTags(&CardAction{})
}

// Participant maps to the Cassandra "Participant" UDT.
type Participant struct {
	ID          string `json:"id"                    cql:"id"`
	UserName    string `json:"userName"              cql:"user_name"`
	EngName     string `json:"engName,omitempty"     cql:"eng_name"`
	CompanyName string `json:"companyName,omitempty" cql:"company_name"`
	AppID       string `json:"appId,omitempty"       cql:"app_id"`
	AppName     string `json:"appName,omitempty"     cql:"app_name"`
	IsBot       bool   `json:"isBot,omitempty"       cql:"is_bot"`
}

func (p *Participant) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(p, name, info, data)
}
func (p *Participant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(p, name, info)
}

// File maps to the Cassandra "File" UDT.
type File struct {
	ID   string `json:"id"   cql:"id"`
	Name string `json:"name" cql:"name"`
	Type string `json:"type" cql:"type"`
}

func (f *File) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(f, name, info, data)
}
func (f *File) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(f, name, info)
}

// Card maps to the Cassandra "Card" UDT.
type Card struct {
	Template string `json:"template"        cql:"template"`
	Data     []byte `json:"data,omitempty"  cql:"data"`
}

func (c *Card) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(c, name, info, data)
}
func (c *Card) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(c, name, info)
}

// CardAction maps to the Cassandra "CardAction" UDT.
type CardAction struct {
	Verb        string `json:"verb"                  cql:"verb"`
	Text        string `json:"text,omitempty"        cql:"text"`
	CardID      string `json:"cardId,omitempty"      cql:"card_id"`
	DisplayText string `json:"displayText,omitempty" cql:"display_text"`
	HideExecLog bool   `json:"hideExecLog,omitempty" cql:"hide_exec_log"`
	CardTmID    string `json:"cardTmId,omitempty"    cql:"card_tmid"`
	Data        []byte `json:"data,omitempty"        cql:"data"`
}

func (ca *CardAction) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(ca, name, info, data)
}
func (ca *CardAction) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(ca, name, info)
}
