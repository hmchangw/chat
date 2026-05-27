// Package cassandra holds Cassandra-only row and UDT carriers.
// No bson tags — these types never reach MongoDB (Mongo-bound types live in pkg/model).
package cassandra

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrDuplicateReactionKey is returned when UnmarshalJSON encounters
// two records with the same (emoji, userAccount) pair.
var ErrDuplicateReactionKey = errors.New("duplicate reaction key")

// Participant maps to the Cassandra "Participant" UDT.
// cql tags are required because gocql lowercases Go field names (e.g. "EngName" → "engname"), not snake_case.
type Participant struct {
	ID          string `json:"id"                    cql:"id"`
	EngName     string `json:"engName,omitempty"     cql:"eng_name"`
	CompanyName string `json:"companyName,omitempty" cql:"company_name"`
	AppID       string `json:"appId,omitempty"       cql:"app_id"`
	AppName     string `json:"appName,omitempty"     cql:"app_name"`
	IsBot       bool   `json:"isBot,omitempty"       cql:"is_bot"`
	Account     string `json:"account,omitempty"     cql:"account"`
}

// File maps to the Cassandra "File" UDT.
type File struct {
	ID   string `json:"id"   cql:"id"`
	Name string `json:"name" cql:"name"`
	Type string `json:"type" cql:"type"`
}

// Card maps to the Cassandra "Card" UDT.
type Card struct {
	Template string `json:"template"       cql:"template"`
	Data     []byte `json:"data,omitempty" cql:"data"`
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

// QuotedParentMessage maps to the Cassandra "QuotedParentMessage" UDT.
type QuotedParentMessage struct {
	MessageID   string        `json:"messageId"             cql:"message_id"`
	RoomID      string        `json:"roomId"                cql:"room_id"`
	Sender      Participant   `json:"sender"                cql:"sender"`
	CreatedAt   time.Time     `json:"createdAt"             cql:"created_at"`
	Msg         string        `json:"msg,omitempty"         cql:"msg"`
	Mentions    []Participant `json:"mentions,omitempty"    cql:"mentions"`
	Attachments [][]byte      `json:"attachments,omitempty" cql:"attachments"`
	MessageLink string        `json:"messageLink,omitempty" cql:"message_link"`
	// ThreadParentID and ThreadParentCreatedAt are set by message-worker when the quoted message is a TShow reply,
	// embedding the parent's identity so history-service can enforce access-window checks without an extra read.
	ThreadParentID        string     `json:"threadParentId,omitempty"        cql:"thread_parent_id"`
	ThreadParentCreatedAt *time.Time `json:"threadParentCreatedAt,omitempty" cql:"thread_parent_created_at"`
}

// ReactionKey is the map-key UDT for Message.Reactions.
type ReactionKey struct {
	// Emoji must be NFC-normalised by writers; enforcement is in the message gatekeeper, not this type.
	Emoji       string `json:"emoji"       cql:"emoji"`
	UserAccount string `json:"userAccount" cql:"user_account"`
}

// ReactorInfo is the map-value UDT for Message.Reactions.
type ReactorInfo struct {
	UserID    string    `json:"userId"    cql:"user_id"`
	EngName   string    `json:"engName"   cql:"eng_name"`
	ChnName   string    `json:"chnName"   cql:"chn_name"`
	Account   string    `json:"account"   cql:"account"`
	ReactedAt time.Time `json:"reactedAt" cql:"reacted_at"`
}

// Reactions is the in-row reaction map. Keys are unique (emoji, user_account) pairs; JSON is a flat array sorted for stable output.
type Reactions map[ReactionKey]ReactorInfo

type reactionEntry struct {
	Emoji       string    `json:"emoji"`
	UserAccount string    `json:"userAccount"`
	UserID      string    `json:"userId"`
	EngName     string    `json:"engName"`
	ChnName     string    `json:"chnName"`
	Account     string    `json:"account"`
	ReactedAt   time.Time `json:"reactedAt"`
}

// MarshalJSON emits a flat array sorted by (emoji, userAccount); nil → "null", empty → "[]".
func (r Reactions) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	if len(r) == 0 {
		return []byte("[]"), nil
	}
	entries := make([]reactionEntry, 0, len(r))
	for k, v := range r {
		entries = append(entries, reactionEntry{
			Emoji:       k.Emoji,
			UserAccount: k.UserAccount,
			UserID:      v.UserID,
			EngName:     v.EngName,
			ChnName:     v.ChnName,
			Account:     v.Account,
			ReactedAt:   v.ReactedAt,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Emoji != entries[j].Emoji {
			return entries[i].Emoji < entries[j].Emoji
		}
		return entries[i].UserAccount < entries[j].UserAccount
	})
	return json.Marshal(entries)
}

// UnmarshalJSON parses the flat array into the map; null → nil, [] → empty map, duplicates → error.
func (r *Reactions) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*r = nil
		return nil
	}
	var entries []reactionEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal reactions array: %w", err)
	}
	out := make(Reactions, len(entries))
	for _, e := range entries {
		key := ReactionKey{Emoji: e.Emoji, UserAccount: e.UserAccount}
		if _, ok := out[key]; ok {
			return fmt.Errorf("reactions: duplicate key (%s, %s): %w", e.Emoji, e.UserAccount, ErrDuplicateReactionKey)
		}
		out[key] = ReactorInfo{
			UserID:    e.UserID,
			EngName:   e.EngName,
			ChnName:   e.ChnName,
			Account:   e.Account,
			ReactedAt: e.ReactedAt,
		}
	}
	*r = out
	return nil
}

