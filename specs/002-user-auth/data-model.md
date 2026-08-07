# Data Model: 注册功能第一阶段（用户认证）

**Feature**: 002-user-auth
**Date**: 2026-08-05

## 设计约束

- 数据访问由 sqlc 生成类型安全代码（宪章 III），生成文件不手工修改
- 数据库完整性约束兜底应用层校验（唯一约束、外键、非空）
- 涉及多个写操作的认证状态变更（注册 = 建用户 + 建会话）在单事务中保持原子性
- 密码与令牌均不以明文存储

---

## 实体 1: users（用户账户）

### 表结构（迁移 000002_users）

```sql
CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_username ON users (username);
```

### 字段

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY | 账户唯一标识 |
| `username` | TEXT | UNIQUE NOT NULL | 登录标识，3–32 字符 `^[a-zA-Z0-9_]+$` |
| `password_hash` | TEXT | NOT NULL | Argon2id PHC 编码串（含盐与参数），永不存明文 |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 创建时间 |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 更新时间 |

### 校验规则（应用层 + 数据库兜底）

| 规则 | 应用层 | 数据库 |
|------|--------|--------|
| 用户名唯一 | 注册前查重（并发仍可能冲突） | `UNIQUE` 约束（最终兜底） |
| 用户名格式 | 3–32 字符 `^[a-zA-Z0-9_]+$` | —（长度隐含于格式） |
| 密码长度 | 8–72 字节 | — |

### 状态

无显式状态字段；账户删除由 `ON DELETE CASCADE` 联动删除其全部会话。

---

## 实体 2: sessions（会话）

### 表结构（迁移 000003_sessions）

```sql
CREATE TABLE sessions (
    id            BIGSERIAL PRIMARY KEY,
    token_hash    TEXT        NOT NULL UNIQUE,
    user_id       BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    last_used_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user_id ON sessions (user_id);
CREATE INDEX idx_sessions_token_hash ON sessions (token_hash);
```

### 字段

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY | 会话唯一标识 |
| `token_hash` | TEXT | UNIQUE NOT NULL | 原始令牌的 SHA-256 十六进制哈希，不存原始令牌 |
| `user_id` | BIGINT | FK → users(id) ON DELETE CASCADE | 所属用户 |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 创建时间 |
| `expires_at` | TIMESTAMPTZ | NOT NULL | 空闲过期（最近使用 + 24h，每次使用滑动续期） |
| `revoked_at` | TIMESTAMPTZ | NULL | 撤销时间（NULL = 未撤销） |
| `last_used_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 最近使用时间（滑动续期依据） |

### 状态转换

会话有效性判定（`SessionIsValid(now, created_at, expires_at)`）:

```
[active: revoked_at IS NULL AND now < expires_at AND now < created_at + 7天]
       │
       ├── 令牌使用且会话有效 ──► [续期: expires_at = now + 24h, last_used_at = now]
       │
       ├── 用户退出 ────────────────► [revoked: revoked_at = now()]
       │                                 │
       │                                 └──► 后续使用返回 AUTH_INVALID_TOKEN
       │
       ├── 空闲 > 24h (now >= expires_at) ──► [idle-expired] ──► 返回 AUTH_INVALID_TOKEN
       │
       ├── 超过 7 天绝对上限 (now >= created_at + 7天) ──► [lifetime-capped] ──► 返回 AUTH_INVALID_TOKEN
       │
       └── 用户删除 (CASCADE) ───────► 会话行删除
```

生命周期规则: **滑动空闲过期 24h + 绝对上限 7 天 + 软撤销**。
（规划初稿"绝对 24h + 距过期<7天续期"存在矛盾，实现阶段修正为此模型，见 research.md 决策 3。）

### 访问模式（sqlc 查询，写入 `db/queries/sessions.sql`）

| 查询 | 目的 | 事务性 |
|------|------|--------|
| `CreateSession` | 插入新会话（注册/登录后） | 注册时随用户插入同事务 |
| `GetSessionByTokenHash` | 中间件校验令牌（JOIN users 取用户名） | 读 |
| `RevokeSessionByTokenHash` | 退出时设置 revoked_at | 写（单写） |
| `TouchSession` | 滑动续期更新 last_used_at/expires_at | 写（单写） |

---

## 实体关系

```
┌──────────────────────┐       1    N       ┌──────────────────────┐
│        users         │ ◄──────────────────│       sessions       │
│   (用户账户)          │  ON DELETE CASCADE │   (会话)              │
│                      │                    │                      │
│ id            PK     │                    │ id            PK     │
│ username      UNIQUE │                    │ token_hash    UNIQUE │
│ password_hash        │                    │ user_id       FK     │
│ created_at           │                    │ expires_at           │
│ updated_at           │                    │ revoked_at    NULL   │
│                      │                    │ last_used_at         │
└──────────────────────┘                    └──────────────────────┘

一个用户可有多个并发会话；删除用户联动删除所有会话。
```

---

## 事务边界

| 操作 | 涉及写操作 | 事务要求 |
|------|-----------|----------|
| 注册 | INSERT user + INSERT session | **单事务**（pgxpool.BeginTx），任一失败整体回滚 |
| 登录 | INSERT session | 单写，无需显式事务 |
| 退出 | UPDATE session SET revoked_at | 单写，无需显式事务 |
| 滑动续期 | UPDATE session SET last_used_at/expires_at | 单写，无需显式事务 |

---

## 验证清单

- [ ] `users.username` 有 UNIQUE 约束；`sessions.token_hash` 有 UNIQUE 约束
- [ ] `sessions.user_id` 有 FK → users(id) ON DELETE CASCADE
- [ ] `sessions.expires_at` NOT NULL；`revoked_at` 可空
- [ ] `password_hash` 为 Argon2id PHC 编码串（含 `$argon2id$` 前缀）
- [ ] `token_hash` 为 SHA-256 十六进制（64 字符），永不含原始令牌
- [ ] 注册操作在单事务中完成（建用户 + 建会话）
- [ ] sqlc 生成代码不手工修改；Querier 接口暴露给服务层注入 mock
