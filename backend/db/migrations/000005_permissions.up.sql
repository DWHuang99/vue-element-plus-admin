-- 000005_permissions.up.sql
-- Permission catalog and role grants for the first authorization closure.
CREATE TABLE permissions (
    id         BIGSERIAL PRIMARY KEY,
    code       TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_permissions (
    role_id       BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id BIGINT NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE INDEX idx_role_permissions_permission_id ON role_permissions (permission_id);

INSERT INTO permissions (code) VALUES
    ('roles.read'),
    ('roles.write'),
    ('departments.read'),
    ('departments.write'),
    ('users.read'),
    ('users.write')
ON CONFLICT (code) DO NOTHING;

-- Before authorization existed, the role API allowed seeded codes to be
-- renamed or unassigned roles to be deleted. Restore those stable identities
-- so an existing installation remains bootable and admin-init can recover it.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM roles WHERE code = 'super_admin') THEN
        UPDATE roles SET code = 'super_admin', updated_at = now()
        WHERE name = '超级管理员';
        IF NOT FOUND THEN
            INSERT INTO roles (name, code) VALUES ('超级管理员', 'super_admin');
        END IF;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM roles WHERE code = 'admin') THEN
        UPDATE roles SET code = 'admin', updated_at = now()
        WHERE name = '管理员';
        IF NOT FOUND THEN
            INSERT INTO roles (name, code) VALUES ('管理员', 'admin');
        END IF;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM roles WHERE code = 'user') THEN
        UPDATE roles SET code = 'user', updated_at = now()
        WHERE name = '普通用户';
        IF NOT FOUND THEN
            INSERT INTO roles (name, code) VALUES ('普通用户', 'user');
        END IF;
    END IF;
END
$$;

-- Both privileged built-in roles receive the complete first-generation catalog.
-- The built-in user role and all custom roles intentionally receive no grants.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.code IN ('admin', 'super_admin')
ON CONFLICT (role_id, permission_id) DO NOTHING;
