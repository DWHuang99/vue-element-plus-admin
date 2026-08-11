# Initialize an administrator

Use this procedure only after the target user has registered normally. It does not create users, set passwords, or bypass the normal password hashing and registration flow.

## Preferred: CLI

Run from `backend/` with the migrated database URL in the environment:

```bash
DATABASE_URL='postgres://...' go run ./cmd/admin-init --username alice --role admin
```

For the highest built-in role:

```bash
DATABASE_URL='postgres://...' go run ./cmd/admin-init --username alice --role super_admin
```

Properties:

- `--role` accepts only `admin` or `super_admin`.
- The username must already exist in `users`.
- No password argument is accepted.
- Repeating the same command is safe; the role grant uses the existing `ON CONFLICT DO NOTHING` query.
- The command adds the requested role and preserves the user's other role assignments.

After promotion, have the user call `GET /api/v1/auth/me` again. Permission checks query current role grants on every request, so no server restart is required.

## Controlled SQL alternative

Use the CLI when possible. If operational policy requires direct SQL, run the following through `psql` with `ON_ERROR_STOP` enabled. Set `role` to exactly `admin` or `super_admin`.

```sql
\set ON_ERROR_STOP on
\set username 'alice'
\set role 'admin'

BEGIN;

SELECT id AS target_user_id
FROM users
WHERE username = :'username'
\gset

SELECT id AS target_role_id
FROM roles
WHERE code = :'role'
  AND code IN ('admin', 'super_admin')
\gset

INSERT INTO user_roles (user_id, role_id)
VALUES (:target_user_id, :target_role_id)
ON CONFLICT (user_id, role_id) DO NOTHING;

COMMIT;
```

If the user or built-in role does not exist, `\gset` does not define the required variable and `ON_ERROR_STOP` prevents the grant from continuing. Do not insert directly into `users`, store plaintext passwords, or grant permissions by editing the seeded permission catalog during this operation.
