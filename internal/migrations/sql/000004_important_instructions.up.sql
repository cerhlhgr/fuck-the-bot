ALTER TABLE bot_important_context
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'fact';

ALTER TABLE bot_important_context
    ADD CONSTRAINT bot_important_context_kind_check
    CHECK (kind IN ('fact', 'instruction'));
