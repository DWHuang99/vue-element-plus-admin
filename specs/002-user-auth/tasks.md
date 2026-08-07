# Tasks: 注册功能第一阶段（用户认证）

**Input**: Design documents from `/specs/002-user-auth/`

**Prerequisites**: plan.md (required), spec.md (required), research.md, data-model.md, contracts/, quickstart.md

**Tests**: Included — per Constitution V (每项行为变更 MUST 配套风险相称的自动化测试) and SC-006/SC-007/SC-008.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions

- Backend paths relative to `backend/` (`/home/hdw/vue-element-plus-admin/backend/`)
- Frontend paths relative to `frontend/` (`/home/hdw/vue-element-plus-admin/frontend/`)
- `sqlc` generated package: `backend/internal/database/sqlc/` (do not hand-edit — regenerate via `make sqlc-generate`)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add new dependency and configuration hooks needed by all stories

- [x] T001 Add `golang.org/x/crypto` dependency to `backend/go.mod` (run `go get golang.org/x/crypto@latest` and `go mod tidy` in backend/)
- [x] T002 [P] Add rate limit configuration to `backend/internal/config/config.go`: `RATE_LIMIT_ENABLED` (default true), `RATE_LIMIT_REGISTER_IP_HOUR`, `RATE_LIMIT_LOGIN_IP_15MIN`, `RATE_LIMIT_LOGIN_USER_15MIN` with validation; extend `Config` struct and `.env.example`
- [x] T003 [P] Add config tests for new rate limit fields in `backend/internal/config/config_test.go`: defaults, invalid values rejected, valid values load

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Migrations, sqlc queries, and core security primitives that ALL user stories depend on

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [x] T004 Create migration `backend/db/migrations/000002_users.up.sql` and `000002_users.down.sql`: `users` table per data-model.md (id BIGSERIAL PK, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL, created_at/updated_at TIMESTAMPTZ NOT NULL DEFAULT now()), index on username; down drops table
- [x] T005 Create migration `backend/db/migrations/000003_sessions.up.sql` and `000003_sessions.down.sql`: `sessions` table per data-model.md (id BIGSERIAL PK, token_hash TEXT UNIQUE NOT NULL, user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE, created_at/expires_at TIMESTAMPTZ NOT NULL, revoked_at TIMESTAMPTZ NULL, last_used_at TIMESTAMPTZ NOT NULL DEFAULT now()); indexes on user_id and token_hash
- [x] T006 Create `backend/db/queries/users.sql` with sqlc queries: `CreateUser` (returns id, created_at), `GetUserByUsername`, `GetUserByID`; update `backend/db/queries/health.sql` if needed for sqlc package consistency
- [x] T007 Create `backend/db/queries/sessions.sql` with sqlc queries: `CreateSession`, `GetSessionByTokenHash` (JOIN users to return user_id, username, expires_at, revoked_at, last_used_at), `RevokeSessionByTokenHash`, `TouchSession`
- [x] T008 Run `make sqlc-generate` in backend/ to generate `backend/internal/database/sqlc/` code; verify no compile errors (do NOT hand-edit generated files)
- [x] T009 Implement `backend/internal/auth/password.go`: Argon2id hashing (params t=3, m=64MB, p=4, salt 16B crypto/rand, output 32B), PHC encoding `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>`, `HashPassword(password string) (string, error)`, `VerifyPassword(hash, password string) (bool, error)` that parses PHC string and is constant-time on comparison
- [x] T010 [P] Implement password unit tests in `backend/internal/auth/password_test.go`: hash/verify round-trip, wrong password rejected, unique salt per hash (two hashes of same password differ), PHC format prefix check, malformed hash rejected
- [x] T011 Implement `backend/internal/auth/token.go`: `GenerateToken() (string, error)` using crypto/rand 32 bytes → base64url; `HashToken(token string) string` → SHA-256 hex (64 chars); helpers for expiry computation (24h absolute, slide-renew threshold 7d)
- [x] T012 [P] Implement token unit tests in `backend/internal/auth/token_test.go`: token uniqueness, token length, hash is 64-char hex, hash is deterministic, hash differs from raw token
- [x] T013 Implement `backend/internal/ratelimit/ratelimit.go`: in-memory sliding window limiter with key-based buckets, `Allow(key string) bool` + `RetryAfter(key string) time.Duration`, mutex-protected, periodic cleanup of stale buckets; config to enable/disable
- [x] T014 [P] Implement ratelimit unit tests in `backend/internal/ratelimit/ratelimit_test.go`: within-limit allows, over-limit blocks, retry-after returned, independent keys, disabled mode always allows, cleanup removes stale keys
- [x] T015 Implement `backend/internal/auth/service.go` core: Service struct holding sqlc Querier + pgx pool tx access; `Register(ctx, username, password) (*AuthResult, error)`, `Login(ctx, username, password) (*AuthResult, error)`, `Logout(ctx, tokenHash string) error`, `Authenticate(ctx, tokenHash string) (*AuthUser, error)` — business logic per data-model.md (transaction for register: CreateUser + CreateSession atomically). **错误分类必须使用以下单一映射（`internal/auth/errors.go` 定义哨兵错误）**:
  | 哨兵错误 | 契约错误码 | HTTP |
  |----------|-----------|------|
  | `ErrUsernameTaken` | `AUTH_USERNAME_TAKEN` | 409 |
  | `ErrInvalidCredentials` | `AUTH_INVALID_CREDENTIALS` | 401 |
  | `ErrInvalidToken` | `AUTH_INVALID_TOKEN` | 401 |
  | `ErrInvalidInput` | `AUTH_INVALID_INPUT` | 400 |
  | `ErrRateLimited` | `RATE_LIMITED` | 429 |
  服务层只返回哨兵错误，HTTP 层负责映射；禁止在服务层返回 HTTP 状态码或拼接错误消息。

