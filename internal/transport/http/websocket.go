package http

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

// WebSocket owns connection lifetimes independently of HTTP request contexts.
type WebSocket struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	stopping bool
	active   sync.WaitGroup
	done     chan struct{}
	clients  map[uuid.UUID]map[*webSocketClient]struct{}
}

func NewWebSocket() *WebSocket {
	ctx, cancel := context.WithCancel(context.Background())
	return &WebSocket{
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		clients: make(map[uuid.UUID]map[*webSocketClient]struct{}),
	}
}

func (h *WebSocket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFromContext(r.Context())
	if !ok {
		writeUnauthorized(w)
		return
	}

	// Add and the transition to stopping share a lock, so Shutdown cannot miss
	// a handshake in progress or race Wait against a new Add.
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "server stopping"})
		return
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()

	// Default origin checks permit only same-host browser connections.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 * 1024)
	ctx, cancelConnection := context.WithCancel(h.ctx)
	defer cancelConnection()
	client := &webSocketClient{userID: userID, ctx: ctx, conn: conn, outbound: make(chan webSocketEvent, 64)}
	if !h.register(client) {
		return
	}
	defer h.unregister(client)

	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = wsjson.Write(writeCtx, conn, webSocketEvent{Type: "connected", UserID: userID.String()})
	cancel()
	if err != nil {
		return
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancelConnection()
		h.readMessages(client)
	}()
	defer func() {
		cancelConnection()
		conn.CloseNow()
		<-readerDone
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-client.outbound:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(writeCtx, conn, event)
			cancel()
			if err != nil {
				return
			}
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// Shutdown rejects new handshakes, closes connections and waits for handlers.
func (h *WebSocket) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	if !h.stopping {
		h.stopping = true
		h.cancel()
		go func() {
			h.active.Wait()
			close(h.done)
		}()
	}
	h.mu.Unlock()
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
