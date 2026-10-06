CREATE TABLE bot_important_context (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    thread_id BIGINT NOT NULL DEFAULT 0,
    source_message_id BIGINT NOT NULL,
    source_sent_at TIMESTAMPTZ NOT NULL,
    author TEXT NOT NULL,
    source_body TEXT NOT NULL,
    summary TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bot_important_context_source_unique UNIQUE (chat_id, source_message_id)
);

CREATE INDEX bot_important_context_dialogue_idx
    ON bot_important_context (chat_id, thread_id, source_sent_at, id);
