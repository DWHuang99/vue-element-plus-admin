CREATE TABLE IF NOT EXISTS roles (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code VARCHAR(50) NOT NULL UNIQUE,
    name VARCHAR(100) NOT NULL,
    permissions TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username VARCHAR(50) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT users_username_length CHECK (CHAR_LENGTH(username) BETWEEN 3 AND 50),
    CONSTRAINT users_password_hash_not_empty CHECK (CHAR_LENGTH(password_hash) > 0)
);

CREATE INDEX IF NOT EXISTS users_role_id_idx ON users(role_id);

INSERT INTO roles (code, name, permissions)
VALUES
    ('admin', '管理员', ARRAY['*.*.*']),
    ('test', '测试用户', ARRAY['example:dialog:create', 'example:dialog:delete']),
    ('user', '普通用户', ARRAY[]::TEXT[])
ON CONFLICT (code) DO NOTHING;
