BEGIN;

ALTER TABLE public.messages
    ADD COLUMN message_type TEXT NOT NULL DEFAULT 'text',
    ADD CONSTRAINT ck_messages_type CHECK (message_type IN ('text'));

CREATE TABLE public.outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type TEXT NOT NULL CHECK (event_type IN ('message.send', 'message.delivered', 'message.readed')),
    message_id UUID NOT NULL REFERENCES public.messages(id),
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMPTZ,
    UNIQUE (message_id, event_type)
);

CREATE INDEX ix_outbox_pending ON public.outbox (created_at, id) WHERE published_at IS NULL;

COMMIT;