**Checkpoint**: Foundation ready — migrations apply, sqlc code generates, password/token/ratelimit primitives tested. User story implementation can begin.

---

## Phase 3: User Story 1 — 注册新账户并进入系统 (Priority: P1) 🎯 MVP

**Goal**: 用户用用户名+密码注册，注册即登录，前端注册页接真实后端。

**Independent Test**: 全新环境注册新用户名 → 201 + 令牌，携带令牌访问 /auth/me 返回该用户；前端注册表单提交后直接进入系统。

### Tests for User Story 1 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T016 [P] [US1] Contract test for POST /api/v1/auth/register in `backend/internal/auth/handler_test.go`: 201 + data.token 非空 + data.user.username; 重复用户名 → 409 AUTH_USERNAME_TAKEN; username `ab`/密码 7 字符 → 400 AUTH_INVALID_INPUT with field_errors; whitespace/blank username → 400; response contains no secrets
- [x] T017 [P] [US1] Integration test for register flow in `backend/internal/auth/service_test.go`: use testcontainers-go PostgreSQL, full migrations, Register creates user + session in one transaction (verify both rows), duplicate username → error, verify password_hash stored as `$argon2id$` not plaintext, verify token_hash is 64-char hex not raw token

### Implementation for User Story 1

- [x] T018 [US1] Implement `backend/internal/auth/handler.go` register handler: parse/validate request (username 3–32 `^[a-zA-Z0-9_]+$`, password 8–72 bytes), call service.Register, map success → 201 `{"data":{token, token_type:"Bearer", expires_in, user}}`, map errors → contracts/auth-api.md error codes
- [x] T019 [US1] Wire register route: in `backend/cmd/server/main.go`, mount rate limit middleware on register route and register `POST /api/v1/auth/register` on the Gin router with `/api/v1` group. **限流 IP 来源**: 使用 `c.ClientIP()`（单实例直连部署；若后续引入反向代理，需在 `frontend/nginx` 或 Gin 的 `trusted proxies` 配置中显式声明可信代理，再依赖 `X-Forwarded-For`）
- [x] T020 [US1] Frontend: add `frontend/.env.development` with `VITE_API_BASE_URL=http://localhost:8080/api/v1`; set axios baseURL from it in `frontend/src/axios/config.ts`
- [x] T021 [US1] Frontend: update `frontend/src/api/login/index.ts` — replace `loginApi` mock call with real `POST /auth/register` and `POST /auth/login`; update `frontend/src/api/login/types.ts` with `AuthResult`/`UserType` types matching contracts/auth-api.md
- [x] T022 [US1] Frontend: in `frontend/src/api/request/index.ts`, add axios request interceptor attaching `Authorization: Bearer <token>` from user store; store token in `frontend/src/store/modules/user.ts` (add `register`/`login` actions calling real API, persist token to localStorage)
- [x] T023 [US1] Frontend: wire `frontend/src/views/Login/components/RegisterForm.vue` to call user store register action, on success route to home; verify Login.vue register/login toggle works

**Checkpoint**: 后端注册可用（201/409/400），前端注册即进入系统。US1 独立可验证。

---

## Phase 4: User Story 2 — 登录已有账户 (Priority: P2)

**Goal**: 用户用用户名+密码登录，登录失败统一响应防枚举，前端登录页接真实后端。

**Independent Test**: 已注册账户正确凭据登录 → 200 + 令牌；错误密码与不存在用户名 → 均为 401 且响应一致。

