package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Users struct{ pool *pgxpool.Pool }

func NewUsers(pool *pgxpool.Pool) *Users { return &Users{pool: pool} }

func (r *Users) Create(ctx context.Context, user domain.User) (domain.User, error) {
	// The UNIQUE constraint arbitrates simultaneous registrations across instances.
	err := r.pool.QueryRow(ctx, `
		INSERT INTO public.users (id, name, email, login, password, salt)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6)
		RETURNING created_at`, user.ID, user.Name, string(user.Email), user.Login, user.Password, user.Salt).Scan(&user.CreatedAt)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" && pgErr.ConstraintName == "users_login_key" {
		return domain.User{}, usecase.ErrLoginTaken
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}
	return user, nil
}

func (r *Users) ByLogin(ctx context.Context, login string) (domain.User, error) {
	var user domain.User
	var email string
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(email, ''), login, password, salt, created_at
		FROM public.users WHERE login = $1`, login).Scan(
		&user.ID, &user.Name, &email, &user.Login, &user.Password, &user.Salt, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, usecase.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("select user: %w", err)
	}
	user.Email = domain.Email(email)
	return user, nil
}
