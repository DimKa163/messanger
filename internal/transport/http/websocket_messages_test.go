package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/security"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

func TestWebSocketOnlineExchange(t *testing.T) {
	h := NewWebSocket()
	tokens, _ := security.NewJWT(websocketTestSecret, time.Hour)
	server := httptest.NewServer(NewRouter(nil, nil, tokens, h))
	t.Cleanup(server.Close)
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	aliceID, bobID := uuid.New(), uuid.New()
	alice := connectOnlineClient(t, ctx, server.URL, tokens, aliceID)
	bob := connectOnlineClient(t, ctx, server.URL, tokens, bobID)
	bobOther := connectOnlineClient(t, ctx, server.URL, tokens, bobID)
	message := incomingMessage{Type: "message", RecipientID: bobID, Content: "Привет, Боб!"}
	if err := wsjson.Write(ctx, alice, message); err != nil {
		t.Fatal(err)
	}
	ack := readOnlineEvent(t, ctx, alice)
	if ack.Type != "sent" || ack.RecipientID != bobID.String() {
		t.Fatalf("sender acknowledgment=%+v", ack)
	}
	for _, conn := range []*websocket.Conn{bob, bobOther} {
		event := readOnlineEvent(t, ctx, conn)
		if event.Type != "message" || event.SenderID != aliceID.String() || event.RecipientID != bobID.String() || event.Content != message.Content {
			t.Fatalf("recipient event=%+v", event)
		}
	}
	if err := wsjson.Write(ctx, bob, incomingMessage{Type: "message", RecipientID: aliceID, Content: "Привет!"}); err != nil {
		t.Fatal(err)
	}
	if ack := readOnlineEvent(t, ctx, bob); ack.Type != "sent" {
		t.Fatalf("reply acknowledgment=%+v", ack)
	}
	if event := readOnlineEvent(t, ctx, alice); event.SenderID != bobID.String() || event.Content != "Привет!" {
		t.Fatalf("reply event=%+v", event)
	}
	if err := wsjson.Write(ctx, alice, incomingMessage{Type: "message", RecipientID: uuid.New(), Content: "Offline"}); err != nil {
		t.Fatal(err)
	}
	if event := readOnlineEvent(t, ctx, alice); event.Type != "error" || event.Code != "recipient_offline" {
		t.Fatalf("offline event=%+v", event)
	}
}

func TestWebSocketRejectsInvalidMessagesAndContinuesReading(t *testing.T) {
	h := NewWebSocket()
	tokens, _ := security.NewJWT(websocketTestSecret, time.Hour)
	server := httptest.NewServer(NewRouter(nil, nil, tokens, h))
	t.Cleanup(server.Close)
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	senderID, recipientID := uuid.New(), uuid.New()
	sender := connectOnlineClient(t, ctx, server.URL, tokens, senderID)
	recipient := connectOnlineClient(t, ctx, server.URL, tokens, recipientID)
	valid := fmt.Sprintf(`{"type":"message","recipient_id":%q,"content":"valid"}`, recipientID)
	for _, tc := range []struct {
		name string
		typ  websocket.MessageType
		data string
	}{
		{"malformed", websocket.MessageText, "{"},
		{"multiple JSON values", websocket.MessageText, valid + " {}"},
		{"missing recipient", websocket.MessageText, `{"type":"message","content":"text"}`},
		{"invalid recipient", websocket.MessageText, `{"type":"message","recipient_id":"bad","content":"text"}`},
		{"empty content", websocket.MessageText, strings.Replace(valid, "valid", "   ", 1)},
		{"long content", websocket.MessageText, strings.Replace(valid, "valid", strings.Repeat("a", 4097), 1)},
		{"unknown type", websocket.MessageText, strings.Replace(valid, "message", "other", 1)},
		{"spoofed sender", websocket.MessageText, strings.TrimSuffix(valid, "}") + `,"sender_id":"` + uuid.NewString() + `"}`},
		{"binary", websocket.MessageBinary, valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := sender.Write(ctx, tc.typ, []byte(tc.data)); err != nil {
				t.Fatal(err)
			}
			if event := readOnlineEvent(t, ctx, sender); event.Type != "error" || event.Code != "invalid_message" {
				t.Fatalf("invalid input event=%+v", event)
			}
		})
	}
	if err := sender.Write(ctx, websocket.MessageText, []byte(valid)); err != nil {
		t.Fatal(err)
	}
	if event := readOnlineEvent(t, ctx, sender); event.Type != "sent" {
		t.Fatalf("valid input after errors=%+v", event)
	}
	if event := readOnlineEvent(t, ctx, recipient); event.Content != "valid" || event.SenderID != senderID.String() {
		t.Fatalf("invalid input reached recipient or valid input was lost: %+v", event)
	}
}

func TestWebSocketDeliveryIsBoundedAndTargetsOnlyRecipient(t *testing.T) {
	h := NewWebSocket()
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	recipientID := uuid.New()
	newClient := func(id uuid.UUID) *webSocketClient {
		client := &webSocketClient{userID: id, ctx: context.Background(), outbound: make(chan webSocketEvent, 1)}
		if !h.register(client) {
			t.Fatal("client registration failed")
		}
		return client
	}
	slow, fast, unrelated := newClient(recipientID), newClient(recipientID), newClient(uuid.New())
	slow.outbound <- webSocketEvent{Type: "existing"}
	message := incomingMessage{Type: "message", RecipientID: recipientID, Content: "text"}
	if code := h.deliver(uuid.New(), message); code != "" || len(fast.outbound) != 1 || len(unrelated.outbound) != 0 {
		t.Fatalf("delivery code=%q, recipient queue=%d, unrelated queue=%d", code, len(fast.outbound), len(unrelated.outbound))
	}
	if code := h.deliver(uuid.New(), message); code != "recipient_busy" {
		t.Fatalf("full queues code=%q", code)
	}
	h.unregister(slow)
	h.unregister(fast)
	if code := h.deliver(uuid.New(), message); code != "recipient_offline" {
		t.Fatalf("disconnected recipient code=%q", code)
	}
	if _, exists := h.clients[recipientID]; exists {
		t.Fatal("disconnected recipient remains registered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	canceled := newClient(recipientID)
	canceled.ctx = ctx
	cancel()
	if code := h.deliver(uuid.New(), message); code != "recipient_offline" {
		t.Fatalf("canceled recipient code=%q", code)
	}
}

func connectOnlineClient(t *testing.T, ctx context.Context, serverURL string, tokens *security.JWT, id uuid.UUID) *websocket.Conn {
	t.Helper()
	token, err := tokens.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token.Value}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if event := readOnlineEvent(t, ctx, conn); event.Type != "connected" || event.UserID != id.String() {
		t.Fatalf("connected event=%+v", event)
	}
	return conn
}

func readOnlineEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) webSocketEvent {
	t.Helper()
	var event webSocketEvent
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatal(err)
	}
	return event
}