### Tests for User Story 2 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T024 [P] [US2] Contract test for POST /api/v1/auth/login in `backend/internal/auth/handler_test.go`: correct credentials → 200 + token; wrong password → 401 AUTH_INVALID_CREDENTIALS; nonexistent username → 401 same code and identical body shape (compare JSON); field validation → 400; no secrets in response
- [x] T025 [P] [US2] Integration test for login flow in `backend/internal/auth/service_test.go`: testcontainers PostgreSQL, Login with correct credentials creates session row; wrong password returns error with AUTH_INVALID_CREDENTIALS code; nonexistent user returns same error code. **SC-003 端到端断言**: 对同一直调 (a) 错误密码与不存在用户名两次 401 响应体**逐字节一致**（防枚举），(b) 连续错误尝试超过阈值后返回 429 RATE_LIMITED 且含 Retry-After 头 —— 两个维度在同一测试中闭环验证
- [x] T026 [P] [US2] Rate limit contract test in `backend/internal/ratelimit/ratelimit_test.go` + integration: after N failed logins exceed limit → 429 RATE_LIMITED with Retry-After header; register over limit → 429

### Implementation for User Story 2

- [x] T027 [US2] Implement `backend/internal/auth/handler.go` login handler: parse/validate, call service.Login, success → 200 `{"data":{token, token_type, expires_in, user}}`, failure → 401 AUTH_INVALID_CREDENTIALS (uniform); mount rate limit middleware on login route (IP + username dimensions). **限流双维度**: IP 键用 `c.ClientIP()`；用户名键从请求体 `username` 提取（先校验字段再限流，避免空用户名污染限流桶）；用户名维度仅在字段校验通过后累加
- [x] T028 [US2] Frontend: update `frontend/src/store/modules/user.ts` login action to call real `POST /auth/login`, store token + user info, handle 401 with generic error message (no account-existence hint); update `frontend/src/views/Login/components/LoginForm.vue` to call it and redirect on success

**Checkpoint**: 登录可用，失败统一响应，前端登录接真实后端。US1 + US2 独立可验证。

---

## Phase 5: User Story 3 — 退出并维护安全会话 (Priority: P3)

**Goal**: 用户可退出（会话立即失效），受保护端点拒绝无效/过期/已撤销令牌，前端退出与会话失效处理接真实后端。

**Independent Test**: 登录 → 携带令牌访问 /auth/me 成功 → 退出 → 原令牌访问 /auth/me 返回 401；伪造/过期令牌均 401。

### Tests for User Story 3 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [x] T029 [P] [US3] Contract tests for logout and me in `backend/internal/auth/handler_test.go`: POST /auth/logout with valid token → 204; with invalid/missing header → 401 AUTH_INVALID_TOKEN; logout is idempotent (reusing revoked token → 204); GET /auth/me with valid token → 200 data.user; with invalid/expired/revoked token → 401 AUTH_INVALID_TOKEN
- [x] T030 [P] [US3] Auth middleware tests in `backend/internal/middleware/auth_test.go`: valid Bearer token → user injected into context; missing header → 401; malformed header → 401; revoked token → 401; expired token → 401; response has no secrets/stack traces
- [x] T031 [P] [US3] Integration test for session lifecycle in `backend/internal/auth/service_test.go`: register → Authenticate ok → Logout revokes → Authenticate fails; expired session (set expires_at in past) → Authenticate fails; sliding renewal: session near expiry gets expires_at extended on use

### Implementation for User Story 3

- [x] T032 [US3] Implement `backend/internal/middleware/auth.go`: parse `Authorization: Bearer <token>`, hash token, call service.Authenticate, inject user_id/username into Gin context, reject with 401 AUTH_INVALID_TOKEN on any failure; expose `GetCurrentUser(c)` helper
- [x] T033 [US3] Implement logout and me handlers in `backend/internal/auth/handler.go`: logout → revoke session via service, 204 (idempotent); me → return current user from context, 200 `{"data":{"user":{id,username,created_at}}}`; wire routes in `backend/cmd/server/main.go` under `/api/v1/auth` group with auth middleware
- [x] T034 [US3] Frontend: update `frontend/src/store/modules/user.ts` logout action to call real `POST /auth/logout` and clear localStorage token; add axios response interceptor in `frontend/src/api/request/index.ts` for 401 → clear token + redirect to login page
- [x] T035 [US3] Frontend: on app initialization (e.g., in `frontend/src/store/modules/user.ts` or router guard), validate stored token via `GET /auth/me`; if valid populate user info, if invalid clear token and redirect to login

**Checkpoint**: 退出立即失效，所有认证中间件场景通过，前端退出/会话失效处理正常。全部三个用户故事独立可验证。

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Constitution compliance verification, security hardening, full validation

