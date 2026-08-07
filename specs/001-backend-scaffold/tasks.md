# Tasks: 最小化后端脚手架

**Input**: Design documents from `/specs/001-backend-scaffold/`

**Prerequisites**: plan.md (required), spec.md (required), research.md (required), data-model.md, contracts/, quickstart.md

**Tests**: Included — per FR-012 (automated baseline checks), SC-002 (100% pass rate), and Constitution V.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions

All paths relative to `backend/` directory at repository root (`/home/hdw/vue-element-plus-admin/backend/`).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Initialize Go module, directory structure, and development environment

- [x] T001 Create backend directory structure per plan.md: `cmd/server/`, `internal/{config,database,health,middleware,server,logging}/`, `db/{migrations,queries}/`
- [x] T002 Initialize Go module with `go mod init` in `backend/go.mod`, set Go 1.23, add core dependencies (gin, pgx/v5, viper, golang-migrate, testify, testcontainers-go)
- [x] T003 [P] Create `backend/docker-compose.yml` with PostgreSQL 17 Alpine, user `scaffold`, database `scaffold_dev`, port 5432, named volume
- [x] T004 [P] Create `backend/.env.example` with all documented environment variables and safe placeholder values (no real secrets)
- [x] T005 [P] Create `backend/sqlc.yaml` with PostgreSQL engine pointing to `db/queries/`, output to `internal/database/sqlc/`
- [x] T006 [P] Create `backend/Makefile` with targets: `build`, `run`, `test`, `test-integration`, `test-unit`, `lint`, `clean`, `migrate-up`, `migrate-down`, `migrate-create`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that ALL user stories depend on — config, logging, database, middleware

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [x] T007 Implement `backend/internal/config/config.go`: Config struct loading via viper from env vars, with defaults and validation for ServerConfig, DatabaseConfig, LogConfig per data-model.md
- [x] T008 [P] Implement config tests in `backend/internal/config/config_test.go`: verify required config missing → error containing key name but NOT value, invalid port → error, valid config loads successfully, env var binding
- [x] T009 [P] Implement `backend/internal/logging/logger.go`: slog setup function returning configured `*slog.Logger` with level and format (text/json) from LogConfig, optionally wrapped for Gin
- [x] T010 [P] Implement logging tests in `backend/internal/logging/logger_test.go`: verify level filtering (debug suppressed at info), text vs JSON format output, slog attributes in structured output
- [x] T011 [P] Implement `backend/internal/middleware/request_id.go`: Gin middleware that injects X-Request-Id header (UUID v4) into request context and response, reads from incoming header if present
- [x] T012 [P] Implement middleware tests in `backend/internal/middleware/request_id_test.go`: verify header injected when missing, header preserved when present, UUID format valid, X-Response-Time header present
- [x] T013 Create initial migration `backend/db/migrations/000001_init.up.sql` with a minimal validation table or comment (scaffold has no business tables per FR-013); create matching `000001_init.down.sql`
- [x] T014 Implement `backend/internal/database/migrate.go`: embed migrations via `embed.FS`, run `migrate.Up()` on startup, handle `migrate.ErrNoChange`, return errors with filename only (no SQL content)
- [x] T015 [P] Implement `backend/internal/database/db.go`: create pgxpool connection from DATABASE_URL, expose `Ping()` for health checks, `Close()` for graceful shutdown, configure pool settings from DatabaseConfig
- [x] T016 Implement database migration tests in `backend/internal/database/db_test.go`: use testcontainers-go to spin up PostgreSQL, verify migrations apply cleanly, verify `Ping()` succeeds, verify `Close()` releases connections, verify dirty state detection

**Checkpoint**: Foundation ready — config loads, logs work, middleware injects IDs, database connects and migrates. User story implementation can now begin.

---

## Phase 3: User Story 1 — 启动可运行的后端服务 (Priority: P1) 🎯 MVP

**Goal**: 开发者执行启动步骤后，服务成功启动、自动执行迁移并报告就绪，可通过健康检查端点验证服务状态。

**Independent Test**: 在干净环境中启动 PostgreSQL、设置 DATABASE_URL、编译并运行服务；GET /health/live 返回 200，GET /health/ready 返回 200 且 checks.database="ok"。

### Tests for User Story 1 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T017 [P] [US1] Contract test for GET /health/live in `backend/internal/health/handler_test.go`: verify 200 status, `{"status":"ok","timestamp":"..."}` structure, no checks field in live response, X-Request-Id header present, 100 rapid calls all 200
- [x] T018 [P] [US1] Contract test for GET /health/ready in `backend/internal/health/handler_test.go`: verify 200 with `checks.database="ok"` when DB available, verify 503 with `checks.database="unavailable"` when DB stopped, verify response format matches contracts/health-api.md, verify no secrets in response body

