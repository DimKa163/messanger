package domain

import (
	"time"

	"github.com/google/uuid"
)

type Member struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	UserID         uuid.UUID `json:"user_id"`
	JoinedAt       time.Time `json:"joined_at"`
}
