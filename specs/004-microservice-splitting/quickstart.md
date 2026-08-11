# Quickstart: 微服务拆分准备验证指南

**Feature**: `004-microservice-splitting`  
**Target mode**: single-process modular monolith

本指南用于实施和验收本 feature。它不启动独立 IAM/Organization 服务，不要求 gRPC、Redis、broker 或 Kubernetes。

## 1. Prerequisites

- Go 1.25.x
- Node.js >= 18
- pnpm 9.x（项目锁定 package manager）
- Docker 可用（testcontainers/PostgreSQL）
- sqlc 可执行版本与项目约定一致
- PostgreSQL development database URL through environment

不要把数据库 URL、密码、原始 token 或管理员凭据写入仓库/日志。

## 2. Feature context

Spec Kit 逻辑 feature：

```text
004-microservice-splitting
```

当前工作可能仍位于 Git branch `develop-wsl`；逻辑 feature 名不会自动切换 Git branch。

Artifacts：

```text
specs/004-microservice-splitting/
├── spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── baseline-implementation.patch
├── baseline-contracts.patch
└── contracts/
```

## 3. Baseline before implementation

从仓库根目录记录当前基线。现有大量未提交改动时，不要 reset、checkout 或覆盖其他工作。

Frozen compatibility point:

```text
repository HEAD: d4b1bae75b37f894879360099066ad49ab78da91
Auth contract v1.1.0 sha256: 3efc6933ffca57596bcb454801434d61a1f811721ab25b32f8d21f3ff5597bee
RBAC contract v1.1.0 sha256: 6254e5f4c8b5c64912d400a7b06fb4da09632e3ee6cc2e7b0ffed1ab0dc52a8f
tracked implementation binary diff sha256: aa6aeb6265b00b50bb85cfbc120442fb37322988f10ebdc9bdc3f92c3057867a
untracked implementation manifest: 15 files, sha256 5b8a82ab168561d7d0bf68dbb4c13afd2210f21f57374604f1296510204524e0
reconstructable implementation patch: specs/004-microservice-splitting/baseline-implementation.patch
patch size: 123047 bytes
patch sha256: 659facfdf93a49bf2caa7e09728451029271ace9b61ab31da47dc5dd0e57e2ee
exact Auth/RBAC contract patch: specs/004-microservice-splitting/baseline-contracts.patch
contract patch size: 9008 bytes
contract patch sha256: f69c52dfefa706d6411c56be1ee90e74f0794c26f48dbc95973a4c9cea9bfa38
```

HEAD 仅是仓库基线。Worktree hashes use `git diff --binary -- . ':(exclude)specs/**' ':(exclude).specify/**'` plus a sorted SHA-256 manifest of untracked files under the same exclusions. The implementation patch combines both implementation components；the contract patch reconstructs the exact Auth/RBAC versioned inputs with the hashes above.

只在 disposable detached worktree 重建，绝不能对当前含未提交改动的工作树应用：

```bash
SOURCE=/home/hdw/vue-element-plus-admin
WT=/tmp/vue-element-plus-admin-004-baseline
IMPLEMENTATION_PATCH="$SOURCE/specs/004-microservice-splitting/baseline-implementation.patch"
CONTRACT_PATCH="$SOURCE/specs/004-microservice-splitting/baseline-contracts.patch"

test "$(sha256sum "$IMPLEMENTATION_PATCH" | cut -d' ' -f1)" = "659facfdf93a49bf2caa7e09728451029271ace9b61ab31da47dc5dd0e57e2ee"
test "$(wc -c < "$IMPLEMENTATION_PATCH")" -eq 123047
test "$(sha256sum "$CONTRACT_PATCH" | cut -d' ' -f1)" = "f69c52dfefa706d6411c56be1ee90e74f0794c26f48dbc95973a4c9cea9bfa38"
test "$(wc -c < "$CONTRACT_PATCH")" -eq 9008
git -C "$SOURCE" worktree add --detach "$WT" d4b1bae75b37f894879360099066ad49ab78da91
git -C "$WT" apply --binary --check "$IMPLEMENTATION_PATCH"
git -C "$WT" apply --binary "$IMPLEMENTATION_PATCH"
git -C "$WT" apply --binary --check "$CONTRACT_PATCH"
git -C "$WT" apply --binary "$CONTRACT_PATCH"
(cd "$WT" && sha256sum specs/002-user-auth/contracts/auth-api.md specs/003-rbac-permission-management/contracts/rbac-api.md)
```