### Implementation for User Story 1

- [x] T019 [US1] Implement `backend/internal/health/handler.go`: Gin handler for GET /health/live (always 200, no dependency checks) and GET /health/ready (ping DB then respond 200/503 with checks map), response structs per data-model.md HealthResponse
- [x] T020 [US1] Implement `backend/internal/server/server.go`: assemble Gin engine with RequestID middleware, register /health/live and /health/ready routes, register NotFound handler returning JSON `{"error":{"code":"NOT_FOUND","message":"..."}}`, create `*http.Server` configured from ServerConfig, wire dependencies (config, logger, db pool)
- [x] T021 [US1] Implement `backend/cmd/server/main.go`: load config → init logger → connect DB → run migrations → create server → start HTTP listener → log "server ready" → wait for shutdown signal, orchestrate in correct dependency order
- [x] T022 [US1] Implement server integration tests in `backend/internal/server/server_test.go`: use testcontainers-go PostgreSQL, start full server on random port, verify /health/live returns 200, verify /health/ready returns 200, verify /nonexistent returns 404 with JSON error, verify X-Request-Id in all responses, stop server and verify clean shutdown
- [x] T023 [US1] Create `backend/README.md` with prerequisites, setup steps (docker compose up, env vars, go run), build and test commands, API reference links to quickstart.md — follow spec SC-001 target (10 min from reading to running)

**Checkpoint**: 服务可启动，数据库迁移自动执行，存活和就绪端点正常响应。US1 独立可验证。

---

## Phase 4: User Story 2 — 可获得诊断的启动失败反馈 (Priority: P2)

**Goal**: 缺失配置或依赖不可用时，服务安全拒绝就绪并提供可操作但不泄密的错误信息。

**Independent Test**: 移除 DATABASE_URL 启动 → 快速失败并报告配置项名称（不含值）；停止数据库后检查 /health/ready → 503 且 database="unavailable"；恢复数据库 → 自动转为 200。

### Tests for User Story 2 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T024 [P] [US2] Config failure integration test in `backend/internal/config/config_test.go`: start service binary (or test main) without DATABASE_URL, assert exit code ≠ 0, assert stderr contains "DATABASE_URL" key name, assert stderr does NOT contain "postgres://" or any password-like string, assert failure within 5s
- [x] T025 [P] [US2] Database unavailable test in `backend/internal/server/server_test.go`: start full server with PostgreSQL, verify ready, stop PostgreSQL container, verify /health/live still 200, verify /health/ready → 503 with database="unavailable", restart PostgreSQL, wait up to 30s, verify /health/ready → 200 again (auto-recovery)
- [x] T026 [P] [US2] Migration failure test in `backend/internal/database/db_test.go`: create intentionally invalid migration SQL in a test-specific migrations dir, verify migration.Up() returns error, verify error message does not contain SQL body, verify process exits rather than retrying
- [x] T027 [P] [US2] Secret leak prevention test in `backend/internal/logging/logger_test.go`: verify that database URL/password strings are NOT present in log output when config fails, verify error response bodies contain no stack traces, no internal paths, no connection strings

### Implementation for User Story 2

- [x] T028 [US2] Enhance config validation in `backend/internal/config/config.go`: add strict validation — DATABASE_URL must be non-empty and parse as valid PostgreSQL DSN (basic format check), SERVER_PORT 1–65535, DATABASE_MAX_CONNS 1–100, LOG_LEVEL enum — each failure reports key name + issue type only, never the value
- [x] T029 [US2] Enhance startup error handling in `backend/cmd/server/main.go`: on config load failure → log structured error with key name only → os.Exit(1), on DB connect failure → log error type (not connection string) → os.Exit(1), on migration failure → log migration file name + error type → os.Exit(1), ALL errors use slog.Error, never fmt.Printf or panic
- [x] T030 [US2] Implement database re-check in `backend/internal/health/handler.go`: readiness handler performs `db.Ping()` on each request (with timeout), on failure returns 503 with database="unavailable", on recovery returns 200 with database="ok"; log only state transitions (ok→unavailable and unavailable→ok) at WARN/INFO level
- [x] T031 [US2] Add unsafe error detection linter: configure `go vet` and/or `staticcheck` to catch fmt.Printf, panic, os.Exit in non-main packages; add to Makefile `lint` target

