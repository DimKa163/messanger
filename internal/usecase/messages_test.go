package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/google/uuid"
)

type consumerRepository struct {
	MessageRepository
	message    domain.Message
	recipients []uuid.UUID
	marks      int
	markError  error
}

func (r *consumerRepository) ForDelivery(context.Context, uuid.UUID) (domain.Message, []uuid.UUID, error) {
	return r.message, r.recipients, nil
}

func (r *consumerRepository) MarkDelivered(context.Context, uuid.UUID) error {
	r.marks++
	return r.markError
}

type consumerDelivery struct {
	sent            map[uuid.UUID]bool
	errors          map[uuid.UUID]error
	receiptUser     uuid.UUID
	receiptType     string
	receiptClientID uuid.UUID
	receiptError    error
}

func (d *consumerDelivery) DeliverMessage(_ context.Context, user uuid.UUID, _ domain.Message) (bool, error) {
	return d.sent[user], d.errors[user]
}

func (d *consumerDelivery) DeliverReceipt(_ context.Context, user uuid.UUID, typ string, clientID uuid.UUID) error {
	d.receiptUser, d.receiptType, d.receiptClientID = user, typ, clientID
	return d.receiptError
}

func TestConsumeMessageRecordsOnlySuccessfulDelivery(t *testing.T) {
	bob, carol := uuid.New(), uuid.New()
	writeFailure, databaseFailure := errors.New("write failure"), errors.New("database failure")
	for _, tc := range []struct {
		name      string
		sent      map[uuid.UUID]bool
		failures  map[uuid.UUID]error
		markError error
		wantMarks int
		wantError error
	}{
		{name: "offline"},
		{name: "online", sent: map[uuid.UUID]bool{bob: true}, wantMarks: 1},
		{name: "failed write", failures: map[uuid.UUID]error{bob: writeFailure}, wantError: writeFailure},
		{name: "partial group delivery", sent: map[uuid.UUID]bool{bob: true}, failures: map[uuid.UUID]error{carol: writeFailure}, wantMarks: 1, wantError: writeFailure},
		{name: "delivery persistence failed", sent: map[uuid.UUID]bool{bob: true}, markError: databaseFailure, wantMarks: 1, wantError: databaseFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &consumerRepository{message: domain.Message{ID: uuid.New()}, recipients: []uuid.UUID{bob, carol}, markError: tc.markError}
			delivery := &consumerDelivery{sent: tc.sent, errors: tc.failures}
			err := NewMessages(repo).Consume(context.Background(), MessageEvent{Type: MessageSend, MessageID: repo.message.ID}, delivery)
			if !errors.Is(err, tc.wantError) || repo.marks != tc.wantMarks {
				t.Fatalf("marks=%d err=%v", repo.marks, err)
			}
		})
	}
}

func TestConsumeReceiptsUsePersistedSenderAndClientID(t *testing.T) {
	now := time.Now()
	for _, typ := range []string{MessageDelivered, MessageRead} {
		t.Run(typ, func(t *testing.T) {
			m := domain.Message{ID: uuid.New(), SenderID: uuid.New(), ClientMessageID: uuid.New(), DeliveredAt: &now, ReadAt: &now}
			repo := &consumerRepository{message: m}
			delivery := &consumerDelivery{}
			event := MessageEvent{Type: typ, MessageID: m.ID, SenderID: uuid.New(), ClientMessageID: uuid.New()}
			if err := NewMessages(repo).Consume(context.Background(), event, delivery); err != nil {
				t.Fatal(err)
			}
			if delivery.receiptUser != m.SenderID || delivery.receiptClientID != m.ClientMessageID || delivery.receiptType != typ {
				t.Fatalf("receipt=%+v", delivery)
			}
			repo.message.DeliveredAt, repo.message.ReadAt = nil, nil
			if err := NewMessages(repo).Consume(context.Background(), event, delivery); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("premature receipt: %v", err)
			}
		})
	}
}

func TestMessagesRejectInvalidInputBeforeStorage(t *testing.T) {
	// An absent repository makes any unexpected storage call fail the test.
	service := NewMessages(nil)
	actor := uuid.New()
	valid := SendMessageInput{ConversationID: uuid.New(), SenderID: actor, ClientMessageID: uuid.New(), MessageType: domain.TextMessage, Content: "text"}
	spoofed := valid
	spoofed.SenderID = uuid.New()
	if _, err := service.Send(context.Background(), actor, spoofed); !errors.Is(err, ErrMessageForbidden) {
		t.Fatalf("sender spoof: %v", err)
	}
	for _, ids := range [][]uuid.UUID{nil, {uuid.Nil}, make([]uuid.UUID, 101)} {
		if err := service.Read(context.Background(), actor, ids); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid batch: %v", err)
		}
	}
	if err := service.Consume(context.Background(), MessageEvent{Type: "unknown", MessageID: uuid.New()}, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown event: %v", err)
	}
}
