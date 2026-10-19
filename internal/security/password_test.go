package security

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/DimKa163/messanger/internal/usecase"
	"golang.org/x/crypto/argon2"
)

func TestArgon2idHashAndVerify(t *testing.T) {
	hasher := NewArgon2id()
	ctx := context.Background()
	hash, salt, err := hasher.Hash(ctx, "correct password")
	if err != nil {
		t.Fatal(err)
	}
	other, otherSalt, err := hasher.Hash(ctx, "correct password")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(hash, other) || bytes.Equal(salt, otherSalt) || len(salt) != 16 {
		t.Fatal("password hashes must have unique random salts")
	}
	if !strings.HasPrefix(string(hash), "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	// Independently check that the stored digest is Argon2id with the stored salt.
	parts := strings.Split(string(hash), "$")
	digest, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || !bytes.Equal(digest, argon2.IDKey([]byte("correct password"), salt, 3, 65536, 4, 32)) {
		t.Fatal("stored digest does not match Argon2id")
	}
	for _, tc := range []struct {
		password string
		want     bool
	}{{"correct password", true}, {"wrong password", false}} {
		match, err := hasher.Verify(ctx, tc.password, hash)
		if err != nil || match != tc.want {
			t.Fatalf("Verify = %v, %v; want %v", match, err, tc.want)
		}
	}
}

func TestArgon2idRejectsMalformedAndUnboundedHashes(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	for _, value := range []string{
		"", "plaintext", strings.Repeat("x", 513),
		"$argon2i$v=19$m=65536,t=3,p=4$" + salt + "$" + key,
		"$argon2id$v=16$m=65536,t=3,p=4$" + salt + "$" + key,
		"$argon2id$v=19$m=4294967295,t=3,p=4$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=4294967295,p=4$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=3,p=0$" + salt + "$" + key,
		"$argon2id$v=19$m=1,t=3,p=4$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=3,p=4$invalid$invalid",
	} {
		if match, err := NewArgon2id().Verify(context.Background(), "password", []byte(value)); err == nil || match {
			t.Fatalf("accepted invalid hash %q", value)
		}
	}
}

func TestArgon2idBoundsConcurrentWorkAndHonorsCancellation(t *testing.T) {
	hasher := NewArgon2id()
	ctx := context.Background()
	// Occupy both permits deterministically, without sleep or scheduler assumptions.
	if err := hasher.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := hasher.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hasher.Hash(ctx, "password"); !errors.Is(err, usecase.ErrBusy) {
		t.Fatalf("saturated Hash error = %v", err)
	}
	<-hasher.slots
	<-hasher.slots
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := hasher.Hash(canceled, "password"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Hash error = %v", err)
	}
	if len(hasher.slots) != 0 {
		t.Fatal("canceled work leaked a permit")
	}
}
