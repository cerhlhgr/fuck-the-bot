ALTER TABLE bot_action_plans DROP COLUMN considered_update_ids;
DROP INDEX bot_updates_decision_candidates_idx;
ALTER TABLE bot_updates
    DROP COLUMN considered_at,
    DROP COLUMN sent_at,
    DROP COLUMN message_id,
    DROP COLUMN thread_id,
    DROP COLUMN chat_id;
