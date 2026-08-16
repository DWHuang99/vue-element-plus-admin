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

```powershell
.\.script\update-database.ps1
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
