CREATE TABLE public.users
(
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(250) NOT NULL,
    email VARCHAR(100) NULL,
    login VARCHAR(100) NOT NULL UNIQUE,
    password BYTEA NOT NULL,
    salt BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.conversations (
    id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type SMALLINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    count BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE public.conversation_members (
  conversation_id UUID NOT NULL REFERENCES conversations(id),
  user_id UUID NOT NULL REFERENCES users(id),
  joined_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (conversation_id, user_id)
);


CREATE TABLE public.messages (
  id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  conversation_id UUID NOT NULL REFERENCES conversations(id),
  sender_id UUID NOT NULL REFERENCES users(id),
  client_message_id UUID NOT NULL,
  sequence BIGINT NOT NULL,
  content BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

  UNIQUE (sender_id, client_message_id),
  UNIQUE (conversation_id, sequence)
);

CREATE INDEX ix_messages_history
    ON messages (conversation_id, sequence DESC);

CREATE INDEX ix_users_login
    ON users (login);