DO $migration$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'user_external_identity'
          AND column_name = 'id'
          AND is_identity = 'NO'
    ) THEN
        ALTER TABLE user_external_identity
            ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY;
    END IF;
END
$migration$;

-- ADD IDENTITY starts its sequence at 1 even when the table already contains
-- rows. Align it with the current maximum while keeping an empty table at 1.
SELECT setval(
    pg_get_serial_sequence('user_external_identity', 'id')::REGCLASS,
    COALESCE(MAX(id), 1),
    COUNT(*) > 0
)
FROM user_external_identity;