最后一行输出必须分别匹配冻结的 Auth/RBAC hash。所有输入均来自 frozen HEAD + 两个 immutable hashed patch，不复制 mutable current contract files。Baseline test evidence must record command, Go/Node/PostgreSQL environment, timestamp and pass/fail output against these exact identifiers；不得只写“current/latest”。

Backend：

```bash
cd backend
go test ./...
go vet ./...
```

Frontend：

```bash
cd frontend
pnpm test:access-control
pnpm vue-tsc --noEmit --skipLibCheck
pnpm eslint . "src/**/*.{js,ts,tsx,vue,html}"
pnpm prettier --check "src/**/*.{js,ts,json,tsx,css,less,vue,html,md}"
pnpm vite build --mode base
```

Repository whitespace：

```bash
git diff --check
```

已知构建 warning（例如旧 caniuse data 或 MockJS eval）应与新的失败区分，不能把真实 test/build failure 描述为成功。

## 4. Proposed configuration compatibility

Implementation should keep `DATABASE_URL` as the initial compatibility source while allowing module-specific overrides:

```text
IAM_DATABASE_URL              fallback DATABASE_URL
ORGANIZATION_DATABASE_URL     fallback DATABASE_URL
ADMIN_BFF_DATABASE_URL        fallback DATABASE_URL (workflow state only)
ADMIN_BFF_ROUTES_ENABLED                  false  # master kill switch
ADMIN_BFF_SHADOW_READS                    false
ADMIN_BFF_AUTH_PROFILE_READS_ENABLED      false
ADMIN_BFF_USER_LIST_READS_ENABLED         false
ADMIN_BFF_DEPARTMENT_WRITES_ENABLED       false
ADMIN_BFF_ROLE_WRITES_ENABLED             false
ADMIN_BFF_MANAGED_USER_WRITES_ENABLED     false  # create/update only
ADMIN_BFF_USER_DELETE_ROUTE_ENABLED       false
LEGACY_DELETE_IAM_DELEGATION_ENABLED      false
IAM_DELETE_EVENT_CONSUMER_ENABLED         false
OUTBOX_DISPATCHER_ENABLED                 false
```

- No secret default is committed。
- First iteration MUST resolve all three URLs to the same physical PostgreSQL database; startup/config tests reject different database identities。
- The single Platform migrator is the only migration runner; runtime pools do not independently advance migration history。
- Prefer separate least-privilege database roles/pools for access enforcement when available。
- Connection limits must be budgeted across pools rather than multiplying the old max blindly。
- `ADMIN_BFF_ROUTES_ENABLED=false` forces all granular BFF capabilities off；granular flags permit independent stage rollback。
- startup MUST reject delete consumer/dispatcher acceptance unless legacy delete delegation is enabled, bridge mode is verified `legacy_delete_sync_enabled=false`, schema/event contract is ready, and the same-physical-database gate passes。
- `ADMIN_BFF_USER_DELETE_ROUTE_ENABLED` is independent from create/update and MUST remain false until delegation=true, bridge mode=false, consumer/dispatcher ready and delete compatibility tests pass；legacy `/users/delete` remains routable during that preparation stage。
- shadow reads never execute session-authentication paths that slide expiry。

Exact names must be reflected in config tests and README when implemented；if implementation chooses different names, update this guide and contracts together。

