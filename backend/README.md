# Backend

## 一键更新数据库和 sqlc

```powershell
.\.script\update-database.ps1
```

脚本会依次启动 PostgreSQL、执行未应用的迁移，然后重新生成 sqlc 代码。

## 启动

```powershell
cd D:\vue-element-plus-admin\backend
docker compose up -d postgres
docker compose run --rm migrate
docker compose up --build -d api
```

`migrate` 只执行尚未应用的迁移。迁移记录保存在数据库的 `schema_migrations` 表中。

测试 Gin：

```powershell
Invoke-RestMethod http://localhost:8080/ping
```

预期响应：

```json
{"message":"pong"}
```

## 新增数据库变更

不要修改已经执行过的迁移。新增一组编号递增的文件，例如：

```text
internal/database/migrations/000002_add_user_email.up.sql
internal/database/migrations/000002_add_user_email.down.sql
```

然后执行：

```powershell
docker compose run --rm migrate
```

## 生成 sqlc 代码

```powershell
docker run --rm -v "${PWD}:/src" -w /src/internal/database sqlc/sqlc:1.31.1 generate
```

sqlc 读取迁移文件了解表结构，但不会执行迁移。

## 回滚最近一次迁移

```powershell
docker compose run --rm migrate `
  -path=/migrations `
  -database="postgres://vue_admin:vue_admin_dev@postgres:5432/vue_admin?sslmode=disable" `
  down 1
```

回滚可能删除数据，执行前先确认对应的 `.down.sql`。

## 停止

```powershell
docker compose down
```
