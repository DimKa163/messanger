BEGIN;

ALTER TABLE public.messages
    ADD COLUMN delivered_at TIMESTAMPTZ,
    ADD COLUMN read_at TIMESTAMPTZ,
    ADD CONSTRAINT ck_messages_read_after_delivery CHECK (
        read_at IS NULL OR (delivered_at IS NOT NULL AND read_at >= delivered_at)
    );

COMMIT;