## 5. Implementation checkpoints

### Checkpoint A — Compatibility suite

Before route movement, create one table-driven public HTTP suite that can run against both router constructors:

```text
legacy router
Admin BFF router
```

Verify at minimum：

- register 201 and login 200；
- logout `200 {"data":{}}`；
- `/auth/me` complete profile and non-null arrays；
- 401 `AUTH_INVALID_TOKEN`；
- 403 `AUTH_FORBIDDEN` without logout；
- all nine management endpoints and permission matrix；
- built-in role code/delete protection；
- batch atomicity；
- department-filtered user pagination and explicit `department: null` when absent；
- exact idempotency/workflow errors and `Retry-After`。

Do not proceed if the baseline and new router disagree without an explicit contract update.

### Checkpoint B — Architecture boundaries

Automated tests must fail for these imports/references：

```text
internal/iam            -> internal/organization
internal/organization   -> internal/iam
internal/adminbff        -> pgx, generated sqlc packages, migrations  (exception: workflow-store adapter internal/adminbff/postgres)
IAM SQL                  -> departments, organization_user_departments
Organization SQL         -> users, sessions, roles, user_roles, permissions, role_permissions
```

The only cross-representation exception is the reviewed temporary Platform trigger bridge installed by the migrator. It is not generated into either module's sqlc package and runtime module roles cannot invoke cross-domain SQL directly. The only module-boundary exception to the `internal/adminbff` rule is the workflow-store adapter `internal/adminbff/postgres`（Admin BFF owns `admin_workflows*`，mirroring the IAM/Organization postgres adapters）; it may import pgx and only the BFF-owned workflow sqlc package（`db/adminbff`），and the architecture test must whitelist exactly that package（见 plan.md Architecture）。

Recommended checks：

- Go AST/package import test；
- SQL token/reference ownership test；
- compile-time small interfaces rather than global `sqlc.Querier`。

### Checkpoint C — Additive migration

Start integration tests from a database migrated through `000005` and populated with：

- user with department；
- user without department；
- multiple roles/permissions；
- built-in roles；
- realistic legacy modification case already covered by permission migration tests。

Upgrade validation：

1. all existing users become `active` and `version=1`; lifecycle temporary default is dropped after backfill；
2. every existing user has a versioned Organization state row: legacy department copied or nullable department tombstone；
3. counts, nullable values and versions match；
4. no cross-domain FK from new membership to users；
5. legacy `users.department_id` plus FK/index remain usable；Platform bridge verifies all four initial paths: `users` INSERT creates state/tombstone v1, changed legacy department upserts/increments state, changed Organization state INSERT/UPDATE syncs legacy column, and installed legacy `users` DELETE trigger synchronously removes state only while versioned Platform mode is true；
6. IAM/Organization/Platform domain objects and bridge functions are owned by separate NOLOGIN roles；functions use `SECURITY DEFINER SET search_path=pg_catalog` and fully qualified objects, PUBLIC/runtime has no schema CREATE/owner membership/bridge mode execute。Catalog tests prove runtime roles cannot cross-query, `ALTER TABLE ... DISABLE TRIGGER`, replace/change-owner functions, update mode, or self-grant；
7. command receipt/workflow/lease-aware outbox/successful-inbox indexes and checks exist；
8. roles, grants, sessions and credentials are unchanged。

Rollback validation：

1. stop writes/dispatcher in the test scenario；
2. resolve/snapshot workflows through command receipts and snapshot pending/leased/blocked events；
3. final-sync `users.department_id` while new code is still active；
4. verify exact mapping and legacy FK/index before switching routes/binary；
5. preserve workflow/receipt/outbox/inbox tables as dormant evidence；
6. preserve migrations 000004/000005。

### Checkpoint D — sqlc generation

After query/config changes：

```bash
cd backend
sqlc generate
git diff -- internal/iam internal/organization internal/adminbff db sqlc.yaml
```

