CREATE TABLE IF NOT EXISTS menus (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_id BIGINT REFERENCES menus(id) ON DELETE RESTRICT,
    type SMALLINT NOT NULL DEFAULT 0,
    path VARCHAR(255) NOT NULL,
    name VARCHAR(100) NOT NULL DEFAULT '',
    component VARCHAR(255) NOT NULL DEFAULT '#',
    status BOOLEAN NOT NULL DEFAULT TRUE,
    meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    permission_list JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS role_menus (
    role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    menu_id BIGINT NOT NULL REFERENCES menus(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, menu_id)
);

CREATE TABLE IF NOT EXISTS role_permissions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    menu_id BIGINT REFERENCES menus(id) ON DELETE CASCADE,
    permission_code VARCHAR(255) NOT NULL,
    CONSTRAINT role_permissions_code_not_empty CHECK (BTRIM(permission_code) <> '')
);

CREATE INDEX IF NOT EXISTS menus_parent_id_idx ON menus(parent_id);
CREATE UNIQUE INDEX IF NOT EXISTS role_permissions_unique_assignment_idx
    ON role_permissions (role_id, COALESCE(menu_id, 0), permission_code);
CREATE INDEX IF NOT EXISTS role_permissions_role_id_idx ON role_permissions(role_id);
CREATE INDEX IF NOT EXISTS role_permissions_menu_id_idx ON role_permissions(menu_id);

INSERT INTO role_permissions (role_id, menu_id, permission_code)
SELECT id, NULL, '*.*.*' FROM roles WHERE code = 'admin'
ON CONFLICT DO NOTHING;

INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
SELECT NULL, 0, '/dashboard', 'Dashboard', '#', TRUE,
       '{"title":"首页","icon":"vi-ant-design:dashboard-filled"}'::jsonb,
       '[]'::jsonb
WHERE NOT EXISTS (SELECT 1 FROM menus);