- [x] T036 [P] Verify constitution compliance: confirm Argon2id + per-account salt (FR-003), login uniform failure (FR-005), rate limiting (FR-006), session expiry + revocation (FR-007/008/009), secrets not in logs/errors (FR-011), audit events logged for register/login/logout/rate-limit (FR-014) — re-run all 5 constitution gates in plan.md
- [x] T037 [P] Audit logging: add structured audit log events (slog) in `backend/internal/auth/service.go` and `backend/cmd/server/main.go` for register success, login success/failure, logout, rate limit trigger, token revocation — with request_id correlation, NO passwords/tokens in log values
- [x] T038 [P] Edge case hardening per spec.md: concurrent login same account (independent sessions), logout one session doesn't affect another, high-frequency auth calls no resource accumulation, blank/whitespace fields → 400, unknown paths under /api/v1/auth → consistent 404
- [x] T039 [P] Run `go vet ./...` and `golangci-lint run ./...` (if available) in backend/ — resolve all warnings; run `go test ./... -cover` confirm >70% coverage on internal/auth, internal/ratelimit, internal/middleware
- [x] T040 Run quickstart.md end-to-end validation (backend API steps 4.1–4.8 + frontend steps 5.3) — fix any discrepancies between quickstart and actual behavior

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies
- **Foundational (Phase 2)**: Depends on Setup — BLOCKS all user stories (migrations/queries/primitives)
- **US1 (Phase 3)**: Depends on Foundational — register backend + frontend entry
- **US2 (Phase 4)**: Depends on US1 backend (service exists) — login is independent of register endpoint but shares service.go; frontend shares request layer
- **US3 (Phase 5)**: Depends on US1 (auth middleware + me validate token) — logout/me build on middleware
- **Polish (Phase 6)**: Depends on all user stories

### User Story Dependencies

- **US1 (P1)**: Register — no dependency on other stories (frontend token storage + Bearer interceptor needed for later stories)
- **US2 (P2)**: Login — shares service.go/handler.go with US1; implement after US1 to avoid same-file conflicts
- **US3 (P3)**: Logout/me — depends on auth middleware (T032) built on US1's session creation; independent of US2 logic

### Within Each User Story

- Tests MUST be written and FAIL before implementation
- Migrations → sqlc queries → password/token primitives → service → handler → route → frontend

### Parallel Opportunities

- T002, T003 (Setup) touch different files — parallel
- T009/T011 (password, token) and T013 (ratelimit) are pure primitives — parallel after T004–T008
- T016/T017 (US1 tests) different files — parallel
- T024/T025/T026 (US2 tests) different files — parallel
- T029/T030/T031 (US3 tests) different files — parallel
- T036–T039 (Polish) different concerns — parallel

---

## Parallel Example: Phase 2 Foundational

```bash
# Migrations + queries + primitives can be developed together:
Task: "T004 Create users migration"
Task: "T005 Create sessions migration"
Task: "T006 Create users.sql queries"
Task: "T007 Create sessions.sql queries"
# Then generate sqlc and build primitives on top:
Task: "T008 Run make sqlc-generate"
Task: "T009 Implement password.go (depends T004)"
Task: "T011 Implement token.go"
Task: "T013 Implement ratelimit.go"
# Tests parallel:
Task: "T010 password_test.go"
Task: "T012 token_test.go"
Task: "T014 ratelimit_test.go"
# Service on top of generated queries:
Task: "T015 service.go (depends T006-T008)"
```

---

## Parallel Example: User Story 1

```bash
# Tests first (fail), then implement:
Task: "T016 register contract tests in handler_test.go"
Task: "T017 register integration tests in service_test.go"

# Then implementation:
Task: "T018 register handler (depends service)"
Task: "T019 wire route in main.go"
Task: "T020 frontend .env.development + axios baseURL"
Task: "T021 frontend api/login/index.ts"
Task: "T022 frontend request interceptor + user store"
Task: "T023 frontend RegisterForm.vue"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 (Setup) + Phase 2 (Foundational)
2. Complete Phase 3: US1 — register backend + frontend
3. **STOP and VALIDATE**: register via curl → 201 + token → /auth/me 200; frontend register → enters system
4. MVP delivers registration with working session

### Incremental Delivery

1. Foundation ready (migrations, sqlc, password/token/ratelimit)
2. US1 register → validate independently → MVP
3. US2 login → validate independently
4. US3 logout/me/session → validate independently
5. Polish: constitution compliance + security audit

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to user story for traceability
- Verify tests fail before implementing (TDD)
- Do NOT hand-edit sqlc generated files (`internal/database/sqlc/`) — regenerate
- Secrets: never log passwords or raw tokens; store only hashes
- Register is transactional (user + session) — verify in tests
- Frontend roles/permissions/menu remain on Mock — do not wire those
