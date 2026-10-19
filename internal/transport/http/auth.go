package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/usecase"
)

type AuthService interface {
	Register(context.Context, usecase.RegisterInput) (domain.User, error)
	Login(context.Context, string, string) (usecase.AccessToken, error)
}

type authHandler struct{ service AuthService }

func (h authHandler) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decodeAuthRequest(w, r, &input) {
		return
	}
	user, err := h.service.Register(r.Context(), usecase.RegisterInput{
		Name: input.Name, Email: input.Email, Login: input.Login, Password: input.Password,
	})
	if err != nil {
		writeAuthError(w, err)
		return
	}
	// Explicit response fields prevent credentials from being exposed by model changes.
	writeJSON(w, http.StatusCreated, struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Login     string    `json:"login"`
		CreatedAt time.Time `json:"created_at"`
	}{user.ID.String(), user.Name, user.Login, user.CreatedAt})
}

func (h authHandler) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decodeAuthRequest(w, r, &input) {
		return
	}
	token, err := h.service.Login(r.Context(), input.Login, input.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AccessToken string    `json:"access_token"`
		TokenType   string    `json:"token_type"`
		ExpiresAt   time.Time `json:"expires_at"`
	}{token.Value, "Bearer", token.ExpiresAt})
}

func decodeAuthRequest(w http.ResponseWriter, r *http.Request, input any) bool {
	w.Header().Set("Cache-Control", "no-store")
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(input)
	if err == nil {
		// Only one JSON object is allowed, including after trailing whitespace.
		if trailing := decoder.Decode(new(any)); trailing != io.EOF {
			err = trailing
			if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		}
		return false
	}
	return true
}

func writeAuthError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "internal server error"
	switch {
	case errors.Is(err, usecase.ErrInvalidInput):
		status, message = http.StatusBadRequest, "invalid registration data"
	case errors.Is(err, usecase.ErrLoginTaken):
		status, message = http.StatusConflict, "login already taken"
	case errors.Is(err, usecase.ErrInvalidCredentials):
		status, message = http.StatusUnauthorized, "invalid login or password"
	case errors.Is(err, usecase.ErrBusy):
		status, message = http.StatusTooManyRequests, "authentication capacity exceeded; retry later"
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