**Checkpoint**: 所有错误场景产生可操作的诊断信息且零秘密泄露。US1 + US2 均独立可验证。

---

## Phase 5: User Story 3 — 验证并安全停止基线服务 (Priority: P3)

**Goal**: 自动化基线检查全部通过，服务可优雅停止并释放资源。

**Independent Test**: 执行 `go test ./...` → 全部通过；发送 SIGTERM → 服务停止接收新请求、等待活跃请求完成、10秒内退出。

### Tests for User Story 3 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T032 [P] [US3] Graceful shutdown test in `backend/internal/server/server_test.go`: start server, fire long-running request (e.g., /health/ready with artificial 3s delay), send shutdown signal, verify: (a) new requests are rejected after shutdown initiated, (b) in-flight request completes, (c) server exits in < 10s, (d) no goroutine leaks (use runtime.NumGoroutine before/after)
- [x] T033 [P] [US3] NotFound and method test in `backend/internal/server/server_test.go`: GET /nonexistent → 404 with JSON error body {"error":{"code":"NOT_FOUND",...}}, POST /health/live → 404 or 405 with consistent JSON error, verify no stack traces or internal paths in any error response, verify error format matches contracts/health-api.md
- [x] T034 [P] [US3] Quickstart validation: execute quickstart.md steps 1–10 in a fresh environment (Docker Compose up, set env vars, go build, ./bin/server, curl endpoints, docker compose stop postgres, verify 503/200 transitions, Ctrl+C graceful shutdown), all steps must complete successfully

### Implementation for User Story 3

- [x] T035 [US3] Implement graceful shutdown in `backend/cmd/server/main.go`: use `signal.NotifyContext` for SIGINT/SIGTERM, create shutdown context with 10s timeout, call `srv.Shutdown()` to stop accepting requests, wait for active requests to drain, call `db.Close()`, log "server stopped" on success or "shutdown timeout" on force exit
- [x] T036 [US3] Implement consistent error response in `backend/internal/server/server.go`: add Gin NoRoute handler returning 404 JSON `{"error":{"code":"NOT_FOUND","message":"The requested path was not found"}}`, add Gin NoMethod handler returning 405 JSON `{"error":{"code":"METHOD_NOT_ALLOWED","message":"..."}}`, ensure all error responses use same top-level `error.code` + `error.message` structure with X-Request-Id header
- [x] T037 [US3] Implement startup/shutdown logging in `backend/cmd/server/main.go`: log "server starting" with addr at startup, "migration completed" with file count, "database connected" with pool config, "server ready" on listen success; on shutdown log "shutting down" with signal name, "server stopped" with elapsed time
- [x] T038 [US3] Run all tests and confirm 100% pass: `go test ./... -v -count=1` must show all packages PASS, `go test ./... -cover` must show reasonable coverage (target >70% for internal packages), no test flakiness on 3 consecutive runs

**Checkpoint**: 全部自动化检查通过，优雅停止工作正常，错误响应一致。全部三个用户故事独立可验证。

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Final quality checks, documentation alignment, and edge case hardening

