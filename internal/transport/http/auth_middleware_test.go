package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type tokenVerifierFunc func(string) (uuid.UUID, error)

func (f tokenVerifierFunc) Verify(token string) (uuid.UUID, error) { return f(token) }

func TestAuthenticatePassesIdentityToAnyHandler(t *testing.T) {
	userID := uuid.New()
	verified := 0
	verifier := tokenVerifierFunc(func(token string) (uuid.UUID, error) {
		verified++
		if token != "access-token" {
			t.Fatalf("token=%q", token)
		}
		return userID, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	handler := Authenticate(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		id, ok := UserIDFromContext(r.Context())
		if !ok || id != userID {
			t.Fatalf("identity=%v, present=%v", id, ok)
		}
		cancel()
		if r.Context().Err() != context.Canceled {
			t.Fatal("middleware lost request cancellation")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(http.MethodGet, "/protected", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer access-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !called || verified != 1 || w.Code != http.StatusNoContent {
		t.Fatalf("handler called=%v, verifications=%d, status=%d", called, verified, w.Code)
	}
	if _, ok := UserIDFromContext(r.Context()); ok {
		t.Fatal("middleware changed the original request context")
	}
}

func TestAuthenticateRejectsInvalidIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier TokenVerifier
	}{
		{"missing verifier", nil},
		{"verification error", tokenVerifierFunc(func(string) (uuid.UUID, error) {
			return uuid.New(), errors.New("private verification details")
		})},
		{"empty user ID", tokenVerifierFunc(func(string) (uuid.UUID, error) { return uuid.Nil, nil })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := Authenticate(tc.verifier)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("unauthorized request reached the handler")
			}))
			r := httptest.NewRequest(http.MethodGet, "/protected", nil)
			r.Header.Set("Authorization", "Bearer token")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized || w.Body.String() != "{\"error\":\"unauthorized\"}\n" {
				t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
			}
		})
	}
}
