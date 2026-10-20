CREATE TABLE bot_judge_modes (
    chat_id BIGINT PRIMARY KEY,
    enabled BOOLEAN NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE bot_judge_cases (
    update_id BIGINT PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    article SMALLINT NOT NULL CHECK (article BETWEEN 1 AND 3),
    phase SMALLINT NOT NULL DEFAULT 0 CHECK (phase BETWEEN 0 AND 3),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bot_judge_cases_chat_idx ON bot_judge_cases (chat_id, created_at DESC);
