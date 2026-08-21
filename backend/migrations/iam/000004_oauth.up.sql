CREATE TABLE user_external_identity (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_issuer  VARCHAR(255) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    email            VARCHAR(255),

    UNIQUE (provider_issuer, provider_subject)
);

CREATE INDEX user_external_identity_user_id_idx
    ON user_external_identity(user_id);
