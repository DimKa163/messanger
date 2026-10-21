package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Messages struct{ pool *pgxpool.Pool }

func NewMessages(pool *pgxpool.Pool) *Messages { return &Messages{pool: pool} }

const messageColumns = `id, conversation_id, sender_id, client_message_id, sequence, content,
	message_type, created_at, delivered_at, read_at`

func scanMessage(row pgx.Row) (domain.Message, error) {
	var m domain.Message
	err := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.ClientMessageID, &m.Sequence,
		&m.Content, &m.MessageType, &m.CreatedAt, &m.DeliveredAt, &m.ReadAt)
	return m, err
}

func rollbackMessageTx(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func insertMessageEvent(ctx context.Context, tx pgx.Tx, typ string, m domain.Message) error {
	payload, err := json.Marshal(usecase.EventForMessage(typ, m))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.outbox (event_type, message_id, payload) VALUES ($1, $2, $3)`, typ, m.ID, payload)
	return err
}

func (r *Messages) Save(ctx context.Context, m domain.Message) (domain.Message, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Message{}, err
	}
	defer rollbackMessageTx(tx)
	// Serialize a sender's idempotency keys even across conversations/processes.
	// This lock also prevents user deletion during the operation.
	var actor uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM public.users WHERE id=$1 FOR UPDATE`, m.SenderID).Scan(&actor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, usecase.ErrMessageForbidden
		}
		return domain.Message{}, err
	}
	// Membership is locked until commit so a concurrent removal cannot pass us.
	var member uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT user_id FROM public.conversation_members
		WHERE conversation_id=$1 AND user_id=$2 FOR SHARE`, m.ConversationID, m.SenderID).Scan(&member); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, usecase.ErrMessageForbidden
		}
		return domain.Message{}, err
	}
	existing, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM public.messages
		WHERE sender_id=$1 AND client_message_id=$2`, m.SenderID, m.ClientMessageID))
	if err == nil {
		if existing.ConversationID != m.ConversationID || existing.MessageType != m.MessageType || !bytes.Equal(existing.Content, m.Content) {
			return domain.Message{}, usecase.ErrMessageConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, err
	}
	// UPDATE locks the conversation and atomically allocates a sequence. All
	// message, counter and outbox changes roll back together on any failure.
	if err := tx.QueryRow(ctx, `UPDATE public.conversations SET count=count+1 WHERE id=$1 RETURNING count`, m.ConversationID).Scan(&m.Sequence); err != nil {
		return domain.Message{}, err
	}
	m, err = scanMessage(tx.QueryRow(ctx, `INSERT INTO public.messages
		(id, conversation_id, sender_id, client_message_id, sequence, content, message_type)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+messageColumns,
		m.ID, m.ConversationID, m.SenderID, m.ClientMessageID, m.Sequence, m.Content, m.MessageType))
	if err != nil {
		return domain.Message{}, fmt.Errorf("insert message: %w", err)
	}
	if err := insertMessageEvent(ctx, tx, usecase.MessageSend, m); err != nil {
		return domain.Message{}, err
	}
	return m, tx.Commit(ctx)
}

func (r *Messages) ForDelivery(ctx context.Context, id uuid.UUID) (domain.Message, []uuid.UUID, error) {
	m, err := scanMessage(r.pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM public.messages WHERE id=$1`, id))
	if err != nil {
		return domain.Message{}, nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT user_id FROM public.conversation_members WHERE conversation_id=$1 AND user_id<>$2 ORDER BY user_id`, m.ConversationID, m.SenderID)
	if err != nil {
		return domain.Message{}, nil, err
	}
	defer rows.Close()
	var recipients []uuid.UUID
	for rows.Next() {
		var user uuid.UUID
		if err := rows.Scan(&user); err != nil {
			return domain.Message{}, nil, err
		}
		recipients = append(recipients, user)
	}
	return m, recipients, rows.Err()
}

func (r *Messages) MarkDelivered(ctx context.Context, id uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackMessageTx(tx)
	m, err := scanMessage(tx.QueryRow(ctx, `UPDATE public.messages SET delivered_at=clock_timestamp()
		WHERE id=$1 AND delivered_at IS NULL RETURNING `+messageColumns, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err := insertMessageEvent(ctx, tx, usecase.MessageDelivered, m); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Messages) MarkRead(ctx context.Context, actor uuid.UUID, ids []uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackMessageTx(tx)
	// Stable message ordering avoids deadlocks between overlapping read batches.
	// Lock memberships too, so removal cannot interleave with authorization.
	rows, err := tx.Query(ctx, `SELECT m.id, m.conversation_id, m.sender_id, m.client_message_id,
		m.sequence, m.content, m.message_type, m.created_at, m.delivered_at, m.read_at
		FROM public.messages m JOIN public.conversation_members cm ON cm.conversation_id=m.conversation_id
		WHERE m.id=ANY($1::uuid[]) AND cm.user_id=$2 AND m.sender_id<>$2
		ORDER BY m.id FOR UPDATE OF m FOR SHARE OF cm`, ids, actor)
	if err != nil {
		return err
	}
	var messages []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return err
		}
		messages = append(messages, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(messages) != len(ids) {
		return usecase.ErrMessageForbidden
	}
	for _, m := range messages {
		if m.ReadAt != nil {
			continue
		}
		wasDelivered := m.DeliveredAt != nil
		m, err = scanMessage(tx.QueryRow(ctx, `WITH receipt AS MATERIALIZED (SELECT clock_timestamp() AS at)
			UPDATE public.messages SET delivered_at=COALESCE(delivered_at, receipt.at),
			read_at=GREATEST(delivered_at, receipt.at) FROM receipt WHERE id=$1 RETURNING `+messageColumns, m.ID))
		if err != nil {
			return err
		}
		// A read proves delivery even if the socket consumer has not recorded it yet.
		if !wasDelivered {
			if err := insertMessageEvent(ctx, tx, usecase.MessageDelivered, m); err != nil {
				return err
			}
		}
		if err := insertMessageEvent(ctx, tx, usecase.MessageRead, m); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
