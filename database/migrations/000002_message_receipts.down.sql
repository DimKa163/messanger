BEGIN;

ALTER TABLE public.messages
    DROP CONSTRAINT ck_messages_read_after_delivery,
    DROP COLUMN read_at,
    DROP COLUMN delivered_at;

COMMIT;
