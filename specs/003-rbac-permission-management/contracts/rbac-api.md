# RBAC API Contract: 权限管理第一阶段

**Feature**: 003-rbac-permission-management
**Date**: 2026-08-07
**Base**: `/api/v1`（管理接口均需 `Authorization: Bearer <token>`，经 `middleware.Auth` 校验）

## 通用约定

- 成功信封：`{"data": <payload>}`
- 失败信封：`{"error": {"code": "<CODE>", "message": "<msg>", "field_errors": {...?}}}`
- JSON 字段统一 `snake_case`
- **所有写接口返回 JSON body（`200 {"data":{}}`），绝不返回 204** —— 前端响应拦截器对空 body 的 2xx 会误弹"请求失败"
- 校验失败统一 `400 AUTH_INVALID_INPUT`（含 `field_errors`）
- 未带/无效令牌 → `401 AUTH_INVALID_TOKEN`

---

## 角色

### GET `/roles` — 角色列表

查询参数：无。

**Response** `200`
```json
{
  "data": {
    "list": [
      {"id": 1, "name": "超级管理员", "code": "super_admin", "created_at": "2026-08-07T03:00:00Z"}
    ],
    "total": 3
  }
}
```

### POST `/roles` — 新建/更新角色

请求体：
```json
{"id": 1, "name": "运营", "code": "operator"}
```

| 字段 | 类型 | 规则 |
|------|------|------|
| `id` | int64 | 缺省 → 新建；提供 → 更新 |
| `name` | string | 必填，1–64 字符，全局唯一 |
| `code` | string | 必填，`^[a-z][a-z0-9_]*$`，全局唯一 |

**Response** `200`
```json
{"data": {}}
```

**Errors**:
- `400 AUTH_INVALID_INPUT` — `name`/`code` 缺失或格式非法（`field_errors`）
- `409 NAME_TAKEN` — `name` 或 `code` 已被占用
- `404 ROLE_NOT_FOUND` — 更新时 `id` 不存在

### POST `/roles/delete` — 批量删除角色

请求体：
```json
{"ids": [2, 3]}
```

**Response** `200`
```json
{"data": {}}
```

**Errors**:
- `400 AUTH_INVALID_INPUT` — `ids` 为空或含非法值
- `400` — 角色仍被用户引用（`field_errors.ids`：先解除用户角色关联）
- `404 ROLE_NOT_FOUND` — 含不存在的角色 id

---

## 部门

### GET `/departments` — 部门树

查询参数：无。

**Response** `200`
```json
{
  "data": {
    "list": [
      {"id": 1, "name": "研发部", "parent_id": null, "children": [{"id": 2, "name": "前端组", "parent_id": 1}]}
    ]
  }
}
```

> `list` 为树形结构（嵌套 `children`）。前端 `Department.vue` 通过 api 层把 `parent_id` 拼成 `parentName`。

### POST `/departments` — 新建/更新部门

请求体：
```json
{"id": 2, "name": "前端组", "parent_id": 1}
```

| 字段 | 类型 | 规则 |
|------|------|------|
| `id` | int64 | 缺省 → 新建；提供 → 更新 |
| `name` | string | 必填，1–64 字符，全局唯一 |
| `parent_id` | int64? | NULL → 根部门；须存在且 ≠ 自身（不能以自己为父） |

**Response** `200`
```json
{"data": {}}
```

**Errors**:
- `400 AUTH_INVALID_INPUT` — 字段缺失/格式非法/`parent_id` 为自身
- `409 NAME_TAKEN` — `name` 已被占用
- `404 DEPARTMENT_NOT_FOUND` — 更新时 `id` 或 `parent_id` 不存在

### POST `/departments/delete` — 批量删除部门

请求体：
```json
{"ids": [4, 5]}
```

**Response** `200`
```json
{"data": {}}
```

**Errors**:
- `400 AUTH_INVALID_INPUT` — `ids` 为空或含非法值
- `400` — 存在子部门（`field_errors.ids`：先处理下级部门）或部门下仍有用户（先解除用户关联）
- `404 DEPARTMENT_NOT_FOUND` — 含不存在的部门 id

