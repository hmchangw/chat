package atrest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

func TestSplitMessage_AllFields(t *testing.T) {
	in := &cassandra.Message{
		RoomID:      "r1",
		MessageID:   "m1",
		Msg:         "hello",
		Attachments: [][]byte{[]byte("a")},
		Card:        &cassandra.Card{Template: "t", Data: []byte("c")},
		SysMsgData:  []byte("sys"),
		QuotedParentMessage: &cassandra.QuotedParentMessage{
			MessageID:   "p",
			Msg:         "parent body",
			Attachments: [][]byte{[]byte("pa")},
		},
	}
	enc := SplitForEncryption(in)
	assert.Equal(t, "hello", enc.Msg)
	assert.Equal(t, [][]byte{[]byte("a")}, enc.Attachments)
	assert.Equal(t, "t", enc.Card.Template)
	assert.NotNil(t, enc.QuotedParentContent)
	assert.Equal(t, "parent body", enc.QuotedParentContent.Msg)

	// SplitForEncryption MUST NOT mutate the input.
	assert.Equal(t, "hello", in.Msg)
	assert.NotNil(t, in.QuotedParentMessage)
	assert.Equal(t, "parent body", in.QuotedParentMessage.Msg)
}

func TestApplyDecryptedFields_Restores(t *testing.T) {
	target := &cassandra.Message{
		RoomID:    "r1",
		MessageID: "m1",
		QuotedParentMessage: &cassandra.QuotedParentMessage{
			MessageID: "p", // metadata preserved by reader
		},
	}
	// sys_msg_data is set on the row (plaintext column) and must be left
	// untouched by ApplyDecryptedFields since it is not part of the bundle.
	target.SysMsgData = []byte("sys")
	enc := &EncryptedFields{
		Msg:         "hello",
		Attachments: [][]byte{[]byte("a")},
		Card:        &cassandra.Card{Template: "t", Data: []byte("c")},
		QuotedParentContent: &QuotedParentEncrypted{
			Msg:         "parent body",
			Attachments: [][]byte{[]byte("pa")},
		},
	}
	ApplyDecryptedFields(target, enc)
	assert.Equal(t, "hello", target.Msg)
	assert.Equal(t, "t", target.Card.Template)
	assert.Equal(t, []byte("sys"), target.SysMsgData)
	assert.Equal(t, "parent body", target.QuotedParentMessage.Msg)
	assert.Equal(t, [][]byte{[]byte("pa")}, target.QuotedParentMessage.Attachments)
	// metadata preserved
	assert.Equal(t, "p", target.QuotedParentMessage.MessageID)
}

func TestStripEncryptedFields_NullsContent(t *testing.T) {
	in := &cassandra.Message{
		Msg:         "hello",
		Attachments: [][]byte{[]byte("a")},
		Card:        &cassandra.Card{Template: "t"},
		SysMsgData:  []byte("sys"),
		QuotedParentMessage: &cassandra.QuotedParentMessage{
			MessageID:   "p",
			Msg:         "parent body",
			Attachments: [][]byte{[]byte("pa")},
		},
	}
	StripEncryptedFields(in)
	assert.Empty(t, in.Msg)
	assert.Nil(t, in.Attachments)
	assert.Nil(t, in.Card)
	// sys_msg_data is not encrypted, so Strip leaves it on the plaintext column.
	assert.Equal(t, []byte("sys"), in.SysMsgData)
	// quoted_parent_message kept, body fields nulled
	assert.Equal(t, "p", in.QuotedParentMessage.MessageID)
	assert.Empty(t, in.QuotedParentMessage.Msg)
	assert.Nil(t, in.QuotedParentMessage.Attachments)
}
