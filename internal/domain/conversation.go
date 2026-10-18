package domain

import (
	"time"

	"github.com/google/uuid"
)

type ConversationType int

const (
	UnspecifiedConversation ConversationType = iota
	PrivateConversation
	GroupConversation
)

type Conversation struct {
	ID        uuid.UUID
	Type      ConversationType
	Count     int64
	CreatedAt time.Time
}
