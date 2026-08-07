# Quickstart: 权限管理第一阶段验证指南

**Feature**: 003-rbac-permission-management
**Date**: 2026-08-07

本指南提供可运行的端到端验证场景，证明本特性工作正常。
契约细节见 [contracts/rbac-api.md](./contracts/rbac-api.md)，数据模型见 [data-model.md](./data-model.md)。

---

## 前置条件

- Go 1.23+、Docker（testcontainers 用）
- Node.js + pnpm（前端）
- 本地或容器内 PostgreSQL（后端默认 `DATABASE_URL` 指向 localhost:5432，库名见 `backend/.env*` / 默认配置）

## 启动

### 后端

```bash
cd backend
make db-up        # 启动 PostgreSQL（如有 makefile 目标；否则用 docker run 起 pg）
make migrate-up   # 执行 000001–000004 迁移 + 种子
go run ./cmd/server    # 监听 :8080，注册 /api/v1/{auth,roles,departments,users}
```

### 前端

```bash
cd frontend
pnpm install
pnpm dev          # 开发服务器 :4000，VITE_API_BASE_PATH=http://localhost:8080
```

> 注意：`.env.base` 的 `VITE_API_BASE_PATH=http://localhost:8080` 使请求直达 Go 后端；
> `VITE_USE_MOCK` 对管理接口不再需要 mock（本阶段起全部走真实后端）。

---

## 验证场景

### 场景 1：登录/注册后正常跳转

1. 打开 `http://localhost:4000`。
2. **预期**：登录页可输入并提交；登录成功后跳转 Dashboard；注册成功后同样跳转。
   - 不再卡在登录页（原因为 `dynamicRouter` 依赖 `/mock/role/list` 404，已通过默认
     `dynamicRouter:false` 解锁静态路由）。
3. 刷新页面保持登录态；点击退出，返回登录页且原令牌失效。

### 场景 2：角色管理（真实数据）

1. 登录后进入 权限管理 → 角色管理。
2. **预期**：表格展示种子角色（超级管理员 `super_admin` / 管理员 `admin` / 普通用户 `user`）。
3. 新增角色（如"运营" code=`operator`）→ 保存后列表出现该角色。
4. 编辑该角色名 → 保存后生效。
5. 删除该角色 → 列表移除。
6. 尝试新建与种子角色同名的角色 → **预期** 409，页面提示"已存在"。

### 场景 3：部门管理（树形结构）

1. 进入 权限管理 → 部门管理。
2. **预期**：展示种子部门树（研发部 → 前端组/后端组，产品部，运营部，…）。
3. 在"前端组"下新建子部门 → 树中出现在其下。
4. 编辑部门名 → 生效。
5. 删除"研发部"（存在子部门）→ **预期** 400，提示先处理下级部门。
6. 删除刚建的空叶子部门 → 成功。

> 注：迁移不种子任何用户（既有用户仅来自注册）。因此用户列表初始为空，
> 需先注册/新建再验证。管理接口当前仅鉴权（`middleware.Auth`），未做角色级授权
> （角色授权属阶段二）；任何已登录用户均可管理。

### 场景 4：用户管理（分页 + 角色分配）

1. 注册一个账号后进入 权限管理 → 用户管理。
2. **预期**：列表初始为空（或仅显示手动新建的用户）。
3. 新建用户（填用户名/账号/邮箱/密码，选部门，分配"普通用户"角色）→ 保存成功，出现在列表（含角色列、所属部门列）。
4. 选择部门过滤 → 列表仅显示该部门成员；分页正常。
5. 编辑该用户，追加"管理员"角色 → 保存后 `role` 列显示两个角色名。
6. 删除该用户 → 列表移除；**预期** 该用户无法再登录（用户被物理删除后认证失败）。
7. 尝试新建已存在的用户名 → **预期** 409 提示用户名已占用。

### 场景 5：`/auth/me` 返回角色

```bash
# 先注册一个测试账号（注册即登录，返回 token）
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"me_test","password":"test-password-123"}' | jq -r '.data.token')

curl -s http://localhost:8080/api/v1/auth/me -H "Authorization: Bearer $TOKEN" | jq
```

**预期**：`data.user.roles` 非空数组（含默认角色 `{code:"user"}`），`{id, name, code}` 结构正确；
`data.user.department` 为 `{id, name}` 或 `null`。

### 场景 6：注册新用户自动获得默认角色

1. 前端注册页注册新账号（或用 `curl POST /api/v1/auth/register`）。
2. 立即 `GET /api/v1/auth/me`（用返回 token）。
3. **预期**：`roles` 包含普通用户（`code: "user"`）—— 注册事务内自动写入 `user_roles`。

### 场景 7：鉴权与写接口信封

```bash
# 未带令牌 → 401
curl -s http://localhost:8080/api/v1/roles
# 预期 {"error":{"code":"AUTH_INVALID_TOKEN",...}}，HTTP 401

# 写接口返回 JSON body 而非 204（新增角色为例）
curl -s -X POST http://localhost:8080/api/v1/roles \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"测试","code":"test"}'
# 预期 HTTP 200 + {"data":{}}，且前端不弹"请求失败"
```

### 场景 8：退出不再误报

1. 登录后点退出。
2. **预期**：正常回登录页，**不弹**"请求失败"错误（`logout` 已从 204 改为 `200 {"data":{}}`）。

---

## 契约与模型引用

- [contracts/rbac-api.md](./contracts/rbac-api.md) — 端点、请求/响应、错误码、鉴权要求
- [data-model.md](./data-model.md) — 表结构、约束、种子数据、删除行为

## 后续

- 阶段二：`/speckit-tasks` 生成 `tasks.md` 后进入实现。
