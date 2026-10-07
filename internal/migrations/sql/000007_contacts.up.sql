CREATE TABLE bot_contacts (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL,
    link TEXT NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chat_id, user_id)
);

CREATE INDEX bot_contacts_chat_username_idx ON bot_contacts (chat_id, lower(username));
CREATE INDEX bot_contacts_chat_name_idx ON bot_contacts (chat_id, lower(display_name));
