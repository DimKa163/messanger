package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/security"
	"github.com/DimKa163/messanger/internal/usecase"
)

type memoryUsers struct {
	mu    sync.Mutex
	users map[string]domain.User
}

func (m *memoryUsers) Create(_ context.Context, user domain.User) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.users[user.Login]; exists {
		return domain.User{}, usecase.ErrLoginTaken
	}
	user.CreatedAt = time.Now().UTC()
	m.users[user.Login] = user
	return user, nil
}

func (m *memoryUsers) ByLogin(_ context.Context, login string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[login]
	if !ok {
		return domain.User{}, usecase.ErrUserNotFound
	}
	return user, nil
}

func TestAuthHTTPRegistrationAndLogin(t *testing.T) {
	users := &memoryUsers{users: make(map[string]domain.User)}
	tokens, err := security.NewJWT("test-secret-with-at-least-32-bytes", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := usecase.NewAuth(context.Background(), users, security.NewArgon2id(), tokens)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil, auth)
	registerJSON := `{"name":"Alice","login":"alice","password":"correct password"}`
	registered := authRequest(router, "/auth/register", registerJSON)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", registered.Code, registered.Body.String())
	}
	for _, forbidden := range []string{"password", "salt", "argon2", "correct password"} {
		if strings.Contains(registered.Body.String(), forbidden) {
			t.Fatalf("registration response exposes %q", forbidden)
		}
	}
	var public struct{ ID string }
	if err := json.Unmarshal(registered.Body.Bytes(), &public); err != nil || public.ID == "" {
		t.Fatal("registration did not return a user ID")
	}
	stored := users.users["alice"]
	if !strings.HasPrefix(string(stored.Password), "$argon2id$") || len(stored.Salt) != 16 {
		t.Fatal("registration did not persist an Argon2id hash and salt")
	}
	loggedIn := authRequest(router, "/auth/login", `{"login":"alice","password":"correct password"}`)
	if loggedIn.Code != http.StatusOK || loggedIn.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("login: %d %s", loggedIn.Code, loggedIn.Body.String())
	}
	var response struct {
		AccessToken string    `json:"access_token"`
		TokenType   string    `json:"token_type"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(loggedIn.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id, err := tokens.Verify(response.AccessToken)
	if err != nil || id.String() != public.ID || response.TokenType != "Bearer" || !response.ExpiresAt.After(time.Now()) {
		t.Fatalf("invalid access token response: subject=%v, error=%v", id, err)
	}
	wrong := authRequest(router, "/auth/login", `{"login":"alice","password":"wrong password"}`)
	missing := authRequest(router, "/auth/login", `{"login":"missing","password":"wrong password"}`)
	if wrong.Code != http.StatusUnauthorized || missing.Code != wrong.Code || missing.Body.String() != wrong.Body.String() {
		t.Fatal("unknown users and wrong passwords must have identical unauthorized responses")
	}
	duplicate := authRequest(router, "/auth/register", registerJSON)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate registration: %d", duplicate.Code)
	}
}

type failingAuth struct{ err error }

func (s failingAuth) Register(context.Context, usecase.RegisterInput) (domain.User, error) {
	return domain.User{}, s.err
}

func (s failingAuth) Login(context.Context, string, string) (usecase.AccessToken, error) {
	return usecase.AccessToken{}, s.err
}

func TestAuthHTTPErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{usecase.ErrInvalidInput, http.StatusBadRequest},
		{usecase.ErrLoginTaken, http.StatusConflict},
		{usecase.ErrInvalidCredentials, http.StatusUnauthorized},
		{usecase.ErrBusy, http.StatusTooManyRequests},
		{errors.New("private database password"), http.StatusInternalServerError},
	} {
		for _, path := range []string{"/auth/register", "/auth/login"} {
			response := authRequest(NewRouter(nil, failingAuth{tc.err}), path, `{}`)
			if response.Code != tc.status || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body.String())
			}
			if tc.err == usecase.ErrBusy && response.Header().Get("Retry-After") == "" {
				t.Fatal("overload response must tell the client to retry later")
			}
		}
	}
}

func TestAuthHTTPRejectsInvalidRequests(t *testing.T) {
	for _, path := range []string{"/auth/register", "/auth/login"} {
		for _, tc := range []struct {
			body, contentType string
			status            int
		}{
			{`{}`, "", http.StatusUnsupportedMediaType},
			{`{}`, "text/plain", http.StatusUnsupportedMediaType},
			{``, "application/json", http.StatusBadRequest},
			{`{"login":`, "application/json", http.StatusBadRequest},
			{`{"unknown":"value"}`, "application/json", http.StatusBadRequest},
			{`{} {}`, "application/json", http.StatusBadRequest},
			{`{} trailing`, "application/json", http.StatusBadRequest},
			{`[]`, "application/json", http.StatusBadRequest},
			{`{"password":"` + strings.Repeat("a", 17*1024) + `"}`, "application/json", http.StatusRequestEntityTooLarge},
		} {
			// A nil service makes accidental processing of rejected input fail the test.
			router := NewRouter(nil, nil)
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%s: status=%d; want %d; body=%s", path, w.Code, tc.status, w.Body.String())
			}
		}
	}
}

func authRequest(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
