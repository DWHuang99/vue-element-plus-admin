# Implementation Plan: 最小化后端脚手架

**Branch**: `001-backend-scaffold` | **Date**: 2026-08-04 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-backend-scaffold/spec.md`

**Note**: This template is filled in by the `/speckit-plan` command; its definition describes the execution workflow.

## Summary

提供一个基于 Go + Gin + PostgreSQL + sqlc 的最小化后端服务基线。服务支持配置校验、健康检查
（存活/就绪）、自动数据库迁移、结构化日志、请求 ID 关联、优雅停止以及全面的自动化基线测试。
脚手架为后续业务功能保留明确的接入边界，不含任何用户认证、注册或业务领域能力。

**技术决策核心**:
- Go 1.23 + Gin 1.10：高性能、低依赖、与项目宪章对齐
- PostgreSQL 17 + sqlc 1.28：契约化数据访问，SQL 为单一事实来源
- golang-migrate 4.18：嵌入式迁移，与 sqlc 互补
- log/slog：标准库结构化日志，零外部依赖
- viper 1.19：成熟的环境变量配置方案
- testing + testify：标准库测试 + 断言辅助
- testcontainers-go：PostgreSQL 集成测试的隔离环境

## Technical Context

**Language/Version**: Go 1.23

**Primary Dependencies**:
- `github.com/gin-gonic/gin` v1.10 — HTTP 框架
- `github.com/jackc/pgx/v5` v5.7 — PostgreSQL 驱动
- `github.com/sqlc-dev/sqlc` v1.28 — 类型安全 SQL 代码生成
- `github.com/golang-migrate/migrate/v4` v4.18 — 数据库迁移
- `github.com/spf13/viper` v1.19 — 配置管理
- `log/slog` (标准库) — 结构化日志
- `github.com/stretchr/testify` v1.10 — 测试断言
- `github.com/testcontainers/testcontainers-go` v0.34 — 集成测试 PostgreSQL 实例

**Storage**: PostgreSQL 17（开发期可通过 Docker Compose 提供本地实例）

**Testing**: `go test` + testify 断言 + testcontainers-go（PostgreSQL 集成测试使用临时容器）

**Target Platform**: Linux 服务器（开发期支持 Linux/macOS/Windows with WSL2）

**Project Type**: Web 服务（后端 API）

**Performance Goals**:
- 健康检查端点 P95 < 50ms
- 启动完成（含迁移） < 5 秒
- 优雅停止 < 10 秒

**Constraints**:
- 内存占用 < 50MB（空闲时）
- 配置缺失时 5 秒内快速失败
- 零秘密泄露（日志、错误响应）
- 不创建业务领域数据模型

**Scale/Scope**:
- 单进程服务
- 单个 PostgreSQL 数据库连接池（max 25 connections）
- 约 15–25 个源文件（cmd/、internal/ 结构）

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| # | 原则 | 合规状态 | 说明 |
|---|------|----------|------|
| I | **身份安全优先** | ✅ NOT APPLICABLE | 本功能明确不包含登录、注册、认证等功能（FR-015），无身份数据需要保护。安全基线（速率限制、账户枚举防护等）留待身份功能规格时启用。脚手架本身不引入安全风险。 |
| II | **分阶段交付与边界控制** | ✅ PASS | 本功能是身份三个阶段之前的"第零阶段"，形成可独立运行、测试和部署的最小闭环。不使用未来阶段的抽象（无用户模型、无认证中间件、无邮件/SSO 依赖）。后续业务能力通过兼容扩展接入。 |
| III | **PostgreSQL 与 sqlc 契约化数据访问** | ✅ PASS | 采用 sqlc 生成类型安全代码，迁移由 golang-migrate 管理，SQL 文件作为可审查契约。迁移在启动时自动执行，与对应查询在同一变更中提交。生成文件不手工修改。本阶段仅创建 schema_migrations 追踪表（脚手架需要验证迁移能力）。 |
| IV | **API 契约与前后端解耦** | ✅ PASS | 定义明确的健康检查端点模型（存活/就绪），使用独立的请求/响应结构体，不暴露数据库行结构。所有端点定义状态码、认证要求（本阶段无）、字段校验和机器可读错误码。 |
| V | **可验证、可观测与隐私保护** | ✅ PASS | 每项行为变更配套自动化测试：配置校验有单元测试，迁移执行和健康检查有 PostgreSQL 集成测试，API 端点有请求/响应测试。使用 slog 结构化日志 + 请求 ID 关联。日志不包含密码、令牌或敏感配置值。安全事件（启动失败、迁移失败、存储连接变化）有可审计记录。 |

**Gate Result**: ✅ ALL GATES PASS — no violations to justify.

## Project Structure

### Documentation (this feature)

```text
specs/001-backend-scaffold/
├── plan.md              # This file (/speckit-plan command output)
├── research.md          # Phase 0 output (/speckit-plan command)
├── data-model.md        # Phase 1 output (/speckit-plan command)
├── quickstart.md        # Phase 1 output (/speckit-plan command)
├── contracts/           # Phase 1 output (/speckit-plan command)
│   └── health-api.md    # Health check API contract
└── tasks.md             # Phase 2 output (/speckit-tasks command - NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
backend/
├── cmd/
│   └── server/
│       └── main.go              # 入口：配置加载、依赖组装、启动/停止
├── internal/
│   ├── config/
│   │   ├── config.go            # 配置结构体与加载（viper）
│   │   └── config_test.go       # 配置加载与校验测试
│   ├── database/
│   │   ├── db.go                # 连接池创建、健康检查
│   │   ├── db_test.go           # 数据库连接与迁移测试
│   │   └── migrate.go           # 自动迁移（golang-migrate）
│   ├── health/
│   │   ├── handler.go           # 存活/就绪 HTTP 处理器
│   │   └── handler_test.go      # 健康检查端点测试
│   ├── middleware/
│   │   ├── request_id.go        # 请求 ID 中间件
│   │   └── request_id_test.go   # 请求 ID 测试
│   ├── server/
│   │   ├── server.go            # HTTP 服务器组装、启动、优雅停止
│   │   └── server_test.go       # 服务器集成测试
│   └── logging/
│       ├── logger.go            # slog 初始化与配置
│       └── logger_test.go       # 日志行为测试
├── db/
│   ├── migrations/              # golang-migrate SQL 迁移文件
│   │   └── 000001_init.up.sql   # 初始迁移（schema_migrations 追踪）
│   └── queries/                 # sqlc 查询 SQL 文件
│       └── health.sql           # 健康检查相关查询（如需要）
├── sqlc.yaml                    # sqlc 配置文件
├── go.mod
├── go.sum
├── Makefile                     # 开发任务：构建、测试、运行、迁移
└── README.md                    # 项目启动说明
```

**Structure Decision**: 采用 Go 社区标准的 `cmd/` + `internal/` 布局。`internal/` 下按职责分
子包（config、database、health、middleware、server、logging），每个子包自带测试文件。SQL 迁移
和查询文件独立于 Go 代码，由 golang-migrate 和 sqlc 分别管理。此结构为后续业务模块（auth、
user 等）预留了清晰目录边界——每个业务模块在 `internal/` 下新增子包即可，无需修改启动和基础设施层。

## Complexity Tracking

> **No violations** — all Constitution Check gates passed. No complexity justifications required.
