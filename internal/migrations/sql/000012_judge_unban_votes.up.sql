CREATE TABLE bot_judge_unban_votes (
    request_update_id BIGINT PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    thread_id BIGINT NOT NULL DEFAULT 0,
    request_message_id BIGINT NOT NULL,
    target_user_id BIGINT NOT NULL,
    target_name TEXT NOT NULL,
    target_status TEXT NOT NULL CHECK (target_status IN ('restricted', 'kicked')),
    poll_message_id BIGINT NOT NULL DEFAULT 0,
    closes_at TIMESTAMPTZ,
    yes_votes INTEGER NOT NULL DEFAULT 0,
    no_votes INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'created' CHECK (state IN ('created', 'open', 'closed', 'applied', 'complete')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX bot_judge_unban_votes_active_target_idx
ON bot_judge_unban_votes (chat_id, target_user_id)
WHERE state IN ('created', 'open', 'closed', 'applied');
