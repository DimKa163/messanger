package security

import (
	"errors"
	"strings"
	"time"

	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const tokenIssuer = "messanger"
const tokenAudience = "messanger-api"

type JWT struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewJWT(secret string, ttl time.Duration) (*JWT, error) {
	if len(secret) < 32 || strings.TrimSpace(secret) != secret || ttl < time.Second || ttl > 24*time.Hour {
		return nil, errors.New("JWT requires a secret of at least 32 bytes and a TTL between 1s and 24h")
	}
	return &JWT{secret: []byte(secret), ttl: ttl, now: time.Now}, nil
}

func (j *JWT) Issue(userID uuid.UUID) (usecase.AccessToken, error) {
	if userID == uuid.Nil {
		return usecase.AccessToken{}, errors.New("cannot issue JWT for an empty user ID")
	}
	now := j.now().UTC().Truncate(time.Second)
	expires := now.Add(j.ttl).Truncate(time.Second)
	claims := jwt.RegisteredClaims{
		Issuer: tokenIssuer, Audience: jwt.ClaimStrings{tokenAudience}, Subject: userID.String(),
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expires),
	}
	value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(j.secret)
	if err != nil {
		return usecase.AccessToken{}, err
	}
	return usecase.AccessToken{Value: value, ExpiresAt: expires}, nil
}

// Verify validates the signature, algorithm, lifetime, issuer, audience and user ID.
func (j *JWT) Verify(value string) (uuid.UUID, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(value, claims, func(*jwt.Token) (any, error) { return j.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(),
		jwt.WithIssuer(tokenIssuer), jwt.WithAudience(tokenAudience), jwt.WithIssuedAt(), jwt.WithTimeFunc(j.now))
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("invalid JWT subject")
	}
	return id, nil
}
