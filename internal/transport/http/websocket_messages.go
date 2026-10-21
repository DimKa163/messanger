package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type MessageService interface {
	Send(context.Context, uuid.UUID, usecase.SendMessageInput) (domain.Message, error)
	Read(context.Context, uuid.UUID, []uuid.UUID) error
}

type webSocketClient struct {
	userID   uuid.UUID
	ctx      context.Context
	conn     *websocket.Conn
	outbound chan socketWrite
}

type socketWrite struct {
	event webSocketEvent
	ctx   context.Context
	done  chan error // buffered; the writer never waits for a canceled consumer
}

type webSocketEvent struct {
	Type            string             `json:"type"`
	UserID          string             `json:"user_id,omitempty"`
	SenderID        string             `json:"sender_id,omitempty"`
	ConversationID  string             `json:"conversation_id,omitempty"`
	MessageID       string             `json:"message_id,omitempty"`
	ClientMessageID string             `json:"client_message_id,omitempty"`
	MessageType     domain.MessageType `json:"message_type,omitempty"`
	Content         string             `json:"content,omitempty"`
	Sequence        int64              `json:"sequence,omitempty"`
	MessageIDs      []uuid.UUID        `json:"message_ids,omitempty"`
	Code            string             `json:"code,omitempty"`
}

type incomingMessage struct {
	Type            string             `json:"type"`
	ConversationID  uuid.UUID          `json:"conversation_id"`
	SenderID        uuid.UUID          `json:"sender_id"`
	ClientMessageID uuid.UUID          `json:"client_message_id"`
	MessageType     domain.MessageType `json:"message_type"`
	Content         string             `json:"content"`
}

type incomingRead struct {
	Type       string      `json:"type"`
	MessageIDs []uuid.UUID `json:"message_ids"`
}

func (h *WebSocket) register(client *webSocketClient) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping {
		return false
	}
	if h.clients[client.userID] == nil {
		h.clients[client.userID] = make(map[*webSocketClient]struct{})
	}
	h.clients[client.userID][client] = struct{}{}
	return true
}

func (h *WebSocket) unregister(client *webSocketClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients[client.userID], client)
	if len(h.clients[client.userID]) == 0 {
		delete(h.clients, client.userID)
	}
}

func (h *WebSocket) DeliverMessage(ctx context.Context, user uuid.UUID, m domain.Message) (bool, error) {
	return h.deliver(ctx, user, webSocketEvent{Type: "message.new", SenderID: m.SenderID.String(),
		ConversationID: m.ConversationID.String(), MessageID: m.ID.String(), MessageType: m.MessageType, Content: string(m.Content)})
}

func (h *WebSocket) DeliverReceipt(ctx context.Context, user uuid.UUID, typ string, clientID uuid.UUID) error {
	_, err := h.deliver(ctx, user, webSocketEvent{Type: typ, ClientMessageID: clientID.String()})
	return err
}

// deliver enqueues to every live session without waiting under the hub lock.
// Success means an actual socket write in at least one session. Offline users
// return false; full queues and failed writes return an error for broker retry.
func (h *WebSocket) deliver(ctx context.Context, user uuid.UUID, event webSocketEvent) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type pendingWrite struct {
		client *webSocketClient
		done   chan error
	}
	var pending []pendingWrite
	online := false
	h.mu.Lock()
	for client := range h.clients[user] {
		if client.ctx.Err() != nil {
			continue
		}
		online = true
		done := make(chan error, 1)
		select {
		case client.outbound <- socketWrite{event: event, ctx: ctx, done: done}:
			pending = append(pending, pendingWrite{client: client, done: done})
		default:
		}
	}
	h.mu.Unlock()
	if !online {
		return false, nil
	}
	sent := false
	for _, p := range pending {
		select {
		case err := <-p.done:
			sent = sent || err == nil
		case <-p.client.ctx.Done():
			// A completed write may race with a later disconnect.
			select {
			case err := <-p.done:
				sent = sent || err == nil
			default:
			}
		case <-ctx.Done():
			// Include completed writes from other sessions before reporting failure.
			for _, other := range pending {
				select {
				case err := <-other.done:
					sent = sent || err == nil
				default:
				}
			}
			if sent {
				return true, nil
			}
			return false, errors.Join(usecase.ErrDeliveryBusy, ctx.Err())
		}
	}
	if sent {
		return true, nil
	}
	return false, usecase.ErrDeliveryBusy
}

func decodeSocketMessage(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return usecase.ErrInvalidInput
	}
	return nil
}

func (h *WebSocket) readMessages(client *webSocketClient) {
	for {
		typ, data, err := client.conn.Read(client.ctx)
		if err != nil {
			return
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if typ != websocket.MessageText || !utf8.Valid(data) || json.Unmarshal(data, &envelope) != nil {
			err = usecase.ErrInvalidInput
		} else {
			err = h.handleMessage(client, envelope.Type, data)
		}
		if err != nil && !client.enqueue(webSocketEvent{Type: "error", Code: messageErrorCode(err)}) {
			return
		}
	}
}

func (h *WebSocket) handleMessage(client *webSocketClient, typ string, data []byte) error {
	ctx, cancel := context.WithTimeout(client.ctx, 10*time.Second)
	defer cancel()
	if h.messages == nil {
		return errors.New("message service unavailable")
	}
	switch typ {
	case usecase.MessageSend:
		var in incomingMessage
		if decodeSocketMessage(data, &in) != nil {
			return usecase.ErrInvalidInput
		}
		m, err := h.messages.Send(ctx, client.userID, usecase.SendMessageInput{
			ConversationID: in.ConversationID, SenderID: in.SenderID, ClientMessageID: in.ClientMessageID,
			MessageType: in.MessageType, Content: in.Content})
		if err != nil {
			return err
		}
		if !client.enqueue(webSocketEvent{Type: "message.accepted", MessageID: m.ID.String(),
			ClientMessageID: m.ClientMessageID.String(), ConversationID: m.ConversationID.String(), Sequence: m.Sequence}) {
			return client.ctx.Err()
		}
	case usecase.MessageRead:
		var in incomingRead
		if decodeSocketMessage(data, &in) != nil {
			return usecase.ErrInvalidInput
		}
		if err := h.messages.Read(ctx, client.userID, in.MessageIDs); err != nil {
			return err
		}
		if !client.enqueue(webSocketEvent{Type: "message.readed.accepted", MessageIDs: in.MessageIDs}) {
			return client.ctx.Err()
		}
	default:
		return usecase.ErrInvalidInput
	}
	return nil
}

func messageErrorCode(err error) string {
	switch {
	case errors.Is(err, usecase.ErrInvalidInput):
		return "invalid_message"
	case errors.Is(err, usecase.ErrMessageForbidden):
		return "forbidden"
	case errors.Is(err, usecase.ErrMessageConflict):
		return "idempotency_conflict"
	default:
		return "internal_error"
	}
}

func (client *webSocketClient) enqueue(event webSocketEvent) bool {
	select {
	case client.outbound <- socketWrite{event: event}:
		return true
	case <-client.ctx.Done():
		return false
	}
}
