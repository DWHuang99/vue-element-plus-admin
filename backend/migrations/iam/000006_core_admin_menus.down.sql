WITH RECURSIVE managed_routes AS (
    SELECT id
    FROM menus
    WHERE parent_id IS NULL
      AND path IN ('/dashboard', '/authorization')

    UNION ALL

    SELECT child.id
    FROM menus AS child
    JOIN managed_routes AS parent ON parent.id = child.parent_id
)
DELETE FROM role_menus
WHERE role_id = (SELECT id FROM roles WHERE code = 'admin')
  AND menu_id IN (SELECT id FROM managed_routes);

DELETE FROM menus
WHERE parent_id = (
    SELECT id
    FROM menus
    WHERE parent_id IS NULL
      AND path = '/authorization'
      AND name = 'Authorization'
    ORDER BY id
    LIMIT 1
);

DELETE FROM menus
WHERE parent_id IS NULL
  AND path = '/authorization'
  AND name = 'Authorization';
