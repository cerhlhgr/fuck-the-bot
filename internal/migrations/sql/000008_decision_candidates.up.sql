ALTER TABLE bot_updates
    ADD COLUMN chat_id BIGINT,
    ADD COLUMN thread_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN message_id BIGINT,
    ADD COLUMN sent_at TIMESTAMPTZ,
    ADD COLUMN considered_at TIMESTAMPTZ;

UPDATE bot_updates
SET chat_id = (payload->'message'->'chat'->>'id')::bigint,
    thread_id = CASE WHEN payload->'message'->>'is_topic_message' = 'true'
                     THEN COALESCE((payload->'message'->>'message_thread_id')::bigint, 0)
                     ELSE 0 END,
    message_id = (payload->'message'->>'message_id')::bigint,
    sent_at = COALESCE(to_timestamp(NULLIF(payload->'message'->>'date', '')::bigint), received_at)
WHERE payload->'message' IS NOT NULL;

CREATE INDEX bot_updates_decision_candidates_idx
    ON bot_updates (chat_id, thread_id, message_id)
    WHERE considered_at IS NULL AND message_id IS NOT NULL;

ALTER TABLE bot_action_plans
    ADD COLUMN considered_update_ids BIGINT[] NOT NULL DEFAULT '{}';
