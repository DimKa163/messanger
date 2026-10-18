CREATE TABLE users
(
    id UUID PRIMARY KEY DEFAULT get_random_uuid(),
    name VARCHAR(250) NOT NULL,
    login VARCHAR(100) NOT NULL UNIQUE,
    password BYTEA NOT NULL,
    salt BYTEA NOT NULL
);

CREATE TABLE conversations (
   id UUID PRIMARY KEY,
   type SMALLINT NOT NULL,
   created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE conversation_members (
  conversation_id UUID NOT NULL,
  user_id UUID NOT NULL,
  joined_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
);

CREATE TABLE messages (
  id UUID PRIMARY KEY,
  conversation_id UUID NOT NULL,
  sender_id UUID NOT NULL,
  client_message_id UUID NOT NULL,
  sequence BIGINT NOT NULL,
  content BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,

  UNIQUE (sender_id, client_message_id),
  UNIQUE (conversation_id, sequence)
);

CREATE INDEX ix_messages_history
    ON messages (conversation_id, sequence DESC);

CREATE INDEX ix_users_login
    ON users (login);