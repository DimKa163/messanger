package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/security"
	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

type socketMessageRepository struct {
	mu         sync.Mutex
	message    domain.Message
	recipient  uuid.UUID
	deliveries int
	reads      int
}

func (r *socketMessageRepository) Save(_ context.Context, m domain.Message) (domain.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.message.ID != uuid.Nil {
		if string(r.message.Content) != string(m.Content) {
			return domain.Message{}, usecase.ErrMessageConflict
		}
		return r.message, nil
	}
	m.Sequence, m.CreatedAt = 1, time.Now()
	r.message = m
	return m, nil
}
func (r *socketMessageRepository) ForDelivery(context.Context, uuid.UUID) (domain.Message, []uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.message
	return m, []uuid.UUID{r.recipient}, nil
}
func (r *socketMessageRepository) MarkDelivered(context.Context, uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.message.DeliveredAt == nil {
		now := time.Now()
		r.message.DeliveredAt = &now
		r.deliveries++
	}
	return nil
}
func (r *socketMessageRepository) MarkRead(_ context.Context, actor uuid.UUID, ids []uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if actor != r.recipient || len(ids) != 1 || ids[0] != r.message.ID {
		return usecase.ErrMessageForbidden
	}
	if r.message.ReadAt == nil {
		now := time.Now()
		r.message.ReadAt = &now
		r.reads++
	}
	return nil
}