Generated files must not be manually edited. Verify：

- IAM adapter imports only IAM-generated package；
- Organization adapter imports only Organization-generated package；
- BFF generated package, if any, contains workflow queries only；
- global `internal/database/sqlc` usage is removed by the final phase。

### Checkpoint E — Workflow failure injection

Run unit/integration tests with failures after every side-effect boundary.

Create user：

- IAM provisioning timeout/rollback；
- Organization set failure；
- IAM activation failure；
- compensation failure；
- process restart at every persisted step；expired `running`/`compensating` lease atomic reclaim (compensation reclaim resolves the last compensation receipt and never resets the forward-attempt budget), last-command receipt resolution before next side effect, and stale claim-token persist rejection；
- request ends before credential receipt → `awaiting_client_input`；before deadline no forward-attempt burn and same-key authorized HTTP retry supplies credential in memory；after deadline worker only rejects no-side-effect work or CAS-compensates applied membership, returning `OPERATION_EXPIRED` on success or `RECONCILIATION_REQUIRED` on conflict；intentional credential change uses a new key；
- 10-attempt/original-24h workflow retry exhaustion becomes `failed_manual`/`RECONCILIATION_REQUIRED`；budget cannot reset/extend；manual receipt-finalize/compensation appends immutable recovery action；
- repeated same idempotency key, conflicting key payload, running replay and 24h retention；
- participant commit followed by BFF crash, then `ResolveCommand`；
- activation timeout resolves IAM receipt before any compensation。

Update user：

- target department validation failure；
- Organization timeout unknown outcome；
- IAM transaction failure；
- clear retains null-department tombstone/version; set→clear ABA followed by delayed restore is rejected；
- membership restore conflict/failure and delayed compensation after a newer update；
- stale IAM/membership expected versions；
- crash after IAM commit before workflow success marker, recovered via IAM receipt。

Delete user：

- invalid mixed batch causes zero deletion；
- transaction rollback means no outbox；
- committed delete means durable event；
- duplicate HTTP retry creates no duplicate logical event；
- invalid/missing batch target rejects before subject rows；valid preflight acquires exclusion for every target, and one batch result/receipt persists every tombstone version；
- terminal validation/not-found/protection persists `rejected` and replays the original safe error。

Security assertions：

- revoke actor permission after preflight/between every durable step, including within one uninterrupted HTTP request；the next forward participant side effect must not occur and the reject/compensate/finalize table applies；
- completed same-key replay still performs current authentication/route authorization first；revoked actor receives 403, restored permission may replay stored success but cannot reopen a revocation-rejected workflow；
- provisioning user cannot login；
- workflow rows contain no password/token/hash；
- logs/event payload contain no password/token/hash/username/email for deletion event。

### Checkpoint F — Outbox/inbox crash matrix

Test these exact windows：

1. IAM transaction rollback before commit。
2. delete + event commit while dispatcher stopped。
3. Organization failure before inbox transaction commit。
4. Organization commit followed by producer acknowledgement failure。
5. duplicate/concurrent delivery。
6. unsupported event version or producer/aggregate/payload mismatch enters durable blocked state。
7. claim scan atomically blocks an expired epoch；the 20th failed claimed attempt uses one `RecordOutboxFailure` transaction to choose blocked (never pending-then-separate-block)。
8. worker crash after durable claim is already counted；lease expiry/reclaim increments attempt again；stale claim-token ack/record-failure is rejected。
9. authenticated Platform requeue only: unauthorized/unapproved/non-blocked calls fail；new epoch start equals delayed `available_at`, total attempts persist, and immutable audit snapshots prior start/deadline/block/error/counts。
10. dispatcher restart with backlog。
11. graceful server shutdown during polling。

Expected outcome：at-least-once delivery, zero lost committed events, one effective Organization cleanup per event/handler.

## 6. Local application verification

The project memory indicates the WSL environment normally starts the stack from the WSL repository with：

