CREATE TABLE IF NOT EXISTS casbin_rule (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ptype VARCHAR(100) NOT NULL DEFAULT '',
    v0 VARCHAR(100) NOT NULL DEFAULT '',
    v1 VARCHAR(100) NOT NULL DEFAULT '',
    v2 VARCHAR(100) NOT NULL DEFAULT '',
    v3 VARCHAR(100) NOT NULL DEFAULT '',
    v4 VARCHAR(100) NOT NULL DEFAULT '',
    v5 VARCHAR(100) NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_casbin_rule
    ON casbin_rule (ptype, v0, v1, v2, v3, v4, v5);

-- Existing user-to-role assignments become Casbin grouping policies.
INSERT INTO casbin_rule (ptype, v0, v1)
SELECT 'g', 'user:' || u.id::TEXT, 'role:' || r.code
FROM users u
JOIN roles r ON r.id = u.role_id
ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING;

-- Existing role permissions become Casbin permission policies.
INSERT INTO casbin_rule (ptype, v0, v1)
SELECT DISTINCT 'p', 'role:' || r.code, rp.permission_code
FROM role_permissions rp
JOIN roles r ON r.id = rp.role_id
WHERE r.status = TRUE AND BTRIM(rp.permission_code) <> ''
ON CONFLICT (ptype, v0, v1, v2, v3, v4, v5) DO NOTHING;
