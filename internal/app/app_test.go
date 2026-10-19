package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/security"
	httptransport "github.com/DimKa163/messanger/internal/transport/http"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

func TestServeWaitsForActiveRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	releaseRequest := sync.OnceFunc(func() { close(release) })
	defer releaseRequest()
	a := testApp(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			t.Error("active request was canceled before graceful shutdown completed")
		}
	}), time.Second)
	listener := testListener(t)
	stopping := make(chan struct{})
	a.server.RegisterOnShutdown(func() { close(stopping) })
	finished := make(chan error, 1)
	go func() { finished <- a.serve(ctx, listener) }()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			err = resp.Body.Close()
		}
		response <- err
	}()
	awaitSignal(t, started)
	cancel()
	awaitSignal(t, stopping)
	select {
	case err := <-finished:
		t.Fatalf("server returned before request finished: %v", err)
	default:
	}
	releaseRequest()
	if err := awaitResult(t, response); err != nil {
		t.Fatal(err)
	}
	if err := awaitResult(t, finished); err != nil {
		t.Fatal(err)
	}
}

func TestServeCancelsRequestWhenShutdownExpires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	requestCanceled := make(chan struct{})
	a := testApp(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(requestCanceled)
	}), 10*time.Millisecond)
	listener := testListener(t)
	finished := make(chan error, 1)
	go func() { finished <- a.serve(ctx, listener) }()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
		response <- err
	}()
	awaitSignal(t, started)
	cancel()
	if err := awaitResult(t, finished); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v; want deadline exceeded", err)
	}
	awaitSignal(t, requestCanceled)
	awaitResult(t, response)
}

func TestServeClosesWebSocketsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tokens, err := security.NewJWT("test-secret-with-at-least-32-bytes", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := tokens.Issue(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	ws := httptransport.NewWebSocket()
	t.Cleanup(func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := ws.Shutdown(shutdownCtx); err != nil {
			t.Error(err)
		}
	})
	a := testApp(httptransport.NewRouter(nil, nil, tokens, ws), time.Second)
	a.websocket = ws
	listener := testListener(t)
	finished := make(chan error, 1)
	go func() { finished <- a.serve(ctx, listener) }()
	clientCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	conn, _, err := websocket.Dial(clientCtx, "ws://"+listener.Addr().String()+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token.Value}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var event map[string]string
	if err := wsjson.Read(clientCtx, conn, &event); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := awaitResult(t, finished); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(clientCtx); err == nil || clientCtx.Err() != nil {
		t.Fatalf("WebSocket was not closed by server shutdown: %v", err)
	}
}

func testApp(handler http.Handler, timeout time.Duration) *App {
	return &App{
		server:          &http.Server{Handler: handler},
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		shutdownTimeout: timeout,
	}
}

func testListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
	}
}

func awaitResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server operation")
		return nil
	}
}
