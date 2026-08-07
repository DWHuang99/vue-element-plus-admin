# Research: 最小化后端脚手架技术决策

**Feature**: 001-backend-scaffold
**Date**: 2026-08-04
**Status**: Complete

## 研究摘要

本文档记录后端脚手架所有关键技术决策的研究过程。每个决策包含:选择方案、选择理由、
拒绝的替代方案及拒绝原因。所有 NEEDS CLARIFICATION 已解决。

---

## 决策 1: Go 版本

**Decision**: Go 1.23

**Rationale**:
- Go 1.23 是当前最新稳定版本（2024年8月发布）
- 增强了 `log/slog` 标准库（结构化日志），消除了第三方日志库依赖
- 改进了 `net/http` 路由匹配和优雅停止，对 Gin 框架有上游性能提升
- 1.21+ 的 `slices`/`maps` 标准库包减少样板代码
- 项目宪章要求 Go，该版本是满足约束的最优选择

**Alternatives considered**:
- Go 1.22: 已稳定但缺少 1.23 的部分优化（范围函数迭代器、Timer/Ticker 改进）
- Go 1.24 (unstable): 尚未发布，不满足脚手架稳定性要求

---

## 决策 2: HTTP 框架

**Decision**: Gin 1.10 (`github.com/gin-gonic/gin`)

**Rationale**:
- 宪章明确要求使用 Gin
- 轻量级、高性能（基于 httprouter 基数树）
- 丰富的中间件生态（恢复、日志、CORS 等）
- 内建优雅停止支持（`http.Server.Shutdown`）
- 适合 API 服务而非全栈渲染——与本功能完全匹配
- v1.10 是最新稳定版，修复了多个安全公告

**Alternatives considered**:
- `net/http` 标准库: 功能完备但缺少路由参数、中间件链等便利抽象；更多样板代码
- Chi: 轻量级、惯用 Go 风格，但与宪章要求不一致
- Echo: 功能类似 Gin，但社区稍小，宪章约束优先
- Fiber: 基于 fasthttp，非标准 HTTP 语义，生态兼容性差

---

## 决策 3: PostgreSQL 驱动

**Decision**: pgx v5 (`github.com/jackc/pgx/v5`)

**Rationale**:
- sqlc 官方推荐的 PostgreSQL 驱动
- 原生 PostgreSQL 协议实现，性能优于 database/sql + lib/pq
- 支持连接池（pgxpool）、批量操作、COPY 协议、通知/监听
- 与 golang-migrate 兼容（通过 `database/sql` 适配器或直接 pgx 连接）
- 类型安全，与 sqlc 生成的类型系统无缝衔接

**Alternatives considered**:
- `lib/pq`: 传统的 database/sql 驱动，维护缓慢，不支持 pgx 的高级特性
- `pgx` v4: 旧版本，sqlc 1.28 推荐 v5 以获得更好的类型支持
- ORM (GORM/Bun): 宪章禁止 ORM 隐式行为；sqlc 是宪章唯一授权的数据访问方式

---

## 决策 4: SQL 代码生成

**Decision**: sqlc 1.28 (`github.com/sqlc-dev/sqlc`)

**Rationale**:
- 宪章要求使用 sqlc 进行类型安全 SQL 代码生成
- 从 SQL 文件生成 Go 代码，SQL 即契约——可直接审查
- 完全避免 ORM N+1 查询、延迟加载等隐式行为
- 编译期类型检查，消除运行时 SQL 拼写错误
- v1.28 支持 PostgreSQL 17 的所有数据类型

**Alternatives considered**:
- 手工编写 database/sql: 类型不安全，字符串拼接 SQL，审查困难
- GORM Gen: 从模型生成代码，反向依赖——先有 Go 结构体后有 SQL，不符合宪章
- sqlx: 反射型绑定，类型安全弱于 sqlc，无编译期验证

---

## 决策 5: 数据库迁移工具

**Decision**: golang-migrate v4.18 (`github.com/golang-migrate/migrate/v4`)

**Rationale**:
- Go 生态中最成熟的嵌入式数据库迁移方案
- 支持从 `embed.FS` 运行迁移——编译进二进制，部署无需额外文件
- 纯 SQL 迁移文件，与 sqlc 的 SQL 文件互补
- 支持 PostgreSQL 事务包装（每个迁移文件自动在事务中执行）
- 版本追踪表 `schema_migrations` 自动创建，无额外配置
- Dirty 状态检测（迁移失败时标记，避免部分应用）

**Alternatives considered**:
- Goose: 支持 Go 和 SQL 迁移，但 Go 迁移模式鼓励混入业务逻辑，违反宪章"SQL 作为单一事实来源"
- Atlas: 声明式迁移，功能强大但学习曲线高；脚手架阶段过度引入复杂性
- Flyway/Liquibase: Java 生态，需要 JVM 运行时，不符合 Go 项目定位
- 手写迁移脚本: 缺乏版本追踪、回滚、dirty 检测等基础能力

---

## 决策 6: 结构化日志

**Decision**: `log/slog`（Go 标准库）

