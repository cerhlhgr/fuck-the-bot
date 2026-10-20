ALTER TABLE bot_judge_cases
ADD COLUMN forwarded_message_id BIGINT NOT NULL DEFAULT 0;
