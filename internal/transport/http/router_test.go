package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestHealthDoesNotRequireDatabase(t *testing.T) {
	router := NewRouter(pingFunc(func(context.Context) error {
		t.Fatal("liveness must not access the database")
		return nil
	}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestReadiness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"available", nil, http.StatusOK},
		{"unavailable", errors.New("private database details"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			router := NewRouter(pingFunc(func(ctx context.Context) error {
				called = true
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 2*time.Second {
					t.Error("readiness query must have a bounded timeout")
				}
				return tc.err
			}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if !called || w.Code != tc.status {
				t.Fatalf("ping called=%v, status=%d; want %d", called, w.Code, tc.status)
			}
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal("readiness exposes database error details")
			}
		})
	}
}
