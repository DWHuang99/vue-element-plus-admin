-- 000003_sessions.up.sql
-- Create sessions table per data-model.md.
CREATE TABLE sessions (
    id           BIGSERIAL PRIMARY KEY,
    token_hash   TEXT        NOT NULL UNIQUE,
    user_id      BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user_id ON sessions (user_id);
CREATE INDEX idx_sessions_token_hash ON sessions (token_hash);