**Rationale**:
- Go 1.21 起标准库内置，零外部依赖
- 结构化键值对日志，支持日志级别（Debug/Info/Warn/Error）
- 支持 Handler 扩展——开发环境用 `slog.NewTextHandler`（人类可读），生产环境用 `slog.NewJSONHandler`（机器解析）
- 通过 `slog.With` 支持请求 ID、trace ID 等上下文属性关联
- 满足宪章 V 中"结构化日志 + 请求关联"的要求

**Alternatives considered**:
- Zap: 高性能但 API 复杂，脚手架阶段无极端性能需求
- Zerolog: 零分配设计，API 不如 slog 直观，社区倾向标准库
- Logrus: 维护模式，作者已不推荐新项目使用

---

## 决策 7: 配置管理

**Decision**: Viper 1.19 (`github.com/spf13/viper`)

**Rationale**:
- Go 生态最成熟的配置库
- 支持环境变量、配置文件（YAML/JSON/TOML）、命令行标志的优先级链
- 类型安全（`viper.GetString`、`viper.GetInt` 等）
- 配置项绑定到结构体（`mapstructure` tag）便于集中管理
- 宪章要求配置来自环境或受控配置源——Viper 以环境变量为最高优先级

**Alternatives considered**:
- `os.Getenv` + 手工解析: 重复造轮子，缺少文件、默认值、类型转换等便利功能
- `caarlos0/env`: 轻量级结构体 tag 方案，但缺少 Viper 的多源优先级和文件支持
- Koanf: 更轻量，但社区和文档不如 Viper 成熟

---

## 决策 8: 测试框架

**Decision**: `testing`（标准库）+ `testify` v1.10 + `testcontainers-go` v0.34

**Rationale**:

**单元测试** — `testing` + `testify/assert` + `testify/require`:
- 标准库测试运行器，零学习成本
- testify 提供可读的断言方法（`assert.Equal`、`assert.NoError`）
- `require` 在断言失败时立即停止测试，适合前置条件检查
- 不引入 BDD 框架（Ginkgo/Gomega），保持测试代码简单直接

**集成测试** — `testcontainers-go` + `pgx`:
- 在 Docker 中启动真实 PostgreSQL 容器，验证完整的 SQL 迁移和查询
- 每次测试独立数据库实例，测试间完全隔离
- 容器自动生命周期管理（测试结束自动清理）
- 适合 CI 环境（需要 Docker 运行时）

**测试边界定义**:

| 测试类型 | 范围 | 工具 | 目的 |
|----------|------|------|------|
| 单元测试 | 配置校验、中间件逻辑、日志格式化 | `testing` + `testify` | 验证独立函数行为 |
| 集成测试 | 数据库迁移、健康检查端点、完整启动/停止流程 | `testing` + `testcontainers-go` | 验证跨边界行为 |
| 契约测试 | API 请求/响应结构、状态码、错误格式 | `testing` + `httptest` | 验证 API 契约合规 |

**Alternatives considered**:
- Ginkgo + Gomega: BDD 风格，额外学习成本，脚手架规模不需要
- 仅标准库 `testing`: 缺少便利断言，错误信息不够可读
- 内存 SQLite 替代 PostgreSQL 集成测试: 宪章要求真实 PostgreSQL 环境，SQLite 的 SQL 方言差异会导致测试误报

---

## 决策 9: 项目目录结构

**Decision**: `cmd/server/main.go` + `internal/{config,database,health,middleware,server,logging}`

**Rationale**:
- 遵循 Go 社区标准项目布局（`golang-standards/project-layout` 的精简版）
- `cmd/` 包含可执行入口，`internal/` 保护内部包不被外部 import
- 按职责分子包而非按层分子包——避免过早抽象
- 每个子包自包含测试文件，便于隔离运行和 CI 并行
- 水平扩展:新增业务模块（未来 `internal/auth`、`internal/user`）只需在 `internal/` 下加子包
- 垂直边界:SQL 迁移在 `db/migrations/`、查询在 `db/queries/`，与 Go 代码独立

**Alternatives considered**:
- 单 `main.go` 文件: 所有代码在一个包，不可维护
- `pkg/` 目录: 暗示对外暴露 API，脚手架阶段无公共 API，过度设计
- Clean Architecture 分层: 接口层、用例层、实体层——脚手架阶段过于复杂，未来需要时重构

---

## 决策 10: 优雅停止实现

**Decision**: `os/signal` + `http.Server.Shutdown` + context 超时

**Rationale**:
- `signal.NotifyContext` 捕获 SIGINT/SIGTERM，返回可取消 context
- `http.Server.Shutdown` 等待活跃请求完成（不中断正在处理的请求）
- 设置硬超时（10秒）作为兜底——超时后强制退出
- 停止顺序:停止接受新请求 → 等待活跃请求完成 → 关闭数据库连接池 → 退出

**伪代码流程**:
```
1. 启动 HTTP server（goroutine）
2. 阻塞等待 SIGINT/SIGTERM
3. 创建 10s 超时 context
4. srv.Shutdown(ctx) → 停止接受新请求，等待活跃请求完成
5. db.Close() → 关闭连接池
6. 退出或 ctx 超时强制退出
```

