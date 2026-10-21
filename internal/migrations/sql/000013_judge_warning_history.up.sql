CREATE TABLE bot_judge_warnings (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    article SMALLINT NOT NULL CHECK (article BETWEEN 1 AND 3),
    first_update_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_id, article)
);

ALTER TABLE bot_judge_cases ADD COLUMN admin_warning BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE bot_judge_cases SET admin_warning = TRUE WHERE warning_only = TRUE;
