package postgres

import (
	"context"
	"strings"
	"testing"
)

func TestNewPoolRejectsInvalidURLWithoutExposingPassword(t *testing.T) {
	const password = "private-password"
	pool, err := NewPool(context.Background(), "postgres://user:"+password+"@localhost:invalid/database")
	if pool != nil || err == nil {
		t.Fatal("invalid URL must return an error without a pool")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatal("parse error exposes the database password")
	}
}