func TestWebSocketPersistAndBrokerEvents(t *testing.T) {
	aliceID, bobID := uuid.New(), uuid.New()
	repo := &socketMessageRepository{recipient: bobID}
	service := usecase.NewMessages(repo)
	h := NewWebSocket(service)
	tokens, _ := security.NewJWT(websocketTestSecret, time.Hour)
	server := httptest.NewServer(NewRouter(nil, nil, tokens, h))
	t.Cleanup(server.Close)
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	alice := connectOnlineClient(t, ctx, server.URL, tokens, aliceID)
	bob := connectOnlineClient(t, ctx, server.URL, tokens, bobID)
	bobOther := connectOnlineClient(t, ctx, server.URL, tokens, bobID)
	in := incomingMessage{Type: usecase.MessageSend, ConversationID: uuid.New(), SenderID: aliceID,
		ClientMessageID: uuid.New(), MessageType: domain.TextMessage, Content: "Привет!"}
	if err := wsjson.Write(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	ack := readOnlineEvent(t, ctx, alice)
	if ack.Type != "message.accepted" || ack.ClientMessageID != in.ClientMessageID.String() || ack.Sequence != 1 || ack.MessageID == "" {
		t.Fatalf("ack=%+v", ack)
	}
	if err := wsjson.Write(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	if repeat := readOnlineEvent(t, ctx, alice); repeat.MessageID != ack.MessageID {
		t.Fatalf("repeat=%+v", repeat)
	}
	m, _, _ := repo.ForDelivery(ctx, uuid.Nil)
	if m.DeliveredAt != nil {
		t.Fatal("message marked delivered before broker event")
	}
	if err := service.Consume(ctx, usecase.EventForMessage(usecase.MessageSend, m), h); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{bob, bobOther} {
		event := readOnlineEvent(t, ctx, conn)
		if event.Type != "message.new" || event.MessageID != ack.MessageID || event.SenderID != aliceID.String() || event.ConversationID != in.ConversationID.String() || event.Content != in.Content || event.MessageType != domain.TextMessage {
			t.Fatalf("new=%+v", event)
		}
	}
	m, _, _ = repo.ForDelivery(ctx, m.ID)
	if m.DeliveredAt == nil {
		t.Fatal("successful socket write did not record delivery")
	}
	if err := service.Consume(ctx, usecase.EventForMessage(usecase.MessageDelivered, m), h); err != nil {
		t.Fatal(err)
	}
	if event := readOnlineEvent(t, ctx, alice); event.Type != usecase.MessageDelivered || event.ClientMessageID != in.ClientMessageID.String() {
		t.Fatalf("delivered=%+v", event)
	}
	if err := wsjson.Write(ctx, bob, incomingRead{Type: usecase.MessageRead, MessageIDs: []uuid.UUID{m.ID, m.ID}}); err != nil {
		t.Fatal(err)
	}
	if event := readOnlineEvent(t, ctx, bob); event.Type != "message.readed.accepted" {
		t.Fatalf("read ack=%+v", event)
	}
	if err := service.Consume(ctx, usecase.EventForMessage(usecase.MessageRead, m), h); err != nil {
		t.Fatal(err)
	}
	if event := readOnlineEvent(t, ctx, alice); event.Type != usecase.MessageRead || event.ClientMessageID != in.ClientMessageID.String() {
		t.Fatalf("read receipt=%+v", event)
	}
	// Retransmitted broker events remain safe; clients deduplicate message.new by ID.
	if err := service.Consume(ctx, usecase.EventForMessage(usecase.MessageSend, m), h); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{bob, bobOther} {
		_ = readOnlineEvent(t, ctx, conn)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.deliveries != 1 || repo.reads != 1 {
		t.Fatalf("deliveries=%d reads=%d", repo.deliveries, repo.reads)
	}
}

func TestWebSocketRejectsInvalidMessagesAndContinuesReading(t *testing.T) {
	senderID := uuid.New()
	repo := &socketMessageRepository{}
	h := NewWebSocket(usecase.NewMessages(repo))
	tokens, _ := security.NewJWT(websocketTestSecret, time.Hour)
	server := httptest.NewServer(NewRouter(nil, nil, tokens, h))
	t.Cleanup(server.Close)
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sender := connectOnlineClient(t, ctx, server.URL, tokens, senderID)
	valid := fmt.Sprintf(`{"type":"message.send","conversation_id":%q,"sender_id":%q,"client_message_id":%q,"message_type":"text","content":"valid"}`, uuid.New(), senderID, uuid.New())
	for _, tc := range []struct {
		name       string
		typ        websocket.MessageType
		data, code string
	}{
		{"malformed", websocket.MessageText, "{", "invalid_message"},
		{"multiple values", websocket.MessageText, valid + " {}", "invalid_message"},
		{"unknown field", websocket.MessageText, strings.TrimSuffix(valid, "}") + `,"recipient_id":"` + uuid.NewString() + `"}`, "invalid_message"},
		{"missing conversation", websocket.MessageText, strings.Replace(valid, `"conversation_id":"`, `"conversation_id":"bad`, 1), "invalid_message"},
		{"empty content", websocket.MessageText, strings.Replace(valid, "valid", "   ", 1), "invalid_message"},
		{"long content", websocket.MessageText, strings.Replace(valid, "valid", strings.Repeat("a", 4097), 1), "invalid_message"},
		{"NUL", websocket.MessageText, strings.Replace(valid, "valid", `\u0000`, 1), "invalid_message"},
		{"unsupported type", websocket.MessageText, strings.Replace(valid, `"text"`, `"image"`, 1), "invalid_message"},
		{"unknown command", websocket.MessageText, strings.Replace(valid, "message.send", "message.new", 1), "invalid_message"},
		{"spoofed sender", websocket.MessageText, strings.Replace(valid, senderID.String(), uuid.NewString(), 1), "forbidden"},
		{"binary", websocket.MessageBinary, valid, "invalid_message"},
		{"empty read", websocket.MessageText, `{"type":"message.readed","message_ids":[]}`, "invalid_message"},
		{"legacy read keys", websocket.MessageText, `{"type":"message.readed","client_message_ids":[]}`, "invalid_message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := sender.Write(ctx, tc.typ, []byte(tc.data)); err != nil {
				t.Fatal(err)
			}
			if event := readOnlineEvent(t, ctx, sender); event.Type != "error" || event.Code != tc.code {
				t.Fatalf("event=%+v", event)
			}
		})
	}
	if err := sender.Write(ctx, websocket.MessageText, []byte(valid)); err != nil {
		t.Fatal(err)
	}
	if event := readOnlineEvent(t, ctx, sender); event.Type != "message.accepted" {
		t.Fatalf("valid after errors=%+v", event)
	}
	conflict := strings.Replace(valid, "valid", "changed", 1)
	if err := sender.Write(ctx, websocket.MessageText, []byte(conflict)); err != nil {
		t.Fatal(err)
	}
	if event := readOnlineEvent(t, ctx, sender); event.Code != "idempotency_conflict" {
		t.Fatalf("conflict=%+v", event)
	}
}

func TestWebSocketDeliveryWaitsForWriteAndSkipsFullQueues(t *testing.T) {
	h := NewWebSocket()
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	recipient := uuid.New()
	newClient := func(id uuid.UUID) *webSocketClient {
		c := &webSocketClient{userID: id, ctx: context.Background(), outbound: make(chan socketWrite, 1)}
		h.register(c)
		return c
	}
	slow, fast, unrelated := newClient(recipient), newClient(recipient), newClient(uuid.New())
	slow.outbound <- socketWrite{event: webSocketEvent{Type: "existing"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		ok, err := h.deliver(ctx, recipient, webSocketEvent{Type: "message.new"})
		if !ok && err == nil {
			err = errors.New("not sent")
		}
		result <- err
	}()
	var queued socketWrite
	select {
	case queued = <-fast.outbound:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-result:
		t.Fatal("enqueue was incorrectly considered delivery")
	default:
	}
	if len(unrelated.outbound) != 0 {
		t.Fatal("event reached unrelated user")
	}
	queued.done <- nil
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	fast.outbound <- socketWrite{}
	if ok, err := h.deliver(ctx, recipient, webSocketEvent{}); ok || !errors.Is(err, usecase.ErrDeliveryBusy) {
		t.Fatalf("full queues: ok=%v err=%v", ok, err)
	}
	h.unregister(slow)
	h.unregister(fast)
	if ok, err := h.deliver(ctx, recipient, webSocketEvent{}); ok || err != nil {
		t.Fatalf("offline: ok=%v err=%v", ok, err)
	}
	failed := newClient(recipient)
	go func() { item := <-failed.outbound; item.done <- errors.New("write failed") }()
	if ok, err := h.deliver(ctx, recipient, webSocketEvent{}); ok || err == nil {
		t.Fatalf("failed write: ok=%v err=%v", ok, err)
	}
}

func connectOnlineClient(t *testing.T, ctx context.Context, serverURL string, tokens *security.JWT, id uuid.UUID) *websocket.Conn {
	t.Helper()
	token, err := tokens.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token.Value}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if event := readOnlineEvent(t, ctx, conn); event.Type != "connected" || event.UserID != id.String() {
		t.Fatalf("connected=%+v", event)
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
