WITH dashboard AS (
    SELECT id
    FROM menus
    WHERE parent_id IS NULL AND path = '/dashboard' AND name = 'Dashboard'
    ORDER BY id
    LIMIT 1
), inserted_dashboard AS (
    INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
    SELECT NULL, 0, '/dashboard', 'Dashboard', '#', TRUE,
           '{"title":"首页","icon":"vi-ant-design:dashboard-filled"}'::JSONB,
           '[]'::JSONB
    WHERE NOT EXISTS (SELECT 1 FROM dashboard)
    RETURNING id
), dashboard_menu AS (
    SELECT id FROM dashboard
    UNION ALL
    SELECT id FROM inserted_dashboard
), inserted_analysis AS (
    INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
    SELECT dashboard_menu.id, 0, 'analysis', 'Analysis', 'views/Dashboard/Analysis', TRUE,
           '{"title":"首页","affix":true,"noCache":true}'::JSONB,
           '[]'::JSONB
    FROM dashboard_menu
    WHERE NOT EXISTS (
        SELECT 1
        FROM menus
        WHERE parent_id = dashboard_menu.id AND path = 'analysis' AND name = 'Analysis'
    )
    RETURNING id
), dashboard_routes AS (
    SELECT id FROM dashboard_menu
    UNION ALL
    SELECT id FROM inserted_analysis
    UNION ALL
    SELECT menus.id
    FROM menus
    JOIN dashboard_menu ON menus.parent_id = dashboard_menu.id
    WHERE menus.path = 'analysis' AND menus.name = 'Analysis'
)
INSERT INTO role_menus (role_id, menu_id)
SELECT roles.id, dashboard_routes.id
FROM roles
CROSS JOIN dashboard_routes
WHERE roles.code IN ('test', 'user')
ON CONFLICT DO NOTHING;
