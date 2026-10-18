package domain

import (
	"time"

	"github.com/google/uuid"
)

type Message struct {
	ID              uuid.UUID `json:"id"`
	ConversationID  uuid.UUID `json:"conversation_id"`
	SenderID        uuid.UUID `json:"sender_id"`
	ClientMessageID uuid.UUID `json:"client_message_id"`
	Sequence        int64     `json:"sequence"`
	Content         []byte    `json:"content"`
	CreatedAt       time.Time `json:"created_at"`
}
