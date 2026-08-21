DELETE FROM role_menus
WHERE role_id IN (SELECT id FROM roles WHERE code IN ('test', 'user'))
  AND menu_id IN (
      SELECT child.id
      FROM menus AS child
      JOIN menus AS parent ON parent.id = child.parent_id
      WHERE parent.path = '/dashboard'
        AND parent.name = 'Dashboard'
        AND child.path = 'analysis'
        AND child.name = 'Analysis'

      UNION

      SELECT id
      FROM menus
      WHERE parent_id IS NULL AND path = '/dashboard' AND name = 'Dashboard'
  );

DELETE FROM menus AS child
USING menus AS parent
WHERE child.parent_id = parent.id
  AND parent.path = '/dashboard'
  AND parent.name = 'Dashboard'
  AND child.path = 'analysis'
  AND child.name = 'Analysis';
