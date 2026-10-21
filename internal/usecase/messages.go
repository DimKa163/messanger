package usecase

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/google/uuid"
)

var (
	ErrMessageForbidden = errors.New("message access denied")
	ErrMessageConflict  = errors.New("client message ID reused with different data")
	ErrDeliveryBusy     = errors.New("online delivery unavailable")
)

const (
	MessageSend      = "message.send"
	MessageDelivered = "message.delivered"
	MessageRead      = "message.readed"
)

// MessageEvent is the outbox payload and the future Kafka consumer contract.
// MessageID identifies a persisted message; consumers reload authoritative data.
type MessageEvent struct {
	Type            string             `json:"type"`
	MessageID       uuid.UUID          `json:"message_id"`
	ConversationID  uuid.UUID          `json:"conversation_id"`
	SenderID        uuid.UUID          `json:"sender_id"`
	ClientMessageID uuid.UUID          `json:"client_message_id"`
	MessageType     domain.MessageType `json:"message_type"`
	Content         string             `json:"content"`
}

func EventForMessage(typ string, m domain.Message) MessageEvent {
	return MessageEvent{Type: typ, MessageID: m.ID, ConversationID: m.ConversationID,
		SenderID: m.SenderID, ClientMessageID: m.ClientMessageID, MessageType: m.MessageType, Content: string(m.Content)}
}

type MessageRepository interface {
	Save(context.Context, domain.Message) (domain.Message, error)
	ForDelivery(context.Context, uuid.UUID) (domain.Message, []uuid.UUID, error)
	MarkDelivered(context.Context, uuid.UUID) error
	MarkRead(context.Context, uuid.UUID, []uuid.UUID) error
}

// OnlineDelivery reports success only after a socket write succeeds, never on enqueue.
type OnlineDelivery interface {
	DeliverMessage(context.Context, uuid.UUID, domain.Message) (bool, error)
	DeliverReceipt(context.Context, uuid.UUID, string, uuid.UUID) error
}

type Messages struct{ repo MessageRepository }

func NewMessages(repo MessageRepository) *Messages { return &Messages{repo: repo} }

type SendMessageInput struct {
	ConversationID  uuid.UUID
	SenderID        uuid.UUID
	ClientMessageID uuid.UUID
	MessageType     domain.MessageType
	Content         string
}

func (s *Messages) Send(ctx context.Context, actor uuid.UUID, in SendMessageInput) (domain.Message, error) {
	if actor == uuid.Nil || in.SenderID != actor {
		return domain.Message{}, ErrMessageForbidden
	}
	if in.ConversationID == uuid.Nil || in.ClientMessageID == uuid.Nil || in.MessageType != domain.TextMessage ||
		!utf8.ValidString(in.Content) || strings.ContainsRune(in.Content, 0) || len(in.Content) > 4096 || strings.TrimSpace(in.Content) == "" {
		return domain.Message{}, ErrInvalidInput
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return domain.Message{}, err
	}
	return s.repo.Save(ctx, domain.Message{ID: id, ConversationID: in.ConversationID, SenderID: actor,
		ClientMessageID: in.ClientMessageID, MessageType: in.MessageType, Content: []byte(in.Content)})
}

func (s *Messages) Read(ctx context.Context, actor uuid.UUID, ids []uuid.UUID) error {
	if actor == uuid.Nil || len(ids) == 0 || len(ids) > 100 {
		return ErrInvalidInput
	}
	unique := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			return ErrInvalidInput
		}
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	return s.repo.MarkRead(ctx, actor, unique)
}

// Consume is called by the future Kafka adapter. Return errors for retry; broker
// redelivery can repeat message.new, which clients deduplicate by message_id.
func (s *Messages) Consume(ctx context.Context, event MessageEvent, delivery OnlineDelivery) error {
	if event.MessageID == uuid.Nil || (event.Type != MessageSend && event.Type != MessageDelivered && event.Type != MessageRead) {
		return ErrInvalidInput
	}
	m, recipients, err := s.repo.ForDelivery(ctx, event.MessageID)
	if err != nil {
		return err
	}
	switch event.Type {
	case MessageSend:
		var sent bool
		var deliveryErr error
		for _, id := range recipients {
			ok, err := delivery.DeliverMessage(ctx, id, m)
			sent = sent || ok
			deliveryErr = errors.Join(deliveryErr, err)
			// TODO: send push to offline recipients and count successful push as delivery.
		}
		if sent {
			deliveryErr = errors.Join(deliveryErr, s.repo.MarkDelivered(ctx, m.ID))
		}
		return deliveryErr
	case MessageDelivered:
		if m.DeliveredAt == nil {
			return ErrInvalidInput
		}
	case MessageRead:
		if m.ReadAt == nil {
			return ErrInvalidInput
		}
	}
	return delivery.DeliverReceipt(ctx, m.SenderID, event.Type, m.ClientMessageID)
}
