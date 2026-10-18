package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var emailRegex = regexp.MustCompile(
	`^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`,
)

type Email string

func ParseEmail(email string) (Email, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", errors.New("empty email")
	}
	if !emailRegex.MatchString(email) {
		return "", errors.New("invalid email")
	}
	return Email(email), nil
}

type User struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Email     Email     `json:"-"`
	Login     string    `json:"login"`
	Password  []byte    `json:"-"`
	Salt      []byte    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}
