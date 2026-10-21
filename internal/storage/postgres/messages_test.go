package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func seedMessageConversation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, users ...uuid.UUID) uuid.UUID {
	t.Helper()
	conversation := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO public.conversations (id,type) VALUES ($1,2)`, conversation); err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if _, err := pool.Exec(ctx, `INSERT INTO public.users (id,login,name,password,salt) VALUES ($1,$2,'test','password','salt')
			ON CONFLICT (id) DO NOTHING`, user, user.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO public.conversation_members (conversation_id,user_id) VALUES ($1,$2)`, conversation, user); err != nil {
			t.Fatal(err)
		}
	}
	return conversation
}

func TestMessagesPostgres(t *testing.T) {
	pool := isolatedUsersDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice, bob, carol, outsider := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	conversation := seedMessageConversation(t, ctx, pool, alice, bob, carol)
	other := seedMessageConversation(t, ctx, pool, alice, outsider)
	repo := NewMessages(pool)
	service := usecase.NewMessages(repo)
	input := usecase.SendMessageInput{ConversationID: conversation, SenderID: alice, ClientMessageID: uuid.New(),
		MessageType: domain.TextMessage, Content: "Привет ' -- \\u0000"}
	m, err := service.Send(ctx, alice, input)
	if err != nil || m.Sequence != 1 || m.CreatedAt.IsZero() {
		t.Fatalf("send: %+v %v", m, err)
	}
	duplicate, err := service.Send(ctx, alice, input)
	if err != nil || duplicate.ID != m.ID || duplicate.Sequence != 1 {
		t.Fatalf("repeat: %+v %v", duplicate, err)
	}
	changed := input
	changed.Content = "different"
	if _, err := service.Send(ctx, alice, changed); !errors.Is(err, usecase.ErrMessageConflict) {
		t.Fatalf("changed repeat: %v", err)
	}
	changed = input
	changed.ConversationID = other
	if _, err := service.Send(ctx, alice, changed); !errors.Is(err, usecase.ErrMessageConflict) {
		t.Fatalf("cross-conversation repeat: %v", err)
	}
	changed = input
	changed.SenderID = outsider
	if _, err := service.Send(ctx, outsider, changed); !errors.Is(err, usecase.ErrMessageForbidden) {
		t.Fatalf("outsider send: %v", err)
	}
	if err := service.Read(ctx, alice, []uuid.UUID{m.ID}); !errors.Is(err, usecase.ErrMessageForbidden) {
		t.Fatalf("self read: %v", err)
	}
	if err := service.Read(ctx, outsider, []uuid.UUID{m.ID}); !errors.Is(err, usecase.ErrMessageForbidden) {
		t.Fatalf("outsider read: %v", err)
	}

	// Distinct senders may reuse the same client ID: reads use server message IDs.
	second := input
	second.SenderID = bob
	secondMessage, err := service.Send(ctx, bob, second)
	if err != nil {
		t.Fatal(err)
	}
	// Mixed authorized/unknown batches are atomic, including their outbox events.
	if err := service.Read(ctx, carol, []uuid.UUID{m.ID, uuid.New()}); !errors.Is(err, usecase.ErrMessageForbidden) {
		t.Fatalf("mixed read: %v", err)
	}
	loaded, _, err := repo.ForDelivery(ctx, m.ID)
	if err != nil || loaded.ReadAt != nil || loaded.DeliveredAt != nil {
		t.Fatalf("partial batch changed receipts: %+v %v", loaded, err)
	}

	// A first read also proves delivery and emits both events exactly once.
	if err := service.Read(ctx, carol, []uuid.UUID{m.ID, secondMessage.ID, m.ID}); err != nil {
		t.Fatal(err)
	}
	loaded, recipients, err := repo.ForDelivery(ctx, m.ID)
	if err != nil || loaded.ReadAt == nil || loaded.DeliveredAt == nil || loaded.ReadAt.Before(*loaded.DeliveredAt) || len(recipients) != 2 {
		t.Fatalf("receipts: %+v recipients=%v err=%v", loaded, recipients, err)
	}
	if err := service.Read(ctx, bob, []uuid.UUID{m.ID}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkDelivered(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	again, _, _ := repo.ForDelivery(ctx, m.ID)
	if !again.ReadAt.Equal(*loaded.ReadAt) || !again.DeliveredAt.Equal(*loaded.DeliveredAt) {
		t.Fatal("repeat receipt changed first timestamps")
	}
	var events, count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.outbox WHERE message_id=$1`, m.ID).Scan(&events); err != nil || events != 3 {
		t.Fatalf("events=%d err=%v", events, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count FROM public.conversations WHERE id=$1`, conversation).Scan(&count); err != nil || count != 2 {
		t.Fatalf("counter=%d err=%v", count, err)
	}
	var payload usecase.MessageEvent
	if err := pool.QueryRow(ctx, `SELECT payload FROM public.outbox WHERE message_id=$1 AND event_type='message.send'`, m.ID).Scan(&payload); err != nil || payload.MessageID != m.ID || payload.Content != input.Content {
		t.Fatalf("outbox payload=%+v err=%v", payload, err)
	}

	// Forced outbox failure must roll back the message and sequence allocation.
	if _, err := pool.Exec(ctx, `ALTER TABLE public.outbox ADD CONSTRAINT test_reject_send CHECK (event_type <> 'message.send') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	changed = input
	changed.ClientMessageID = uuid.New()
	if _, err := service.Send(ctx, alice, changed); err == nil {
		t.Fatal("expected outbox failure")
	}
	if err := pool.QueryRow(ctx, `SELECT count FROM public.conversations WHERE id=$1`, conversation).Scan(&count); err != nil || count != 2 {
		t.Fatalf("counter leaked after rollback=%d err=%v", count, err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE public.outbox DROP CONSTRAINT test_reject_send`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM public.conversation_members WHERE conversation_id=$1 AND user_id=$2`, conversation, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(ctx, alice, input); !errors.Is(err, usecase.ErrMessageForbidden) {
		t.Fatalf("removed member retry: %v", err)
	}
}

func TestConcurrentMessagesPostgres(t *testing.T) {
	pool := isolatedUsersDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice, bob, carol := uuid.New(), uuid.New(), uuid.New()
	conversation := seedMessageConversation(t, ctx, pool, alice, bob, carol)
	repo := NewMessages(pool)
	service := usecase.NewMessages(repo)
	input := usecase.SendMessageInput{ConversationID: conversation, SenderID: alice, ClientMessageID: uuid.New(), MessageType: domain.TextMessage, Content: "concurrent"}
	run := func(n int, fn func(int) error) {
		start, results := make(chan struct{}), make(chan error, n)
		for i := range n {
			go func() { <-start; results <- fn(i) }()
		}
		close(start)
		for range n {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
	}
	// The first transaction holds the sender row while both retries reach Save.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackMessageTx(blocker)
	if _, err := blocker.Exec(ctx, `SELECT id FROM public.users WHERE id=$1 FOR UPDATE`, alice); err != nil {
		t.Fatal(err)
	}
	started, results := make(chan struct{}, 2), make(chan error, 2)
	for range 2 {
		go func() { started <- struct{}{}; _, err := service.Send(ctx, alice, input); results <- err }()
	}
	<-started
	<-started
	// Wait for PostgreSQL to confirm both writes are blocked by our transaction,
	// so this test exercises overlap rather than relying on goroutine scheduling.
	for {
		var blocked int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock'
			AND query LIKE '%SELECT id FROM public.users%FOR UPDATE%'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked == 2 {
			break
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	run(12, func(i int) error {
		in := input
		if i%2 == 0 {
			in.SenderID = bob
		}
		in.ClientMessageID = uuid.New()
		_, err := service.Send(ctx, in.SenderID, in)
		return err
	})
	var count, messages, distinctSequences, maxSequence, events int
	if err := pool.QueryRow(ctx, `SELECT count FROM public.conversations WHERE id=$1`, conversation).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT sequence), max(sequence) FROM public.messages WHERE conversation_id=$1`, conversation).Scan(&messages, &distinctSequences, &maxSequence); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.outbox WHERE event_type='message.send'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if count != 13 || messages != count || distinctSequences != count || maxSequence != count || events != count {
		t.Fatalf("count=%d messages=%d distinct=%d max=%d outbox=%d", count, messages, distinctSequences, maxSequence, events)
	}
	m, err := service.Send(ctx, alice, input)
	if err != nil {
		t.Fatal(err)
	}
	run(12, func(i int) error {
		if i%3 == 0 {
			return repo.MarkDelivered(ctx, m.ID)
		}
		reader := bob
		if i%2 == 0 {
			reader = carol
		}
		return service.Read(ctx, reader, []uuid.UUID{m.ID})
	})
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.outbox WHERE message_id=$1`, m.ID).Scan(&events); err != nil || events != 3 {
		t.Fatalf("concurrent receipts events=%d err=%v", events, err)
	}
}
