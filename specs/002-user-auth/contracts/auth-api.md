# Auth API Contract

**Feature**: 002-user-auth
**Date**: 2026-08-10
**Version**: 1.1.0

## Overview

本契约定义了用户认证 API。覆盖注册、登录、退出与当前用户查询。契约遵循宪章 IV：
明确的请求/响应/错误模型、稳定机器可读错误码、不暴露数据库行结构。

---

## Conventions

### Base URL

```
http://{host}:{port}/api/v1
```

### 认证头

受保护端点要求 `Authorization: Bearer <token>`。令牌为服务端签发的不透明字符串。

### 响应包装

- 成功: `{"data": ...}`
- 失败: `{"error": {"code": "ERROR_CODE", "message": "...", "field_errors": [...]?}}`

### 错误码

| 错误码 | HTTP | 场景 |
|--------|------|------|
| `AUTH_INVALID_CREDENTIALS` | 401 | 登录凭据错误（统一响应，防枚举） |
| `AUTH_USERNAME_TAKEN` | 409 | 用户名已被占用 |
| `AUTH_INVALID_TOKEN` | 401 | 令牌缺失/无效/过期/已撤销 |
| `AUTH_FORBIDDEN` | 403 | 已认证，但缺少管理端点要求的权限 |
| `AUTH_INVALID_INPUT` | 400 | 请求字段校验失败（含 `field_errors`） |
| `RATE_LIMITED` | 429 | 触发速率限制（含 `Retry-After` 头） |

`field_errors` 格式:
```json
[{"field": "username", "code": "TOO_SHORT", "message": "用户名长度需为 3-32 个字符"}]
```

### 安全约束

- 响应体永不包含密码、原始令牌、堆栈、内部路径
- 认证失败统一语义：无效凭据/令牌在错误码上不区分原因细节
- 所有端点带 `X-Request-Id` 请求关联

---

## Endpoints

### POST /auth/register — 注册

注册新账户。成功即登录（返回会话令牌）。

**Request**
```json
{
  "username": "alice",
  "password": "supersecret123"
}
```

**Validation**

| 字段 | 规则 |
|------|------|
| `username` | 必填，3–32 字符，`^[a-zA-Z0-9_]+$` |
| `password` | 必填，8–72 字节 |

**Response — 201 Created**
```json
{
  "data": {
    "token": "<opaque-token>",
    "token_type": "Bearer",
    "expires_in": 86400,
    "user": {
      "id": 1,
      "username": "alice",
      "created_at": "2026-08-05T10:00:00Z"
    }
  }
}
```

**Errors**

| 错误码 | HTTP | 说明 |
|--------|------|------|
| `AUTH_INVALID_INPUT` | 400 | 字段校验失败 |
| `AUTH_USERNAME_TAKEN` | 409 | 用户名已存在 |
| `RATE_LIMITED` | 429 | 每 IP 每小时 10 次 |

---

### POST /auth/login — 登录

使用用户名 + 密码登录，签发会话令牌。

**Request**
```json
{
  "username": "alice",
  "password": "supersecret123"
}
```

**Response — 200 OK**
```json
{
  "data": {
    "token": "<opaque-token>",
    "token_type": "Bearer",
    "expires_in": 86400,
    "user": {
      "id": 1,
      "username": "alice",
      "created_at": "2026-08-05T10:00:00Z"
    }
  }
}
```

**Errors**

| 错误码 | HTTP | 说明 |
|--------|------|------|
| `AUTH_INVALID_CREDENTIALS` | 401 | 用户名不存在或密码错误（统一响应） |
| `AUTH_INVALID_INPUT` | 400 | 字段校验失败 |
| `RATE_LIMITED` | 429 | 每 IP 15 分钟 10 次 + 每用户名 15 分钟 5 次 |

---

### POST /auth/logout — 退出

撤销当前会话令牌。幂等：无论令牌是否有效均返回成功，不暴露令牌状态。

**Request**
```
Authorization: Bearer <token>
```
无请求体。

**Response — 200 OK**
```json
{"data": {}}
```

写接口必须返回非空 JSON 信封；前端响应拦截器不接受空 body 的 2xx。

