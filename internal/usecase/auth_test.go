package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/google/uuid"
)

type userStub struct {
	user domain.User
	err  error
}

func (s *userStub) Create(_ context.Context, user domain.User) (domain.User, error) {
	s.user = user
	user.CreatedAt = time.Now()
	return user, s.err
}

func (s *userStub) ByLogin(context.Context, string) (domain.User, error) { return s.user, s.err }

type passwordStub struct {
	hashed   string
	verified []byte
	match    bool
	err      error
}

func (s *passwordStub) Hash(_ context.Context, password string) ([]byte, []byte, error) {
	s.hashed = password
	return []byte("encoded hash"), []byte("random salt"), s.err
}

func (s *passwordStub) Verify(_ context.Context, _ string, hash []byte) (bool, error) {
	s.verified = hash
	return s.match, s.err
}

type tokenStub struct{ subject uuid.UUID }

func (s *tokenStub) Issue(id uuid.UUID) (AccessToken, error) {
	s.subject = id
	return AccessToken{Value: "signed token"}, nil
}

func TestRegisterHashesBeforePersistenceAndReturnsPublicUser(t *testing.T) {
	users, passwords, tokens := &userStub{}, &passwordStub{}, &tokenStub{}
	a, err := NewAuth(context.Background(), users, passwords, tokens)
	if err != nil {
		t.Fatal(err)
	}
	user, err := a.Register(context.Background(), RegisterInput{
		Name: " Alice ", Login: " alice ", Password: " password ", Email: " alice@example.com ",
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := users.user
	if stored.Name != "Alice" || stored.Login != "alice" || stored.Email != "alice@example.com" || stored.ID == uuid.Nil {
		t.Fatalf("unexpected persisted user: %+v", stored)
	}
	if passwords.hashed != " password " || string(stored.Password) != "encoded hash" || string(stored.Salt) != "random salt" {
		t.Fatal("password was changed or stored without hashing")
	}
	if user.ID != stored.ID || user.Password != nil || user.Salt != nil || user.CreatedAt.IsZero() {
		t.Fatal("registration must return a persisted public user without credentials")
	}
	users.err = ErrLoginTaken
	if _, err := a.Register(context.Background(), RegisterInput{Name: "Alice", Login: "alice", Password: "password"}); !errors.Is(err, ErrLoginTaken) {
		t.Fatalf("duplicate registration = %v", err)
	}
}

func TestRegisterRejectsInvalidDataBeforeHashing(t *testing.T) {
	valid := RegisterInput{Name: "Alice", Login: "alice", Password: "password"}
	for _, tc := range []struct {
		name   string
		change func(*RegisterInput)
	}{
		{"empty name", func(i *RegisterInput) { i.Name = " " }},
		{"long name", func(i *RegisterInput) { i.Name = strings.Repeat("Я", 251) }},
		{"nul name", func(i *RegisterInput) { i.Name = "a\x00b" }},
		{"short login", func(i *RegisterInput) { i.Login = "ab" }},
		{"invalid login", func(i *RegisterInput) { i.Login = "alice bob" }},
		{"long login", func(i *RegisterInput) { i.Login = strings.Repeat("a", 101) }},
		{"short password", func(i *RegisterInput) { i.Password = "1234567" }},
		{"short unicode password", func(i *RegisterInput) { i.Password = "абвг" }},
		{"long password", func(i *RegisterInput) { i.Password = strings.Repeat("a", 1025) }},
		{"invalid email", func(i *RegisterInput) { i.Email = "broken@" }},
		{"long email", func(i *RegisterInput) { i.Email = strings.Repeat("a", 100) + "@example.com" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.change(&input)
			users, passwords := &userStub{}, &passwordStub{}
			a, _ := NewAuth(context.Background(), users, passwords, &tokenStub{})
			passwords.hashed = ""
			if _, err := a.Register(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Register error = %v", err)
			}
			if passwords.hashed != "" || users.user.ID != uuid.Nil {
				t.Fatal("invalid input reached hashing or storage")
			}
		})
	}
}

func TestLoginIssuesTokenOnlyAfterPasswordVerification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repoErr error
		match   bool
		want    error
	}{
		{"success", nil, true, nil},
		{"wrong password", nil, false, ErrInvalidCredentials},
		{"unknown login", ErrUserNotFound, false, ErrInvalidCredentials},
		{"dummy must never authenticate", ErrUserNotFound, true, ErrInvalidCredentials},
		{"database unavailable", errors.New("database offline"), true, errors.New("database offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			users := &userStub{user: domain.User{ID: id, Password: []byte("user hash")}, err: tc.repoErr}
			passwords, tokens := &passwordStub{match: tc.match}, &tokenStub{}
			a, _ := NewAuth(context.Background(), users, passwords, tokens)
			token, err := a.Login(context.Background(), "alice", "password")
			if tc.want == nil {
				if err != nil || tokens.subject != id || token.Value == "" {
					t.Fatalf("Login = %+v, %v; token subject=%v", token, err, tokens.subject)
				}
			} else if err == nil || tokens.subject != uuid.Nil || token.Value != "" {
				t.Fatal("failed login issued a token or did not return an error")
			}
			if tc.want == ErrInvalidCredentials && !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("credentials error = %v", err)
			}
			if errors.Is(tc.repoErr, ErrUserNotFound) && string(passwords.verified) != "encoded hash" {
				t.Fatal("unknown user did not perform dummy password verification")
			}
		})
	}
}

func TestAuthPropagatesPasswordFailuresWithoutWritingOrIssuing(t *testing.T) {
	users, passwords, tokens := &userStub{}, &passwordStub{}, &tokenStub{}
	a, _ := NewAuth(context.Background(), users, passwords, tokens)
	passwords.err = ErrBusy
	if _, err := a.Register(context.Background(), RegisterInput{Name: "Alice", Login: "alice", Password: "password"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("Register = %v", err)
	}
	if _, err := a.Login(context.Background(), "alice", "password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Login = %v", err)
	}
	if users.user.ID != uuid.Nil || tokens.subject != uuid.Nil {
		t.Fatal("failed hashing wrote a user or issued a token")
	}
	if _, err := NewAuth(context.Background(), users, passwords, tokens); !errors.Is(err, ErrBusy) {
		t.Fatalf("NewAuth = %v", err)
	}
}