---

## 用户

### GET `/users` — 分页用户列表

查询参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| `department_id` | int64? | 按部门过滤（含直接成员） |
| `page_index` | int | 从 1 起，默认 1 |
| `page_size` | int | 默认 10，最大 100 |
| `username` | string? | 关键词（模糊匹配 `username`） |
| `account` | string? | 关键词（模糊匹配 `account`） |

**Response** `200`
```json
{
  "data": {
    "list": [
      {
        "id": 1,
        "username": "admin",
        "account": "管理员",
        "email": "admin@example.com",
        "create_time": "2026-08-07T03:00:00Z",
        "role": "超级管理员",
        "department": {"id": 1, "name": "研发部"}
      }
    ],
    "total": 12
  }
}
```

> `role` 为角色名拼接字符串（逗号分隔，兼容前端表格单列展示）。

### POST `/users` — 新建/更新用户

请求体：
```json
{
  "id": 3,
  "username": "zhangsan",
  "account": "张三",
  "email": "zhangsan@example.com",
  "password": "s3cret-pass",
  "department_id": 2,
  "roles": [3]
}
```

| 字段 | 类型 | 规则 |
|------|------|------|
| `id` | int64 | 缺省 → 新建；提供 → 更新 |
| `username` | string | 必填，3–32 字符，`^[a-zA-Z0-9_]+$`，全局唯一 |
| `account` | string? | 1–64 字符，账号显示名 |
| `email` | string? | 合法邮箱格式（可选） |
| `password` | string? | **新建 MUST**，8–72 字符，Argon2id 哈希；**更新留空则不改** |
| `department_id` | int64? | NULL → 不设部门 |
| `roles` | int[] | 角色 id 列表，替换式（先删后插） |

**Response** `200`
```json
{"data": {}}
```

**Errors**:
- `400 AUTH_INVALID_INPUT` — 字段缺失/格式非法（含 `password` 缺失于新建）
- `409 NAME_TAKEN` — `username` 已被占用
- `404 USER_NOT_FOUND` — 更新时 `id` 不存在
- `404 ROLE_NOT_FOUND` / `404 DEPARTMENT_NOT_FOUND` — `roles`/`department_id` 引用不存在

### POST `/users/delete` — 批量删除用户

请求体：
```json
{"ids": [4, 5]}
```

**Response** `200`
```json
{"data": {}}
```

**Errors**:
- `400 AUTH_INVALID_INPUT` — `ids` 为空或含非法值
- `404 USER_NOT_FOUND` — 含不存在的用户 id

> 物理删除；`user_roles` 因 `ON DELETE CASCADE` 自动清理。

---

## 认证扩展

### GET `/auth/me` — 当前用户（扩展返回角色/部门）

**Response** `200`
```json
{
  "data": {
    "user": {
      "id": 1,
      "username": "admin",
      "account": "管理员",
      "email": "admin@example.com",
      "created_at": "2026-08-07T03:00:00Z",
      "department": {"id": 1, "name": "研发部"},
      "roles": [
        {"id": 1, "name": "超级管理员", "code": "super_admin"}
      ]
    }
  }
}
```

> 新注册用户经 `auth.Register` 默认写入角色 `user`，`roles` 恒非空。

### POST `/auth/logout` — 退出（契约修正）

现返回 `204`（空 body）→ 改为 `200 {"data":{}}`，避免前端拦截器误报"请求失败"。

---

## 错误码汇总

| 错误码 | HTTP | 场景 |
|--------|------|------|
| `AUTH_INVALID_INPUT` | 400 | 请求校验失败（含 `field_errors`） |
| `AUTH_INVALID_TOKEN` | 401 | 未带/无效/过期/已撤销令牌 |
| `ROLE_NOT_FOUND` | 404 | 角色 id 不存在 |
| `DEPARTMENT_NOT_FOUND` | 404 | 部门 id 不存在 |
| `USER_NOT_FOUND` | 404 | 用户 id 不存在 |
| `NAME_TAKEN` | 409 | 角色名/角色码/部门名/用户名重复 |
