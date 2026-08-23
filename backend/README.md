# Backend services

后端按数据所有权拆分为三个进程：

- `iam-api`：auth、user、usermanagement、role、menu，拥有 IAM 数据库。其中 `user` 负责当前用户，`usermanagement` 负责管理员用户 CRUD 及跨服务部门信息编排。
- `department-api`：department，拥有 Department 数据库。
- `gateway`：对外暴露 `http://localhost:8080`，按路径转发请求。

## 数据库与 sqlc

每个业务服务拥有独立迁移和 sqlc 配置：

```text
migrations/iam
migrations/department
internal/database/iam
internal/database/department
```

IAM 的 `users.department_id` 是外部引用，不存在到 Department 数据库的外键。IAM 通过 Department 的 `GetDepartment` 校验用户部门，并通过 `BatchGetDepartments` 补齐用户列表的部门信息。Department 删除部门前通过 IAM 的 `CountUsersByDepartment` 检查用户引用。两个服务分别复用一个长期 gRPC 连接。

删除部门时会先把 Department 数据库中的 `deleting` 设为 `true`。IAM 创建、修改用户时会拒绝绑定删除中的部门；部门仍有用户或远程检查失败时，Department 会恢复 `deleting=false`，只有检查通过后才会物理删除。

更新两个数据库并重新生成 sqlc：

```bash
bash ./.script/update-database.sh
```

只重新生成两套 sqlc 代码：

```bash
bash ./.script/generate-sqlc.sh
```

修改 proto 后重新生成 Go 代码（需要 `protoc`、`protoc-gen-go` 和 `protoc-gen-go-grpc`）：

```powershell
.\scripts\update-grpc.ps1
```

## 启动

确保项目根目录存在匹配的 `private.pem` 和 `public.pem`，然后执行：

```powershell
cd D:\vue-element-plus-admin\backend
docker compose up -d --build
```

服务地址：

```text
Gateway        http://localhost:8080
IAM API        http://localhost:8081
Department API http://localhost:8082
Department gRPC department-api:50051（Docker 内部网络）
IAM gRPC        iam-api:50051（Docker 内部网络）
```

两个 migration 服务分别完成迁移后，IAM 和 Department 才会启动。生产环境必须覆盖 `.env.example` 中的数据库密码。

IAM 调用 Department gRPC 的配置：

```text
DEPARTMENT_GRPC_TARGET=department-api:50051
SERVICE_GRPC_TIMEOUT_MS=3000
```

Department 监听地址由 `DEPARTMENT_GRPC_ADDR` 配置，Compose 中使用 `:50051`。

Department 调用 IAM gRPC 使用 `IAM_GRPC_TARGET=iam-api:50051`，IAM 监听地址由 `IAM_GRPC_ADDR=:50051` 配置。

## OIDC 登录

IAM API 启动时会校验以下 OIDC 配置，缺失或 URL 格式错误时会直接停止启动：

```text
OIDC_ENABLED=true
OIDC_ISSUER=https://accounts.google.com
OIDC_CLIENT_ID=<provider-client-id>
OIDC_CLIENT_SECRET=<provider-client-secret>
OIDC_REDIRECT_URL=http://localhost:8080/api/v1/oauth/callback
OIDC_FRONTEND_REDIRECT_URL=http://localhost:4000/#/login
KEY_ENCRYPTION_KEY=<16-24-or-32-byte-secret>
```

OIDC 默认关闭；`OIDC_ENABLED=false` 时 IAM 不初始化服务商，也不注册 `/api/v1/oauth/*` 和 `/api/v1/gmail/*` 路由，因此未配置 OIDC 的部署仍可正常启动。开启后，其余六项配置均为必填。`KEY_ENCRYPTION_KEY` 支持原始字符串、hex 或 Base64 表示，解码后必须是 16、24 或 32 字节，用于在数据库中加密 Google access/refresh token；生产环境应使用随机生成且稳定保存的密钥，密钥丢失后已有 token 将无法解密。

`OIDC_REDIRECT_URL` 必须与服务商控制台登记的回调地址完全一致。登录成功后，IAM 设置 HttpOnly refresh cookie 并跳转到 `OIDC_FRONTEND_REDIRECT_URL`；前端再调用 `/api/v1/auth/refresh` 建立本系统会话，不会把 access token 放进 URL。

通过 Docker Compose 启动前，请复制 `.env.example` 为 `.env` 并替换 OIDC client ID 和 secret。前端构建还需设置 `VITE_OIDC_ENABLED=true` 才会显示 OIDC 登录入口。生产环境使用 HTTPS 时，还应设置 `COOKIE_SECURE=true`，并把两个回调 URL 改成实际 HTTPS 地址。

## 独立发布

Dockerfile 提供三个独立 Target，每个最终镜像只包含对应服务二进制：

```powershell
docker build --target iam -t vue-admin-iam .
docker build --target department -t vue-admin-department .
docker build --target gateway -t vue-admin-gateway .
```

Compose 已分别使用 `iam`、`department`、`gateway` Target。

IAM 发布：

```powershell
docker compose run --rm iam-migrate
docker compose up -d --build iam-api
```

Department 发布：

```powershell
docker compose run --rm department-migrate
docker compose up -d --build department-api
```

Gateway 发布：

```powershell
docker compose up -d --build gateway
```

## 网关配置

网关目标和超时全部通过环境变量配置：

```text
IAM_SERVICE_URL
DEPARTMENT_SERVICE_URL
GATEWAY_DIAL_TIMEOUT_MS
GATEWAY_RESPONSE_HEADER_TIMEOUT_MS
GATEWAY_READ_HEADER_TIMEOUT_MS
GATEWAY_READ_TIMEOUT_MS
GATEWAY_WRITE_TIMEOUT_MS
GATEWAY_IDLE_TIMEOUT_MS
```
