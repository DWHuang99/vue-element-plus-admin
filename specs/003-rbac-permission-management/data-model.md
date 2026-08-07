# Data Model: 权限管理（RBAC）第一阶段

**Feature**: 003-rbac-permission-management
**Date**: 2026-08-07

## 设计约束

- 数据访问由 sqlc 生成类型安全代码（宪章 III），生成文件不手工修改
- 数据库完整性约束兜底应用层校验（唯一约束、外键、非空）
- 多写操作（建用户 + 角色关联）在单事务中保持原子性
- 迁移 000004 之前为既有 `users`（000002）/ `sessions`（000003）结构

---

## 实体 1: departments（部门，自引用树）

### 表结构（迁移 000004_rbac.up.sql）

```sql
CREATE TABLE departments (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    parent_id  BIGINT      REFERENCES departments(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_departments_parent_id ON departments (parent_id);
```

### 字段

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY | 部门唯一标识 |
| `name` | TEXT | UNIQUE NOT NULL | 部门名（1–64 字符） |
| `parent_id` | BIGINT | NULL, FK → departments(id) | 父部门；NULL 为根部门 |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 创建时间 |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 更新时间 |

### 关系与规则

- 自引用树：`parent_id` 指向 `id`；不允许自引用为父。
- 删除保护：存在子部门或关联用户时拒绝删除（400，提示先处理下级）。
- 应用校验：`name` 非空；`parent_id` 存在且非自身。

---

## 实体 2: roles（角色）

### 表结构

```sql
CREATE TABLE roles (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    code       TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 字段

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `id` | BIGSERIAL | PRIMARY KEY | 角色唯一标识 |
| `name` | TEXT | UNIQUE NOT NULL | 角色名（1–64 字符） |
| `code` | TEXT | UNIQUE NOT NULL | 角色码（`^[a-z][a-z0-9_]*$`），阶段二权限过滤用 |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 创建时间 |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | 更新时间 |

### 关系与规则

- 删除保护：存在关联用户时拒绝删除（400，提示先解除关联）。
- 种子角色：超级管理员（`super_admin`）、管理员（`admin`）、普通用户（`user`）。

---

## 实体 3: user_roles（用户-角色关联）

### 表结构

```sql
CREATE TABLE user_roles (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX idx_user_roles_role_id ON user_roles (role_id);
```

### 字段与约束

| 字段 | 类型 | 约束 |
|------|------|------|
| `user_id` | BIGINT | FK → users(id) ON DELETE CASCADE |
| `role_id` | BIGINT | FK → roles(id) ON DELETE CASCADE |
| 联合主键 | (user_id, role_id) | 防止重复分配 |

### 关系

- users ↔ roles 多对多。
- 保存用户时以"先删后插"替换该用户的角色集，与用户写入同一事务。

---

## 实体 4: users（扩展）

### 表结构变更（ALTER，迁移 000004）

```sql
ALTER TABLE users
    ADD COLUMN account       TEXT,
    ADD COLUMN email         TEXT,
    ADD COLUMN department_id BIGINT REFERENCES departments(id);

CREATE INDEX idx_users_department_id ON users (department_id);
```

### 新增字段

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `account` | TEXT | NULL | 账号显示名（1–64 字符） |
| `email` | TEXT | NULL | 邮箱（可选，格式校验） |
| `department_id` | BIGINT | NULL, FK → departments(id) | 所属部门 |

### 规则

- `username` 仍是登录标识（3–32 字符 `^[a-zA-Z0-9_]+$`，沿用现有校验）。
- 新建用户 MUST 传 `password`（8–72 字符，Argon2id 哈希）；编辑不传密码则不改。
- 用户名全局唯一（既有 UNIQUE 约束兜底）；用户列表按部门过滤分页。

---

## 种子数据（迁移 000004 内插入，幂等）

- 部门树：研发部（含：前端组、后端组）、产品部、运营部、市场部、销售部、客服部。
- 角色：超级管理员（`super_admin`）、管理员（`admin`）、普通用户（`user`）。
- 为既有用户补默认角色：普通用户（`user`）。

> 种子依赖：`000004_rbac.up.sql` 中建表 → 插部门/角色 → 补 `user_roles`（依赖既有 `users`）。

---

## 删除行为汇总

| 目标 | 行为 |
|------|------|
| 删除部门 | 存在子部门或关联用户 → 400；否则物理删除 |
| 删除角色 | 存在关联用户 → 400；否则物理删除（无关联时级联 `user_roles` 已空） |
| 删除用户 | 物理删除；`user_roles` 级联清理（不阻止） |

## 完整性约束清单（数据库层兜底）

1. `departments.name` UNIQUE
2. `roles.name` / `roles.code` UNIQUE
3. `user_roles` 联合主键 (user_id, role_id)
4. `user_roles.user_id` / `role_id` FK ON DELETE CASCADE
5. `users.department_id` FK → departments(id)
6. `users.username` UNIQUE（既有）