// Message represents a row in the Cassandra message tables (messages_by_room, messages_by_id, thread_messages_by_room).
// cql tags are used by history-service/internal/cassrepo.structScan to map columns to fields by name.
type Message struct {
	RoomID                string               `json:"roomId"                          cql:"room_id"`
	Bucket                int64                `json:"-"                                cql:"bucket"`
	CreatedAt             time.Time            `json:"createdAt"                       cql:"created_at"`
	MessageID             string               `json:"messageId"                       cql:"message_id"`
	Sender                Participant          `json:"sender"                          cql:"sender"`
	Msg                   string               `json:"msg"                             cql:"msg"`
	Mentions              []Participant        `json:"mentions,omitempty"              cql:"mentions"`
	Attachments           [][]byte             `json:"attachments,omitempty"           cql:"attachments"`
	File                  *File                `json:"file,omitempty"                  cql:"file"`
	Card                  *Card                `json:"card,omitempty"                  cql:"card"`
	CardAction            *CardAction          `json:"cardAction,omitempty"            cql:"card_action"`
	TShow                 bool                 `json:"tshow,omitempty"                 cql:"tshow"`
	TCount                *int                 `json:"tcount,omitempty"                cql:"tcount"`
	ThreadParentID        string               `json:"threadParentId,omitempty"        cql:"thread_parent_id"`
	ThreadParentCreatedAt *time.Time           `json:"threadParentCreatedAt,omitempty" cql:"thread_parent_created_at"`
	QuotedParentMessage   *QuotedParentMessage `json:"quotedParentMessage,omitempty"   cql:"quoted_parent_message"`
	VisibleTo             string               `json:"visibleTo,omitempty"             cql:"visible_to"`
	// Reactions is nil when absent (omitted from JSON); not modified by edit/delete paths.
	Reactions    Reactions    `json:"reactions,omitempty"             cql:"reactions"`
	Deleted      bool         `json:"deleted,omitempty"               cql:"deleted"`
	Type         string       `json:"type,omitempty"                  cql:"type"`
	SysMsgData   []byte       `json:"sysMsgData,omitempty"            cql:"sys_msg_data"`
	SiteID       string       `json:"siteId,omitempty"                cql:"site_id"`
	EditedAt     *time.Time   `json:"editedAt,omitempty"              cql:"edited_at"`
	UpdatedAt    *time.Time   `json:"updatedAt,omitempty"             cql:"updated_at"`
	ThreadRoomID string       `json:"threadRoomId,omitempty"          cql:"thread_room_id"`
	PinnedAt     *time.Time   `json:"pinnedAt,omitempty"              cql:"pinned_at"`
	PinnedBy     *Participant `json:"pinnedBy,omitempty"              cql:"pinned_by"`
}
