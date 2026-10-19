package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/DimKa163/messanger/internal/usecase"
	"golang.org/x/crypto/argon2"
)

const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 4
	saltLength   = 16
	keyLength    = 32
)

var errInvalidHash = errors.New("invalid Argon2id password hash")

// Argon2id bounds concurrent memory use to two 64 MiB derivations per process.
// A saturated instance rejects work instead of accumulating password requests.
type Argon2id struct {
	slots chan struct{}
}

func NewArgon2id() *Argon2id { return &Argon2id{slots: make(chan struct{}, 2)} }

func (a *Argon2id) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case a.slots <- struct{}{}:
		return nil
	default:
		return usecase.ErrBusy
	}
}

func (a *Argon2id) Hash(ctx context.Context, password string) ([]byte, []byte, error) {
	if err := a.acquire(ctx); err != nil {
		return nil, nil, err
	}
	defer func() { <-a.slots }()
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keyLength)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Keep parameters with the hash so future parameter changes do not break login.
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		argonMemory, argonTime, argonThreads, base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
	return []byte(encoded), salt, nil
}

func (a *Argon2id) Verify(ctx context.Context, password string, encoded []byte) (bool, error) {
	if len(encoded) > 512 {
		return false, errInvalidHash
	}
	parts := strings.Split(string(encoded), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errInvalidHash
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, errInvalidHash
	}
	memory, errM := parseParameter(params[0], "m=", argonMemory)
	iterations, errT := parseParameter(params[1], "t=", argonTime)
	threads, errP := parseParameter(params[2], "p=", argonThreads)
	if errM != nil || errT != nil || errP != nil || memory < 8*threads {
		return false, errInvalidHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < saltLength || len(salt) > 64 {
		return false, errInvalidHash
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(expected) != keyLength {
		return false, errInvalidHash
	}
	if err := a.acquire(ctx); err != nil {
		return false, err
	}
	defer func() { <-a.slots }()
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(threads), keyLength)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parseParameter(value, prefix string, max uint32) (uint32, error) {
	if !strings.HasPrefix(value, prefix) {
		return 0, errInvalidHash
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 32)
	if err != nil || n == 0 || n > uint64(max) {
		return 0, errInvalidHash
	}
	return uint32(n), nil
}
