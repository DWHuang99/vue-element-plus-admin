# Data Model: 最小化后端脚手架

**Feature**: 001-backend-scaffold
**Date**: 2026-08-04

## 设计约束

根据 FR-013，本脚手架 **禁止** 创建用户、认证或其他业务领域数据模型。
本阶段唯一与数据库相关的结构是迁移工具 `golang-migrate` 自动创建的版本追踪表。

---

## 实体 1: schema_migrations（由 golang-migrate 管理）

### 说明

此表由 `golang-migrate/migrate` 库在首次执行迁移时自动创建，用于追踪已应用的迁移版本。
应用代码不直接读写此表 —— 所有操作均通过 `migrate` 包的 API 完成。

### 表结构

```sql
-- 由 golang-migrate 自动创建，此 DDL 仅用于文档说明
CREATE TABLE schema_migrations (
    version BIGINT PRIMARY KEY,
    dirty   BOOLEAN NOT NULL
);
```

### 字段

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `version` | `BIGINT` | PRIMARY KEY | 已应用的迁移版本号（Unix 纳秒时间戳） |
| `dirty` | `BOOLEAN` | NOT NULL | 迁移失败时标记为 true；脏状态下服务无法启动 |

### 状态转换

```
[初始: 表不存在]
       │
       ▼ 首次 migrate.Up() 成功
[clean: dirty=false, version=N]
       │
       ├── migrate.Up() 成功 ──► [clean: dirty=false, version=N+1]
       │
       └── migrate.Up() 失败 ──► [dirty: dirty=true, version=N]
                                      │
                                      ▼ 需要手动修复
                                 [手动清除 dirty 标志或修复迁移后重试]
```

### 访问模式

- **读取**: `migrate.Up()` 内部查询 `schema_migrations` 以确定待执行迁移
- **写入**: `migrate.Up()` 内部在事务中 INSERT/UPDATE `schema_migrations`
- **禁止**: 应用代码 MUST NOT 直接对此表执行 SQL 查询或修改

---

## 实体 2: Config（内存结构体，不持久化）

### 说明

运行时配置通过 Viper 从环境变量加载到 Go 结构体。仅在进程内存中存在，
不写入数据库。

### 结构体

```go
type Config struct {
    Server   ServerConfig
    Database DatabaseConfig
    Log      LogConfig
}

type ServerConfig struct {
    Host         string        // SERVER_HOST, default: "0.0.0.0"
    Port         int           // SERVER_PORT, default: 8080
    ReadTimeout  time.Duration // SERVER_READ_TIMEOUT, default: 30s
    WriteTimeout time.Duration // SERVER_WRITE_TIMEOUT, default: 30s
    IdleTimeout  time.Duration // SERVER_IDLE_TIMEOUT, default: 60s
}

type DatabaseConfig struct {
    URL             string        // DATABASE_URL, required, no default
    MaxConns        int32         // DATABASE_MAX_CONNS, default: 25
    MinConns        int32         // DATABASE_MIN_CONNS, default: 5
    MaxConnLifetime time.Duration // DATABASE_MAX_CONN_LIFETIME, default: 1h
    MaxConnIdleTime time.Duration // DATABASE_MAX_CONN_IDLE_TIME, default: 30m
}

type LogConfig struct {
    Level  string // LOG_LEVEL, default: "info" (debug/info/warn/error)
    Format string // LOG_FORMAT, default: "text" (text/json)
}
```

### 校验规则

| 字段 | 规则 | 失败行为 |
|------|------|----------|
| `ServerConfig.Port` | 1–65535 | 启动失败，报告无效端口号 |
| `DatabaseConfig.URL` | 非空，合法 PostgreSQL URL 格式 | 启动失败，报告"数据库 URL 缺失或格式无效" |
| `DatabaseConfig.MaxConns` | 1–100 | 启动失败，报告无效值 |
| `LogConfig.Level` | debug / info / warn / error | 缺省为 "info"，记录校验警告 |
| `LogConfig.Format` | text / json | 缺省为 "text"，记录校验警告 |

### 秘密安全

- `DATABASE_URL` 在日志和错误消息中 MUST 仅显示为配置项名称，不暴露连接字符串内容
- 所有配置项通过 Viper 加载后，日志中仅记录"配置已加载"，不枚举具体值
- 测试中验证: 错误消息包含配置键名但不包含实际值

---

## 实体 3: HealthStatus（瞬态，不持久化）

### 说明

健康检查的响应载荷，每次请求实时计算各依赖状态后生成。
无数据库存储。

### 结构体

```go
type HealthResponse struct {
    Status    string            `json:"status"`    // "ok" | "degraded"
    Timestamp time.Time         `json:"timestamp"` // RFC 3339
    Checks    map[string]string `json:"checks,omitempty"` // 仅就绪检查包含
}
```

### 状态判定规则

| 端点 | Status | 条件 |
|------|--------|------|
| `/health/live` | `ok` | 进程存活（总是返回） |
| `/health/ready` | `ok` | 数据库连接成功 AND 迁移已执行 |
| `/health/ready` | `degraded` | 数据库不可达 或 迁移失败 或 存在脏状态 |

### 生命期

- 存活检查: 处理器内联生成，无状态
- 就绪检查: 每次请求 ping 数据库 (`SELECT 1`) + 检查 `schema_migrations.dirty = false`

---

## 实体关系总结

```
┌──────────────────────────────────────────────┐
│              进程内存 (Process Memory)          │
│                                                │
│  ┌──────────────┐    ┌──────────────────────┐ │
│  │   Config     │    │   HealthResponse     │ │
│  │ (启动时加载)  │    │ (每次请求实时生成)    │ │
│  └──────┬───────┘    └──────────────────────┘ │
│         │                                       │
│         │  DatabaseConfig.URL                   │
│         ▼                                       │
└──────────────────────────────────────────────┘
          │
          │ pgx 连接
          ▼
┌──────────────────────────────────────────────┐
│           PostgreSQL (持久化存储)               │
│                                                │
│  ┌──────────────────────────────┐             │
│  │     schema_migrations        │             │
│  │  (golang-migrate 自动管理)    │             │
│  └──────────────────────────────┘             │
│                                                │
│  ⚠ 无业务数据表 —— 本阶段禁止创建              │
└──────────────────────────────────────────────┘
```

---

## 验证清单

- [ ] `schema_migrations` 表由 golang-migrate 自动创建，应用代码不直接操作
- [ ] `Config` 结构体中 `DatabaseConfig.URL` 标记为非空必填项
- [ ] 错误消息包含配置键名但不含值内容（通过测试验证）
- [ ] 就绪检查同时验证数据库连接成功 AND `schema_migrations.dirty` 状态
- [ ] 本阶段无用户、认证、业务领域数据模型（符合 FR-013）
