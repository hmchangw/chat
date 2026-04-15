package cassandra

import (
	"time"

	"github.com/gocql/gocql"
)

func init() {
	verifyUDTTags(&Participant{})
	verifyUDTTags(&File{})
	verifyUDTTags(&Card{})
	verifyUDTTags(&CardAction{})
	verifyUDTTags(&QuotedParentMessage{})
}

// Participant maps to the Cassandra "Participant" UDT.
type Participant struct {
	ID          string `json:"id"                    cql:"id"`
	EngName     string `json:"engName,omitempty"     cql:"eng_name"`
	CompanyName string `json:"companyName,omitempty" cql:"company_name"`
	AppID       string `json:"appId,omitempty"       cql:"app_id"`
	AppName     string `json:"appName,omitempty"     cql:"app_name"`
	IsBot       bool   `json:"isBot,omitempty"       cql:"is_bot"`
	Account     string `json:"account,omitempty"     cql:"account"`
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

// QuotedParentMessage maps to the Cassandra "QuotedParentMessage" UDT.
type QuotedParentMessage struct {
	MessageID   string        `json:"messageId"              cql:"message_id"`
	RoomID      string        `json:"roomId"                 cql:"room_id"`
	Sender      Participant   `json:"sender"                 cql:"sender"`
	CreatedAt   time.Time     `json:"createdAt"              cql:"created_at"`
	Msg         string        `json:"msg,omitempty"          cql:"msg"`
	Mentions    []Participant `json:"mentions,omitempty"     cql:"mentions"`
	Attachments [][]byte      `json:"attachments,omitempty"  cql:"attachments"`
	MessageLink string        `json:"messageLink,omitempty"  cql:"message_link"`
}

func (q *QuotedParentMessage) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(q, name, info, data)
}
func (q *QuotedParentMessage) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(q, name, info)
}

// Message represents a message row in the Cassandra message tables (messages_by_room, messages_by_id).
type Message struct {
	RoomID                string                   `json:"roomId"`
	CreatedAt             time.Time                `json:"createdAt"`
	MessageID             string                   `json:"messageId"`
	Sender                Participant              `json:"sender"`
	TargetUser            *Participant             `json:"targetUser,omitempty"`
	Msg                   string                   `json:"msg"`
	Mentions              []Participant            `json:"mentions,omitempty"`
	Attachments           [][]byte                 `json:"attachments,omitempty"`
	File                  *File                    `json:"file,omitempty"`
	Card                  *Card                    `json:"card,omitempty"`
	CardAction            *CardAction              `json:"cardAction,omitempty"`
	TShow                 bool                     `json:"tshow,omitempty"`
	ThreadParentID        string                   `json:"threadParentId,omitempty"`
	ThreadParentCreatedAt *time.Time               `json:"threadParentCreatedAt,omitempty"`
	QuotedParentMessage   *QuotedParentMessage     `json:"quotedParentMessage,omitempty"`
	VisibleTo             string                   `json:"visibleTo,omitempty"`
	Unread                bool                     `json:"unread,omitempty"`
	Reactions             map[string][]Participant `json:"reactions,omitempty"`
	Deleted               bool                     `json:"deleted,omitempty"`
	Type                  string                   `json:"type,omitempty"`
	SysMsgData            []byte                   `json:"sysMsgData,omitempty"`
	SiteID                string                   `json:"siteId,omitempty"`
	EditedAt              *time.Time               `json:"editedAt,omitempty"`
	UpdatedAt             *time.Time               `json:"updatedAt,omitempty"`
}
