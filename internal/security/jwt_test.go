package security

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestJWTIssueAndVerify(t *testing.T) {
	j, err := NewJWT("test-secret-with-at-least-32-bytes", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	id := uuid.New()
	token, err := j.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	if token.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("expiration = %v", token.ExpiresAt)
	}
	if got, err := j.Verify(token.Value); err != nil || got != id {
		t.Fatalf("Verify = %v, %v", got, err)
	}
	other, _ := NewJWT("a-different-secret-with-32-bytes!!", time.Hour)
	other.now = j.now
	if _, err := other.Verify(token.Value); err == nil {
		t.Fatal("accepted a token signed with another key")
	}
	j.now = func() time.Time { return now.Add(time.Hour) }
	if _, err := j.Verify(token.Value); err == nil {
		t.Fatal("accepted an expired token")
	}
}

func TestJWTRejectsInvalidClaimsAndAlgorithms(t *testing.T) {
	j, _ := NewJWT("test-secret-with-at-least-32-bytes", time.Hour)
	now := time.Now().Truncate(time.Second)
	j.now = func() time.Time { return now }
	validClaims := func() jwt.RegisteredClaims {
		return jwt.RegisteredClaims{
			Issuer: tokenIssuer, Audience: jwt.ClaimStrings{tokenAudience}, Subject: uuid.NewString(),
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*jwt.RegisteredClaims)
	}{
		{"no expiry", func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil }},
		{"wrong issuer", func(c *jwt.RegisteredClaims) { c.Issuer = "other" }},
		{"no audience", func(c *jwt.RegisteredClaims) { c.Audience = nil }},
		{"no subject", func(c *jwt.RegisteredClaims) { c.Subject = "" }},
		{"invalid subject", func(c *jwt.RegisteredClaims) { c.Subject = "anything" }},
		{"nil subject", func(c *jwt.RegisteredClaims) { c.Subject = uuid.Nil.String() }},
		{"future issued at", func(c *jwt.RegisteredClaims) { c.IssuedAt = jwt.NewNumericDate(now.Add(time.Hour)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			tc.change(&claims)
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(j.secret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := j.Verify(token); err == nil {
				t.Fatal("accepted invalid claims")
			}
		})
	}
	for _, method := range []jwt.SigningMethod{jwt.SigningMethodNone, jwt.SigningMethodHS384} {
		var key any = j.secret
		if method == jwt.SigningMethodNone {
			key = jwt.UnsafeAllowNoneSignatureType
		}
		token, err := jwt.NewWithClaims(method, validClaims()).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Verify(token); err == nil {
			t.Fatalf("accepted algorithm %s", method.Alg())
		}
	}
	if _, err := j.Verify("malformed.token"); err == nil {
		t.Fatal("accepted malformed token")
	}
}

func TestJWTRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		secret string
		ttl    time.Duration
	}{{"short", time.Hour}, {"                                ", time.Hour}, {"test-secret-with-at-least-32-bytes", 0}, {"test-secret-with-at-least-32-bytes", 25 * time.Hour}} {
		if _, err := NewJWT(tc.secret, tc.ttl); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}
