CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    refresh_token TEXT UNIQUE NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS sources (
    id               BIGSERIAL PRIMARY KEY,
    driver           TEXT NOT NULL,
    username         TEXT NOT NULL,
    overseer_task_id TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (driver, username)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_id  BIGINT NOT NULL REFERENCES sources(id),
    posture    TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, source_id)
);

CREATE INDEX IF NOT EXISTS idx_subs_user ON subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_subs_source ON subscriptions(source_id);

CREATE TABLE IF NOT EXISTS worker_events (
    id         BIGSERIAL PRIMARY KEY,
    source_id  BIGINT NOT NULL REFERENCES sources(id),
    pid        INT NOT NULL,
    event_type TEXT NOT NULL,
    exit_code  INT,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_we_source_ts ON worker_events(source_id, ts);
CREATE INDEX IF NOT EXISTS idx_we_source_pid_type ON worker_events(source_id, pid, event_type);

CREATE TABLE IF NOT EXISTS config (
    id   INT PRIMARY KEY DEFAULT 1,
    data JSONB NOT NULL DEFAULT '{}'
);
