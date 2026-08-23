DELETE FROM menus AS child
USING menus AS parent
WHERE child.parent_id = parent.id
  AND parent.parent_id IS NULL
  AND parent.path = '/mail'
  AND parent.name = 'Mail'
  AND child.path = 'unread'
  AND child.name = 'UnreadMail';

DELETE FROM menus
WHERE parent_id IS NULL
  AND path = '/mail'
  AND name = 'Mail';
