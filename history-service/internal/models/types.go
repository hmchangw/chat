package models

import "github.com/hmchangw/chat/pkg/model"

// Re-export UDT types from the shared model package so existing code
// within history-service continues to compile without import changes.
type (
	Participant = model.Participant
	File        = model.File
	Card        = model.Card
	CardAction  = model.CardAction
)
