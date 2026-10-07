CREATE TABLE bot_action_plans (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    thread_id BIGINT NOT NULL DEFAULT 0,
    first_update_id BIGINT NOT NULL,
    update_ids BIGINT[] NOT NULL,
    actions JSONB NOT NULL,
    completed INTEGER NOT NULL DEFAULT 0 CHECK (completed >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chat_id, thread_id, first_update_id)
);

CREATE INDEX bot_action_plans_created_idx ON bot_action_plans (created_at);