```bash
./dev.sh
```

The WSL repository and any `D:\` checkout are separate copies；verify edits and commands run in `/home/hdw/vue-element-plus-admin`。

After startup, use a normally registered user. There is no default `admin/admin`.

Promote an existing user from `backend/` only when needed：

```bash
DATABASE_URL='postgres://...' go run ./cmd/admin-init --username alice --role admin
```

Properties that must remain true：

- only `admin` or `super_admin`；
- user already exists；
- no password argument；
- idempotent role grant；
- preserves other roles；
- implementation calls IAM application boundary, not generated sqlc directly。

## 7. Manual end-to-end scenarios

### Authentication and profile

1. Register a normal user。
2. Confirm immediate login and `/auth/me` hydration。
3. Confirm `effective_permissions: []` for a normal user。
4. Promote to admin through CLI。
5. Refresh `/auth/me` without restart and confirm six permissions。
6. Logout and confirm subsequent `/auth/me` is 401。

### Authorization refresh

1. Log in as admin and open a protected page。
2. Remove the relevant role/grant through controlled test setup。
3. Issue the protected request again。
4. Expect 403 `AUTH_FORBIDDEN`，token retained, frontend refreshes `/auth/me` and rebuilds routes。
5. Do not accept redirect caused by false 401 mapping。

### Department/user composition

1. Create root and child department。
2. Create a managed user with role and department。
3. Confirm account is not login-capable until workflow activation completes。
4. Confirm `/users` item combines IAM profile/roles and Organization department; a user without membership still has `"department": null`。
5. Filter by department and verify `total`/pagination before/after unrelated users are added。
6. Move user to another department；verify old and new filters。
7. Confirm official frontend sends one valid `Idempotency-Key` per logical user write and reuses it on retry。
8. Simulate IAM update failure and verify version-guarded membership restoration。
9. Commit a newer membership update before delayed compensation; verify stale restore is rejected and yields `RECONCILIATION_REQUIRED`, not overwrite。

### Deletion event

1. Create user with membership and session。
2. Pause dispatcher。
3. Delete user；confirm authentication fails immediately and IAM list excludes user。
4. Confirm outbox pending and Organization membership may temporarily remain internally。
5. Resume dispatcher；confirm membership removed and inbox processed。
6. Redeliver same event；confirm no additional effect/error。

### Built-in role and batch atomicity

1. Attempt to edit built-in role code → 409 immutable code。
2. Attempt to delete built-in role → 409 protected。
3. Submit `[custom, builtin]` delete batch → custom remains。
4. Run equivalent mixed invalid batches for departments/users → no partial delete。

## 8. Observability acceptance

For a BFF aggregate request, verify logs/traces show：

```text
request_id / trace_id
  adminbff.http
    iam.<operation>
    organization.<operation>
