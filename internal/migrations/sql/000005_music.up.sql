CREATE TABLE bot_music_tasks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    chat_id BIGINT NOT NULL,
    thread_id BIGINT NOT NULL DEFAULT 0,
    source_message_id BIGINT NOT NULL,
    task_id TEXT UNIQUE,
    request JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'submitted', 'done', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chat_id, source_message_id)
);

CREATE TABLE bot_music_tracks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    music_task_id BIGINT NOT NULL REFERENCES bot_music_tasks(id) ON DELETE CASCADE,
    audio_id TEXT NOT NULL,
    audio_url TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    delivered_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    UNIQUE (music_task_id, audio_id)
);

CREATE INDEX bot_music_tracks_pending_idx
    ON bot_music_tracks (next_attempt_at, id) WHERE delivered_at IS NULL;
