package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/google/uuid"
)

var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrLoginTaken         = errors.New("login already taken")
	ErrUserNotFound       = errors.New("user not found")
	ErrInvalidCredentials = errors.New("invalid login or password")
	ErrBusy               = errors.New("authentication capacity exceeded")
	loginPattern          = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{2,99}$`)
)

type UserRepository interface {
	Create(context.Context, domain.User) (domain.User, error)
	ByLogin(context.Context, string) (domain.User, error)
}

type PasswordHasher interface {
	Hash(context.Context, string) (encoded, salt []byte, err error)
	Verify(context.Context, string, []byte) (bool, error)
}

type AccessToken struct {
	Value     string
	ExpiresAt time.Time
}

type TokenIssuer interface {
	Issue(uuid.UUID) (AccessToken, error)
}

type Auth struct {
	users     UserRepository
	passwords PasswordHasher
	tokens    TokenIssuer
	dummyHash []byte
}

func NewAuth(ctx context.Context, users UserRepository, passwords PasswordHasher, tokens TokenIssuer) (*Auth, error) {
	// Unknown logins perform the same password verification as existing users.
	dummy, _, err := passwords.Hash(ctx, "dummy password for missing users")
	if err != nil {
		return nil, fmt.Errorf("initialize password verifier: %w", err)
	}
	return &Auth{users: users, passwords: passwords, tokens: tokens, dummyHash: dummy}, nil
}

type RegisterInput struct {
	Name     string
	Email    string
	Login    string
	Password string
}

func (a *Auth) Register(ctx context.Context, input RegisterInput) (domain.User, error) {
	name := strings.TrimSpace(input.Name)
	login := strings.TrimSpace(input.Login)
	if !validText(name, 250) || !loginPattern.MatchString(login) || !validPassword(input.Password) {
		return domain.User{}, ErrInvalidInput
	}
	var email domain.Email
	if value := strings.TrimSpace(input.Email); value != "" {
		var err error
		email, err = domain.ParseEmail(value)
		if err != nil || !validText(value, 100) {
			return domain.User{}, ErrInvalidInput
		}
	}
	hash, salt, err := a.passwords.Hash(ctx, input.Password)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return domain.User{}, fmt.Errorf("generate user ID: %w", err)
	}
	user, err := a.users.Create(ctx, domain.User{
		ID: id, Name: name, Email: email, Login: login, Password: hash, Salt: salt,
	})
	if err != nil {
		return domain.User{}, fmt.Errorf("register user: %w", err)
	}
	return publicUser(user), nil
}

func (a *Auth) Login(ctx context.Context, login, password string) (AccessToken, error) {
	login = strings.TrimSpace(login)
	if !loginPattern.MatchString(login) || !validPassword(password) {
		return AccessToken{}, ErrInvalidCredentials
	}
	user, err := a.users.ByLogin(ctx, login)
	missing := errors.Is(err, ErrUserNotFound)
	if err != nil && !missing {
		return AccessToken{}, fmt.Errorf("find user: %w", err)
	}
	hash := user.Password
	if missing {
		hash = a.dummyHash
	}
	match, err := a.passwords.Verify(ctx, password, hash)
	if err != nil {
		return AccessToken{}, fmt.Errorf("verify password: %w", err)
	}
	if missing || !match {
		return AccessToken{}, ErrInvalidCredentials
	}
	return a.tokens.Issue(user.ID)
}

func validPassword(password string) bool {
	return len(password) <= 1024 && utf8.ValidString(password) && utf8.RuneCountInString(password) >= 8
}

func validText(value string, max int) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= max
}

func publicUser(user domain.User) domain.User {
	user.Password = nil
	user.Salt = nil
	return user
}