- [x] T039 [P] Verify quickstart.md instructions match actual implementation: run each command in quickstart.md sequentially, confirm expected outputs match actual outputs, fix any discrepancies in either quickstart.md or implementation
- [x] T040 [P] Hardening: verify edge cases from spec.md — port conflict → fast failure with clear message, blank/whitespace config values treated as missing, high-frequency health checks produce no disk I/O/DB writes/log spam, unknown paths with various HTTP methods all return safe JSON errors
- [x] T041 [P] Verify constitution compliance: re-run constitution check all 5 gates, confirm migrations are embedded and run automatically, confirm no ORM imports, confirm no business domain models created, confirm sglc type-safe code generation configured, confirm structured logging with request ID correlation
- [x] T042 Verify `.env` file is in `backend/.gitignore` and `.env.example` contains no real values, confirm `go.sum` is tracked, confirm no generated sqlc files are hand-modified
- [x] T043 Run `golangci-lint run ./...` (if configured) or `go vet ./...` — resolve all warnings

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion (T001–T006) — BLOCKS all user stories
- **User Story 1 (Phase 3)**: Depends on Foundational (T007–T016) — No dependencies on other stories
- **User Story 2 (Phase 4)**: Depends on Foundational + US1 (enhances US1's config, error handling, health handlers)
- **User Story 3 (Phase 5)**: Depends on Foundational + US1 (adds graceful shutdown, error responses on top of US1 server)
- **Polish (Phase 6)**: Depends on all user stories complete

### User Story Dependencies

- **User Story 1 (P1)**: Can start after Foundational (Phase 2) — Stories are filesystem-level independent but US2 and US3 enhance US1's server, config, and health packages
- **User Story 2 (P2)**: Extends config.go, main.go, handler.go from US1 — should be implemented AFTER US1 to avoid merge conflicts
- **User Story 3 (P3)**: Extends main.go, server.go from US1 — should be implemented AFTER US1; independent of US2

### Within Each User Story

- Tests MUST be written and FAIL before implementation
- Models/config before handlers
- Handlers before server assembly
- Server assembly before main.go integration
- Core implementation before error path hardening
- Story complete before moving to next priority

### Parallel Opportunities

- T003, T004, T005, T006 (Setup) all touch different files — fully parallelizable
- T008, T010, T012 (Foundational tests) all touch different files — fully parallelizable
- T017, T018 (US1 tests) touch same file but different test functions — sequential within file
- T024, T025, T026, T027 (US2 tests) touch different test files — fully parallelizable
- T032, T033 (US3 tests) touch same test file — sequential within file
- T039, T040, T041 (Polish) touch different concerns — fully parallelizable

---

## Parallel Example: Phase 2 Foundational

```bash
# Launch all independent foundational tasks together:
Task: "T007 Implement config in backend/internal/config/config.go"
Task: "T009 Implement logging in backend/internal/logging/logger.go"  (after T007 for struct)
Task: "T011 Implement middleware in backend/internal/middleware/request_id.go"
Task: "T013 Create initial migration in backend/db/migrations/000001_init.up.sql"

# Then launch their tests in parallel (depends on respective implementations):
Task: "T008 Config tests in backend/internal/config/config_test.go"
Task: "T010 Logging tests in backend/internal/logging/logger_test.go"
Task: "T012 Middleware tests in backend/internal/middleware/request_id_test.go"

# Database layer (depends on T013 migration file):
Task: "T014 Implement migration runner in backend/internal/database/migrate.go"
Task: "T015 Implement DB connection in backend/internal/database/db.go"
Task: "T016 DB/migration tests in backend/internal/database/db_test.go"
```

---

## Parallel Example: User Story 1

```bash
# Launch all tests for User Story 1 together:
Task: "T017 Contract test for health/live in backend/internal/health/handler_test.go"
Task: "T018 Contract test for health/ready in backend/internal/health/handler_test.go"

# After tests fail, implement:
Task: "T019 Health handlers in backend/internal/health/handler.go"
Task: "T020 Server assembly in backend/internal/server/server.go"
Task: "T021 Main entry point in backend/cmd/server/main.go"
Task: "T022 Server integration tests in backend/internal/server/server_test.go"
Task: "T023 README in backend/README.md"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T006)
2. Complete Phase 2: Foundational (T007–T016) — CRITICAL: blocks all stories
3. Complete Phase 3: User Story 1 (T017–T023)
4. **STOP and VALIDATE**: `go build ./cmd/server/` + `./bin/server` + `curl localhost:8080/health/live` must return 200
5. Deploy/demo if ready — MVP delivers runnable server with health checks

### Incremental Delivery

1. Complete Setup + Foundational → Foundation ready (config, logging, DB, middleware)
2. Add User Story 1 → Test independently → Server starts, health checks work (MVP!)
3. Add User Story 2 → Test independently → Diagnostic error messages, DB recovery
4. Add User Story 3 → Test independently → Graceful shutdown, full test suite passes
5. Each story adds value without breaking previous stories

### Parallel Team Strategy

With multiple developers:

1. Team completes Setup + Foundational together (T001–T016)
2. Once Foundational is done:
   - Developer A: US1 implementation (T019–T023) while Developer B writes US1 tests (T017–T018)
   - After US1 complete: Developer A takes US2, Developer B takes US3
3. Stories enhance different aspects of the same codebase — coordinate on shared files (main.go, server.go, handler.go)

---

## Notes

- [P] tasks = different files, no dependencies — can run truly in parallel
- [Story] label maps task to specific user story for traceability
- Each user story should be independently completable and testable
- Verify tests fail before implementing (TDD where tests are listed first)
- Commit after each task or logical group
- Stop at any checkpoint to validate story independently
- Avoid: vague tasks, same file conflicts within a phase, cross-story dependencies that break independence
- Shared files (config.go, main.go, server.go, handler.go) are extended across US1→US2→US3 — implement the P1 baseline first, then enhance in P2/P3
