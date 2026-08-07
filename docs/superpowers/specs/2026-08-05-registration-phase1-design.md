# 注册功能第一阶段设计文档

**Date**: 2026-08-05
**Status**: Approved
**Scope**: 注册、密码登录、退出、服务端会话管理 + 前端认证接线

## 背景与范围

基于已完成的最小化后端脚手架（Go 1.23 + Gin + PostgreSQL + sqlc + golang-migrate），
实现宪章规定的认证第一阶段：基础注册、密码登录、退出与必要的会话管理。

- 不含验证码、邮箱所有权验证（宪章阶段 2）
- 不含 OAuth/OIDC/SSO（宪章阶段 3）
- 不含角色/权限/授权（独立功能）
- 前端接线仅覆盖认证相关接口，角色/权限/菜单保持 Mock

## 架构决策

### 方案选择

- **方案 A（采纳）**: 标准服务层认证 — `internal/auth` 包（service/password/token/handler 分离）
- 方案 B 拒绝: 处理器直连 DB，违反宪章 IV（业务规则 MUST 在可独立测试的服务层）
- 方案 C 拒绝: DDD 仓储模式，违反宪章 II（不得引入未被需求使用的抽象）

### 会话机制: 服务端不透明令牌（Bearer）

- 32 字节 `crypto/rand` 随机令牌
- 数据库仅存 SHA-256 哈希（泄漏 DB 不暴露有效令牌）
- 客户端经 `Authorization: Bearer` 头携带，前端存 localStorage
- 无 CSRF 面（非 Cookie）；前端需防 XSS

## 数据模型

### users 表

| 字段 | 类型 | 约束 |
|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY |
| `username` | TEXT | UNIQUE NOT NULL, 3–32 字符, `^[a-zA-Z0-9_]+$` |
| `password_hash` | TEXT | NOT NULL — Argon2id 编码串（含盐） |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |

### sessions 表

| 字段 | 类型 | 约束 |
|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY |
| `token_hash` | TEXT | UNIQUE NOT NULL — SHA-256 哈希 |
| `user_id` | BIGINT | NOT NULL, FK → users(id), ON DELETE CASCADE |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() |
| `expires_at` | TIMESTAMPTZ | NOT NULL — 创建 + 24h 绝对过期 |
| `revoked_at` | TIMESTAMPTZ | NULL — 撤销时间 |
| `last_used_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() — 滑动续期依据 |

## API 契约

前缀 `/api/v1`。成功 `{"data": ...}`，失败 `{"error": {"code", "message"}}`。

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| POST | `/api/v1/auth/register` | 否 | `{username, password}` → 201 `{token, user}`（注册即登录） |
| POST | `/api/v1/auth/login` | 否 | `{username, password}` → 200 `{token, user}` |
| POST | `/api/v1/auth/logout` | 是 | 撤销当前会话令牌 → 204 |
| GET | `/api/v1/auth/me` | 是 | 返回当前用户 → 200 `{user}` |

### 错误码

| 错误码 | HTTP | 场景 |
|--------|------|------|
| `AUTH_INVALID_CREDENTIALS` | 401 | 登录失败（统一响应防枚举） |
| `AUTH_USERNAME_TAKEN` | 409 | 用户名已被占用 |
| `AUTH_INVALID_TOKEN` | 401 | 令牌无效/过期/已撤销 |
| `AUTH_INVALID_INPUT` | 400 | 请求校验失败（含 `field_errors` 明细） |
| `RATE_LIMITED` | 429 | 触发速率限制 |

## 安全设计

- **密码哈希**: Argon2id（时间=3、内存=64MB、并行=4、盐 16B、输出 32B），
  编码 `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>`（PHC 格式，便于参数升级）
- **密码策略**: 长度 8–72 字符（NIST 长度优先，不强制字符组合）
- **防枚举**: 登录失败统一 `AUTH_INVALID_CREDENTIALS`，不区分用户名不存在/密码错误
- **速率限制**: 内存滑动窗口；注册 每 IP/h 10 次；登录 每 IP/15min 10 次 + 每用户名/15min 5 次
- **令牌生命周期**: 生成 32B 随机；`expires_at` 24h 绝对过期；`last_used_at` 滑动续期
  （距过期 < 7 天才续）；退出设 `revoked_at`；中间件校验 `revoked_at IS NULL`

## 前端接线（仅认证）

- `src/api/login/`: 注册/登录/退出/me 接真实后端
- `src/api/request/`: axios 拦截器附加 Bearer 令牌、401 跳登录
- `src/store/modules/user.ts`: 认证 action 接真实 API
- `src/views/Login/`: 增加注册切换
- 角色/权限/菜单保持 Mock；基础地址用 `VITE_API_BASE_URL`

## 测试策略

| 层 | 内容 | 工具 |
|----|------|------|
| 单元 | password/token/service（mock 注入） | testify |
| 集成 | 注册→登录→me→退出 全流程 + 安全场景 | testcontainers |
| 契约 | 端点请求/响应/错误码/状态码 | httptest |
| 迁移 | users/sessions 建表 + 约束 + FK | testcontainers |

关键集成场景: 重复用户名 409；登录失败统一 401；无效/过期/已撤销令牌 401；
退出后原令牌 401；限流 429；DB 验证哈希非明文、令牌哈希非原始值、含盐。

## 依赖

- `golang.org/x/crypto/argon2`
- 前端无新依赖

## 非目标

邮箱验证/验证码、密码重置、OAuth/OIDC/SSO、角色权限授权、刷新令牌、CSRF（Bearer 方案无此面）。
