-- 000004_rbac.up.sql
-- RBAC data model per data-model.md:
--   departments (self-referencing tree) / roles / user_roles + users extension.
CREATE TABLE departments (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    parent_id  BIGINT      REFERENCES departments(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_departments_parent_id ON departments (parent_id);

CREATE TABLE roles (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    code       TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_roles (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX idx_user_roles_role_id ON user_roles (role_id);

ALTER TABLE users
    ADD COLUMN account       TEXT,
    ADD COLUMN email         TEXT,
    ADD COLUMN department_id BIGINT REFERENCES departments(id);

CREATE INDEX idx_users_department_id ON users (department_id);

-- Seed: default roles (idempotent by code).
INSERT INTO roles (name, code) VALUES
    ('超级管理员', 'super_admin'),
    ('管理员', 'admin'),
    ('普通用户', 'user')
ON CONFLICT (code) DO NOTHING;

-- Seed: department roots (idempotent by name).
INSERT INTO departments (name, parent_id) VALUES
    ('研发部', NULL),
    ('产品部', NULL),
    ('运营部', NULL),
    ('市场部', NULL),
    ('销售部', NULL),
    ('客服部', NULL)
ON CONFLICT (name) DO NOTHING;

-- Seed: department children under 研发部 (idempotent by name).
INSERT INTO departments (name, parent_id)
SELECT '前端组', id FROM departments WHERE name = '研发部'
ON CONFLICT (name) DO NOTHING;

INSERT INTO departments (name, parent_id)
SELECT '后端组', id FROM departments WHERE name = '研发部'
ON CONFLICT (name) DO NOTHING;

-- Seed: assign the default role 'user' to existing users (idempotent).
INSERT INTO user_roles (user_id, role_id)
SELECT u.id, r.id
FROM users u
JOIN roles r ON r.code = 'user'
ON CONFLICT (user_id, role_id) DO NOTHING;
