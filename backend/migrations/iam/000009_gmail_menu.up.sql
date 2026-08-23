DO $$
DECLARE
    mail_id BIGINT;
BEGIN
    SELECT id
    INTO mail_id
    FROM menus
    WHERE parent_id IS NULL
      AND path = '/mail'
      AND name = 'Mail'
    ORDER BY id
    LIMIT 1;

    IF mail_id IS NULL THEN
        INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
        VALUES (
            NULL,
            0,
            '/mail',
            'Mail',
            '#',
            TRUE,
            '{"title":"router.mail"}'::JSONB,
            '[]'::JSONB
        )
        RETURNING id INTO mail_id;
    END IF;

    INSERT INTO menus (parent_id, type, path, name, component, status, meta, permission_list)
    SELECT mail_id, 0, 'unread', 'UnreadMail',
           'views/Gmail/Unread', TRUE,
           '{"title":"router.mail","icon":"vi-ant-design:mail-filled"}'::JSONB,
           '[]'::JSONB
    WHERE NOT EXISTS (
        SELECT 1
        FROM menus
        WHERE parent_id = mail_id
          AND path = 'unread'
          AND name = 'UnreadMail'
    );
END $$;

WITH mail_routes AS (
    SELECT id
    FROM menus
    WHERE parent_id IS NULL
      AND path = '/mail'
      AND name = 'Mail'

    UNION ALL

    SELECT child.id
    FROM menus AS child
    JOIN menus AS parent ON parent.id = child.parent_id
    WHERE parent.parent_id IS NULL
      AND parent.path = '/mail'
      AND parent.name = 'Mail'
      AND child.path = 'unread'
      AND child.name = 'UnreadMail'
)
INSERT INTO role_menus (role_id, menu_id)
SELECT roles.id, mail_routes.id
FROM roles
CROSS JOIN mail_routes
WHERE roles.code IN ('admin', 'test', 'user')
ON CONFLICT DO NOTHING;
