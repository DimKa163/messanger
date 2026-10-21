BEGIN;

DROP TABLE public.outbox;
ALTER TABLE public.messages DROP CONSTRAINT ck_messages_type, DROP COLUMN message_type;

COMMIT;