```

Readiness should distinguish：

- `iam_database`
- `organization_database`
- `admin_workflow_store`
- `outbox_dispatcher`

Metrics/alerts should expose：

- HTTP 401 vs 403 rates；
- IAM/Organization port latency and typed errors；
- workflow and compensation failures；
- provisioning age；
- outbox pending/leased/blocked count, oldest age, attempts, lease recovery and stale-claim rejection；
- inbox processed/duplicate plus handler failures。

Inspect logs to ensure no password, raw token, password hash, database URL or unnecessary email content.

## 9. Staged rollout rehearsal

1. Before deploy, pin the exact 004-compatible rollback artifact image/Git SHA/config manifest and run the extended compatibility suite against it；the frozen pre-004 baseline is characterization only。
2. Unique Platform migrator deploys additive schema with master/granular BFF flags and dispatcher disabled; verify all DSNs identify the same physical database。
3. Validate backfill/schema/readiness/catalog role matrix; enable compatibility dual-write with DELETE trigger installed/mode=true and continuous legacy/new compare without nulling/dropping legacy representation。
4. Enable pure-read shadow comparisons only；do not shadow session authentication because it may slide expiry。
5. Independently enable auth/profile reads then user-list reads while dual-write remains active。
6. Independently enable department writes, role writes, then managed-user create/update workflows。
7. Enable legacy-delete IAM delegation and verify no direct-delete entry remains；through short-lived authenticated Platform operation set `legacy_delete_sync_enabled=false`, verify immutable audit and trigger behavior。
8. Enable user delete outbox/Organization consumer only after startup gate confirms delegation=true and bridge mode=false；then run deletion-event failure injection while public `/users/delete` still uses the delegated legacy handler。
9. After delete compatibility/failure tests pass, independently enable `ADMIN_BFF_USER_DELETE_ROUTE_ENABLED` and verify legacy delete route can still be selected for rollback。
10. Append durable rollout-gate samples for capability manifest, bridge mode/version, legacy/new row/version/checksum parity and rollback-suite identity；any mismatch or monitoring gap resets the 72-continuous-hour window。Approve only after one complete passing window。
11. Disable remaining legacy routing only after parity and rollback rehearsal; retain 7 calendar days rollback support with the pinned 004-compatible artifact。
12. Keep workflow/receipts/published events/successful inbox/recovery and requeue audits at least 30 days; never auto-purge unresolved pending/leased/blocked evidence。

## 10. Rollback rehearsal

Before rollback：

1. freeze management writes and new event producers；put dispatcher into drain mode rather than stopping immediately；
2. recover expired leases and drain retryable pending events, then stop new claims；
3. reclaim expired running workflows with fresh token, resolve/snapshot last participant receipts, and snapshot remaining leased/blocked claim/requeue evidence；
4. reconcile deleted-IAM/stale-Organization exclusions；while new code is still active, final-sync Organization state to legacy `users.department_id`；
5. verify exact bidirectional mapping, legacy FK/index, pinned rollback artifact identity and extended-suite result；
6. if the pinned 004-compatible rollback handler can direct-delete `users`, use short-lived authenticated Platform operation to set delete-sync mode=true and verify mode audit/trigger behavior；
7. only after verification switch the granular route matrix and deploy the pinned 004-compatible rollback artifact；never deploy a pre-004 binary that lacks the extended idempotency/workflow contract during the support window；
8. if verification fails, keep writes frozen and reconcile instead of serving stale legacy data；
9. preserve workflow/receipt/outbox/inbox/compatibility schema as dormant evidence; immediate rollback does not drop them。

Never roll back permission migration 000005 or RBAC migration 000004 as part of this feature rollback.

## 11. Final quality gate

From backend：

```bash
sqlc generate
gofmt -w <changed-go-files>
go test ./...
go vet ./...
```

From frontend：

```bash
pnpm test:access-control
pnpm vue-tsc --noEmit --skipLibCheck
pnpm eslint . "src/**/*.{js,ts,tsx,vue,html}"
pnpm prettier --check "src/**/*.{js,ts,json,tsx,css,less,vue,html,md}"
pnpm vite build --mode base
```

From repository root：

```bash
git diff --check
```

Completion requires：

- public compatibility suite passes against Admin BFF and the pinned 004-compatible rollback artifact/wiring；
- architecture ownership tests pass；
- migration up/down/backfill tests pass in real PostgreSQL；
- workflow/command-receipt/outbox-lease/inbox failure injection passes；
- idempotency header validation/replay/conflict and exact public workflow errors pass；
- delayed compensation/concurrent update/nullable-state ABA CAS tests pass；
- revoke actor permission between preflight/durable steps prevents the next forward side effect inside the original request and after recovery；
- durable 72h rollout-gate reset/approval tests and owner-port conservative cleanup dry-run/purge gates pass；
- existing 001/002/003 behavior remains green；
- no secret leakage；
- Organization in-process adapter passes extraction-ready contract suite；
- no independent service/gRPC/broker introduced in this feature。
