package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type webSocketClient struct {
	userID   uuid.UUID
	ctx      context.Context
	conn     *websocket.Conn
	outbound chan webSocketEvent
}

type webSocketEvent struct {
	Type        string `json:"type"`
	UserID      string `json:"user_id,omitempty"`
	SenderID    string `json:"sender_id,omitempty"`
	RecipientID string `json:"recipient_id,omitempty"`
	Content     string `json:"content,omitempty"`
	Code        string `json:"code,omitempty"`
}

type incomingMessage struct {
	Type        string    `json:"type"`
	RecipientID uuid.UUID `json:"recipient_id"`
	Content     string    `json:"content"`
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

// deliver enqueues to each live session without waiting on a slow recipient.
// Queues are never closed: cancellation owns the lifetime of their consumers.
func (h *WebSocket) deliver(senderID uuid.UUID, message incomingMessage) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	event := webSocketEvent{
		Type: "message", SenderID: senderID.String(),
		RecipientID: message.RecipientID.String(), Content: message.Content,
	}
	online, queued := false, false
	for client := range h.clients[message.RecipientID] {
		if client.ctx.Err() != nil {
			continue
		}
		online = true
		select {
		case client.outbound <- event:
			queued = true
		default:
		}
	}
	if queued {
		return ""
	}
	if online {
		return "recipient_busy"
	}
	return "recipient_offline"
}

func (h *WebSocket) readMessages(client *webSocketClient) {
	for {
		typ, data, err := client.conn.Read(client.ctx)
		if err != nil {
			return
		}
		var message incomingMessage
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if typ != websocket.MessageText || !utf8.Valid(data) || decoder.Decode(&message) != nil ||
			decoder.Decode(new(any)) != io.EOF || message.Type != "message" ||
			message.RecipientID == uuid.Nil || len(message.Content) > 4096 || strings.TrimSpace(message.Content) == "" {
			if !client.enqueue(webSocketEvent{Type: "error", Code: "invalid_message"}) {
				return
			}
			continue
		}
		code := h.deliver(client.userID, message)
		response := webSocketEvent{Type: "sent", RecipientID: message.RecipientID.String()}
		if code != "" {
			response.Type, response.Code = "error", code
		}
		if !client.enqueue(response) {
			return
		}
	}
}

func (client *webSocketClient) enqueue(event webSocketEvent) bool {
	select {
	case client.outbound <- event:
		return true
	case <-client.ctx.Done():
		return false
	}
}
