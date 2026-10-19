package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type TokenVerifier interface {
	Verify(string) (uuid.UUID, error)
}

type userIDContextKey struct{}

// UserIDFromContext returns the identity established by Authenticate.
func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDContextKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// Authenticate validates a Bearer token before calling a protected handler.
func Authenticate(tokens TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			values := r.Header.Values("Authorization")
			if len(values) != 1 || tokens == nil {
				writeUnauthorized(w)
				return
			}
			parts := strings.Fields(values[0])
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeUnauthorized(w)
				return
			}
			id, err := tokens.Verify(parts[1])
			if err != nil || id == uuid.Nil {
				writeUnauthorized(w)
				return
			}
			ctx := context.WithValue(r.Context(), userIDContextKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}
