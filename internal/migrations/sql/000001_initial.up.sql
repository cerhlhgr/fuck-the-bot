CREATE TABLE IF NOT EXISTS bot_history (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    thread_id BIGINT NOT NULL DEFAULT 0,
    message_id BIGINT,
    sent_at TIMESTAMPTZ NOT NULL,
    author TEXT NOT NULL,
    body TEXT NOT NULL,
    bot BOOLEAN NOT NULL DEFAULT FALSE,
    CONSTRAINT bot_history_chat_message_unique UNIQUE (chat_id, message_id)
);

CREATE INDEX IF NOT EXISTS bot_history_context_idx
    ON bot_history (chat_id, thread_id, sent_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS bot_history_expiry_idx ON bot_history (sent_at);

CREATE TABLE IF NOT EXISTS bot_updates (
    update_id BIGINT PRIMARY KEY,
    payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS bot_updates_pending_idx
    ON bot_updates (update_id) WHERE processed_at IS NULL;
