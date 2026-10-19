package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/security"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const websocketTestSecret = "test-secret-with-at-least-32-bytes"

func TestWebSocketRejectsUnauthorizedBeforeUpgrade(t *testing.T) {
	tokens, _ := security.NewJWT(websocketTestSecret, time.Hour)
	valid, _ := tokens.Issue(uuid.New())
	other, _ := security.NewJWT("a-different-secret-with-32-bytes!!", time.Hour)
	wrongKey, _ := other.Issue(uuid.New())
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer: "messanger", Audience: jwt.ClaimStrings{"messanger-api"}, Subject: uuid.NewString(),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}).SignedString([]byte(websocketTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	h := NewWebSocket()
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	router := NewRouter(nil, nil, tokens, h)
	for _, tc := range []struct {
		name    string
		headers []string
		query   string
	}{
		{name: "missing token"},
		{name: "query token only", query: "?access_token=" + valid.Value},
		{name: "wrong scheme", headers: []string{"Basic " + valid.Value}},
		{name: "empty bearer", headers: []string{"Bearer"}},
		{name: "extra fields", headers: []string{"Bearer " + valid.Value + " extra"}},
		{name: "malformed", headers: []string{"Bearer malformed.token"}},
		{name: "wrong signature", headers: []string{"Bearer " + wrongKey.Value}},
		{name: "expired", headers: []string{"Bearer " + expired}},
		{name: "duplicate headers", headers: []string{"Bearer " + valid.Value, "Bearer " + valid.Value}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws"+tc.query, nil)
			// A complete handshake ensures failure happens in authentication.
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Version", "13")
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			for _, value := range tc.headers {
				r.Header.Add("Authorization", value)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized || w.Header().Get("Upgrade") != "" {
				t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
			if w.Header().Get("WWW-Authenticate") != "Bearer" || w.Header().Get("Cache-Control") != "no-store" ||
				w.Body.String() != "{\"error\":\"unauthorized\"}\n" {
				t.Fatalf("unexpected unauthorized response: %v %s", w.Header(), w.Body.String())
			}
		})
	}
}

func TestWebSocketHandshakeAndShutdown(t *testing.T) {
	tokens, _ := security.NewJWT(websocketTestSecret, time.Hour)
	userID := uuid.New()
	token, _ := tokens.Issue(userID)
	h := NewWebSocket()
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	server := httptest.NewServer(NewRouter(nil, nil, tokens, h))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := http.Header{"Authorization": {"bearer " + token.Value}}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status=%d", resp.StatusCode)
	}
	var event struct {
		Type   string    `json:"type"`
		UserID uuid.UUID `json:"user_id"`
	}
	if err := wsjson.Read(ctx, conn, &event); err != nil || event.Type != "connected" || event.UserID != userID {
		t.Fatalf("connected event=%+v, error=%v", event, err)
	}
	readCtx := conn.CloseRead(ctx)
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("server did not handle ping: %v", err)
	}
	// Concurrent calls must close all handlers and reject later connections.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := h.Shutdown(ctx); err != nil {
				t.Errorf("shutdown: %v", err)
			}
		})
	}
	wg.Wait()
	select {
	case <-readCtx.Done():
	case <-ctx.Done():
		t.Fatal("connection remained open after shutdown")
	}
	_, resp, err = websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("connection after shutdown: response=%v, error=%v", resp, err)
	}
}

func TestWebSocketRejectsCrossOriginAndInvalidHandshake(t *testing.T) {
	tokens, _ := security.NewJWT(websocketTestSecret, time.Hour)
	token, _ := tokens.Issue(uuid.New())
	h := NewWebSocket()
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	server := httptest.NewServer(NewRouter(nil, nil, tokens, h))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token.Value}, "Origin": {"https://other.example"}},
	})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin handshake: response=%v, error=%v", resp, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer "+token.Value)
	w := httptest.NewRecorder()
	Authenticate(tokens)(h).ServeHTTP(w, r)
	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("accepted an ordinary HTTP request: %d", w.Code)
	}
}

func TestWebSocketRejectsMissingMiddlewareIdentity(t *testing.T) {
	h := NewWebSocket()
	t.Cleanup(func() { shutdownWebSocket(t, h) })
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("handler without authentication middleware: status=%d", w.Code)
	}
}

func shutdownWebSocket(t *testing.T, h *WebSocket) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