**Errors**

| 错误码 | HTTP | 说明 |
|--------|------|------|
| `AUTH_INVALID_TOKEN` | 401 | 仅当请求缺少 `Authorization` 头，或头格式错误（非 `Bearer <token>`）时返回 |
| — | 200 | 头格式正确但令牌本身无效/过期/已撤销时，**仍返回成功**（幂等撤销，不暴露令牌状态） |

> **语义澄清**：缺失/畸形头 → 401（协议层错误）；有效格式的无效令牌 → `200 {"data":{}}`（业务层幂等）。
> 客户端不应通过 logout 响应判断令牌有效性，应使用 `/auth/me`。

---

### GET /auth/me — 当前用户

返回当前令牌对应的用户信息。前端用于验证令牌有效性与初始化用户状态。

**Request**
```
Authorization: Bearer <token>
```

**Response — 200 OK**
```json
{
  "data": {
    "user": {
      "id": 1,
      "username": "alice",
      "account": "Alice",
      "email": "alice@example.com",
      "created_at": "2026-08-05T10:00:00Z",
      "department": {"id": 1, "name": "研发部"},
      "roles": [
        {"id": 2, "name": "管理员", "code": "admin"}
      ],
      "effective_permissions": [
        "departments.read",
        "departments.write",
        "roles.read",
        "roles.write",
        "users.read",
        "users.write"
      ]
    }
  }
}
```

`effective_permissions` 是用户所有角色权限的去重、有序并集；没有权限时返回 `[]`，不返回 `null`。前端以该字段作为真实管理菜单和按钮权限的唯一来源。

**Errors**

| 错误码 | HTTP | 说明 |
|--------|------|------|
| `AUTH_INVALID_TOKEN` | 401 | 令牌缺失/无效/过期/已撤销 |

---

## Rate Limiting

| 端点 | 维度 | 限制 | 窗口 |
|------|------|------|------|
| register | IP | 10 次 | 1 小时 |
| login | IP | 10 次 | 15 分钟 |
| login | username | 5 次 | 15 分钟 |

限流响应含 `Retry-After` 头（秒）。限流维度基于请求来源 IP 与请求体中的用户名。

---

## Contract Tests

以下测试必须通过才能声明契约合规:

1. **注册成功**: POST /auth/register `{alice, supersecret123}` → 201, data.token 非空,
   data.user.username="alice", data.user.id>0
2. **注册重复用户名**: 再次注册 alice → 409 `AUTH_USERNAME_TAKEN`
3. **注册校验失败**: 用户名 `ab` → 400 `AUTH_INVALID_INPUT`, field_errors[0].field="username";
   密码 7 字符 → 400 field_errors[0].field="password"
4. **登录成功**: POST /auth/login `{alice, supersecret123}` → 200, data.token 非空
5. **登录失败统一响应**: 错误密码 / 不存在用户名 → 均 401 `AUTH_INVALID_CREDENTIALS`，
   响应体结构一致
6. **me 有效**: 携带登录令牌 GET /auth/me → 200, data.user.username="alice"
7. **me 无效令牌**: 伪造令牌 / 过期令牌 / 已撤销令牌 → 均 401 `AUTH_INVALID_TOKEN`
8. **退出生效**: POST /auth/logout（携带令牌）→ `200 {"data":{}}`; 随后 GET /auth/me 用同令牌 → 401
9. **限流**: 超过登录限制后 → 429 `RATE_LIMITED`, 含 `Retry-After`
10. **秘密泄露**: 所有响应字符串匹配 password/token(原始值)/secret/stack → 零命中
11. **性能**: 100 次登录采样 P95 < 400ms（Argon2id m=64MB 单次哈希典型耗时 100–300ms；
   若实现实测 P95 更紧，可收紧本目标；若无法达成 <400ms，需调低 m 参数并更新本契约）

---

## Versioning

本契约采用语义化版本。当前版本 1.1.0。

- **向后兼容**: 不修改已有字段/错误码/状态码的前提下添加字段或端点
- **破坏性变更**: 必须提供版本化端点（如 `/v2/auth/login`）或经批准的同步迁移方案
