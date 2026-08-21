DO $$
DECLARE
    authorization_id BIGINT;
BEGIN
    SELECT id
    INTO authorization_id
    FROM menus
    WHERE parent_id IS NULL
      AND path = '/authorization'
      AND name = 'Authorization'
    ORDER BY id
    LIMIT 1;

    IF authorization_id IS NULL THEN
        INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
        VALUES (
            NULL,
            0,
            '/authorization',
            'Authorization',
            '#',
            TRUE,
            '{"title":"router.authorization","icon":"vi-eos-icons:role-binding","alwaysShow":true}'::JSONB,
            '[]'::JSONB
        )
        RETURNING id INTO authorization_id;
    END IF;

    INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
    SELECT authorization_id, 0, 'department', 'Department',
           'views/Authorization/Department/Department', TRUE,
           '{"title":"router.department"}'::JSONB,
           '[
              {"id":101,"value":"system:department:read","label":"查看"},
              {"id":102,"value":"system:department:create","label":"新增"},
              {"id":103,"value":"system:department:update","label":"编辑"},
              {"id":104,"value":"system:department:delete","label":"删除"}
            ]'::JSONB
    WHERE NOT EXISTS (
        SELECT 1 FROM menus
        WHERE parent_id = authorization_id AND path = 'department' AND name = 'Department'
    );

    INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
    SELECT authorization_id, 0, 'user', 'User',
           'views/Authorization/User/User', TRUE,
           '{"title":"router.user"}'::JSONB,
           '[
              {"id":201,"value":"system:user:read","label":"查看"},
              {"id":202,"value":"system:user:create","label":"新增"},
              {"id":203,"value":"system:user:update","label":"编辑"},
              {"id":204,"value":"system:user:delete","label":"删除"}
            ]'::JSONB
    WHERE NOT EXISTS (
        SELECT 1 FROM menus
        WHERE parent_id = authorization_id AND path = 'user' AND name = 'User'
    );

    INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
    SELECT authorization_id, 0, 'menu', 'Menu',
           'views/Authorization/Menu/Menu', TRUE,
           '{"title":"router.menuManagement"}'::JSONB,
           '[
              {"id":301,"value":"system:menu:read","label":"查看"},
              {"id":302,"value":"system:menu:create","label":"新增"},
              {"id":303,"value":"system:menu:update","label":"编辑"},
              {"id":304,"value":"system:menu:delete","label":"删除"}
            ]'::JSONB
    WHERE NOT EXISTS (
        SELECT 1 FROM menus
        WHERE parent_id = authorization_id AND path = 'menu' AND name = 'Menu'
    );

    INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
    SELECT authorization_id, 0, 'role', 'Role',
           'views/Authorization/Role/Role', TRUE,
           '{"title":"router.role"}'::JSONB,
           '[
              {"id":401,"value":"system:role:read","label":"查看"},
              {"id":402,"value":"system:role:create","label":"新增"},
              {"id":403,"value":"system:role:update","label":"编辑"},
              {"id":404,"value":"system:role:delete","label":"删除"}
            ]'::JSONB
    WHERE NOT EXISTS (
        SELECT 1 FROM menus
        WHERE parent_id = authorization_id AND path = 'role' AND name = 'Role'
    );
END $$;

WITH RECURSIVE admin_routes AS (
    SELECT id
    FROM menus
    WHERE parent_id IS NULL
      AND path IN ('/dashboard', '/authorization')

    UNION ALL

    SELECT child.id
    FROM menus AS child
    JOIN admin_routes AS parent ON parent.id = child.parent_id
)
INSERT INTO role_menus (role_id, menu_id)
SELECT roles.id, admin_routes.id
FROM roles
CROSS JOIN admin_routes
WHERE roles.code = 'admin'
ON CONFLICT DO NOTHING;