**Alternatives considered**:
- 仅 `os.Signal` + `os.Exit`: 不等待活跃请求完成，可能导致请求丢失
- 第三方 graceful shutdown 库: 额外依赖，标准库已足够

---

## 决策 11: 健康检查设计

**Decision**: 两个独立端点 — `/health/live` (存活) + `/health/ready` (就绪)

**Rationale**:
- 生存性检查: 仅验证进程未崩溃，返回 200（不检查依赖）
- 就绪性检查: 验证所有关键依赖可用（数据库连接 + 迁移已应用），成功 200 / 失败 503
- 与 Kubernetes 探针模型对齐——进程编排平台可直接使用
- 就绪检查失败时不退出进程，仅返回 503（满足 FR-006: 存储恢复后自动转为就绪）

**响应格式**:
```json
{
  "status": "ok",           // "ok" | "degraded"
  "timestamp": "2026-08-04T10:00:00Z",
  "checks": {
    "database": "ok"         // "ok" | "unavailable" — 仅就绪检查包含
  }
}
```

**Alternatives considered**:
- 合并为单一 `/health` 端点: 无法区分"进程存活"和"依赖可用"，不满足 FR-005
- 仅就绪检查包含详细信息: 已采纳——存活检查保持最简（仅进程状态）

---

## 决策 12: 自动迁移策略

**Decision**: 启动时通过 golang-migrate 自动应用未执行的迁移

**Rationale**:
- 满足 FR-013: "每次启动时自动检测并应用尚未执行的数据库结构变更"
- `migrate.Up()` 仅执行未应用的迁移，已应用的自动跳过
- 迁移文件嵌入二进制（`embed.FS`），零外部文件依赖
- 迁移失败时 `migrate.Up()` 返回错误 → 记录日志 → 退出（不重试）
- 使用 PostgreSQL 事务: 每个迁移文件在单个事务中执行，失败自动回滚

**错误处理**:
```
if err := m.Up(); err != nil && err != migrate.ErrNoChange {
    slog.Error("migration failed", "error", err.Error()) // 不含秘密
    os.Exit(1) // 快速失败，不重试
}
```

**安全约束**:
- 日志仅记录迁移文件名和错误类型，不记录 SQL 内容（防止日志中泄露 schema 信息）
- 迁移文件通过 Go embed 嵌入，编译期固定，不可在运行时修改
- Dirty lock 检测: 如果上次迁移留下 dirty 状态，新启动强制要求手动修复

**Alternatives considered**:
- 独立迁移命令 (`./server migrate`): 增加运维步骤，违反"10分钟从零到就绪"的 SC-001
- 仅开发环境自动迁移: 增加环境判断逻辑，复杂度上升且不减少风险

---

## 决策 13: 开发环境依赖

**Decision**: Docker Compose 提供本地 PostgreSQL 实例

**Rationale**:
- 开发者无需手动安装和配置 PostgreSQL
- 保证开发环境与测试环境的数据库版本一致
- Docker Compose 文件在 `backend/` 目录，与 Go 代码同级

**docker-compose.yml 概要**:
```yaml
services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: scaffold
      POSTGRES_PASSWORD: scaffold_dev
      POSTGRES_DB: scaffold_dev
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
```

**环境变量说明**（通过 `.env` 文件，不提交仓库）:
- `DATABASE_URL=postgres://scaffold:scaffold_dev@localhost:5432/scaffold_dev?sslmode=disable`
- 测试环境使用 testcontainers-go，不使用此连接

**Alternatives considered**:
- 要求开发者自行安装 PostgreSQL: 增加启动步骤，降低"10分钟就绪"成功率
- 内嵌进程内 PG (embedded-postgres): 需要平台特定二进制，增加复杂度

---

## 决策汇总

| # | 决策域 | 选择 | 核心原因 |
|---|--------|------|----------|
| 1 | 语言版本 | Go 1.23 | 最新稳定，slog 增强，标准库改进 |
| 2 | HTTP 框架 | Gin 1.10 | 宪章要求，轻量高性能 |
| 3 | PG 驱动 | pgx v5 | sqlc 推荐，原生协议，类型安全 |
| 4 | SQL 代码生成 | sqlc 1.28 | 宪章要求，SQL 即契约 |
| 5 | 迁移工具 | golang-migrate 4.18 | 嵌入式，事务支持，dirty 检测 |
| 6 | 日志 | log/slog | 标准库，零依赖，结构化 |
| 7 | 配置 | Viper 1.19 | 多源优先级，环境变量优先 |
| 8 | 测试 | testing + testify + testcontainers-go | 单元/集成/契约三层边界 |
| 9 | 目录结构 | cmd/ + internal/ | Go 社区标准，易扩展 |
| 10 | 优雅停止 | signal + Shutdown + 10s 超时 | 标准库足够，无额外依赖 |
| 11 | 健康检查 | /health/live + /health/ready | 存活/就绪分离，K8s 对齐 |
| 12 | 自动迁移 | golang-migrate embed + 启动时 Up() | 满足 FR-013，零运维步骤 |
| 13 | 开发环境 | Docker Compose PG17 | 一键就绪，版本一致 |
