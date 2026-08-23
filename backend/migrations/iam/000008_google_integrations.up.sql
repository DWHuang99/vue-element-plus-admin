CREATE TABLE IF NOT EXISTS google_integrations (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id                 BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_subject        VARCHAR(255) NOT NULL,
    access_token_encrypted  TEXT NOT NULL,
    refresh_token_encrypted TEXT NOT NULL,
    token_type              VARCHAR(32) NOT NULL DEFAULT 'Bearer',
    expiry                  TIMESTAMPTZ NOT NULL,
    scopes                  TEXT[] NOT NULL DEFAULT '{}',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT google_integrations_user_id_unique UNIQUE (user_id),
    CONSTRAINT google_integrations_provider_subject_unique UNIQUE (provider_subject)
);
