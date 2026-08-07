# Research: 注册功能第一阶段技术决策

**Feature**: 002-user-auth
**Date**: 2026-08-05
**Status**: Complete

## 研究摘要

本文档记录用户认证第一阶段所有关键技术决策。所有决策基于已批准设计文档
`docs/superpowers/specs/2026-08-05-registration-phase1-design.md`、项目宪章
（`.specify/memory/constitution.md`）与已有脚手架实现。

---

## 决策 1: 密码哈希算法 — Argon2id

**Decision**: Argon2id，参数 t=3, m=64MB, p=4，盐 16 字节，输出 32 字节

**Rationale**:
- OWASP 密码存储指南首选算法（2018 年 PHC 竞赛冠军）
- 内存硬函数，显著提高 GPU/ASIC 暴力破解成本
- `golang.org/x/crypto/argon2` 是经过审查的官方实现
- 每账户独立随机盐（宪章 I 明确要求），使用 `crypto/rand` 生成
- 参数在编码串中保存（PHC 格式），未来升级参数/算法无需迁移存量哈希
- 64MB 内存成本在单实例下足够（数百并发登录可承受），响应 <200ms 可达成

**时序注意（契约测试 #11 对齐）**: m=64MB 单次哈希典型耗时 100–300ms（硬件相关）。
契约登录性能目标已放宽为 P95 < 400ms（contracts/auth-api.md）。实现阶段 MUST 实测
register/login 实际耗时；若 P95 超 400ms，应调低 m 参数（如 32MB）并同步更新契约与本决策。
不可在未实测的情况下降低安全参数。

**Alternatives considered**:
- bcrypt: 经典、广泛审查，但非内存硬函数，对 GPU 攻击防护弱于 Argon2id；成本参数最高 31
- scrypt: 内存硬，但参数较复杂，OWASP 首选为 Argon2id
- PBKDF2: SHA 家族基础，已被 OWASP 列为不再推荐的首选（无内存硬性）

---

## 决策 2: 会话机制 — 服务端不透明令牌（Bearer）

**Decision**: 32 字节 `crypto/rand` 随机令牌；数据库仅存 SHA-256 哈希；客户端经
`Authorization: Bearer` 头携带

**Rationale**:
- 与前端 SPA + axios 拦截器天然契合（附加请求头，无需 Cookie 凭据配置）
- 无 CSRF 面（非 Cookie 自动携带）
- 撤销简单：删除/标记会话记录即可，无需黑名单（区别于 JWT）
- 服务端仅存 SHA-256 哈希：即使数据库泄漏，令牌不可重放（宪章 FR-013）
- 随机性 256 bit，碰撞/猜测不可行

**Alternatives considered**:
- JWT（无状态）: 撤销需黑名单，与宪章"撤销"要求冲突；密钥管理复杂；本阶段单令牌即可
- Cookie 会话: 需处理 CSRF、SameSite、跨域凭据（CORS credentials），前后端分离场景配置复杂
- 刷新令牌机制: 本阶段不需要（宪章非目标明确排除），后续阶段可加

---

## 决策 3: 令牌生命周期

**Decision**: 滑动空闲过期（`expires_at` = 最近使用 + 24h）+ 绝对生命周期上限（`created_at` + 7 天）
+ 撤销（`revoked_at`）

**Rationale**:
- 空闲过期: 每次认证成功将 `expires_at` 顺延至 now + 24h（`SessionTTL`），活跃用户不被强制登出
- 绝对上限: 无论多么活跃，会话最迟在创建后 7 天（`MaxSessionLifetime`）失效——防止无限期存活
  的会话，是滑动过期的安全背板
- 撤销: 中间件校验 `revoked_at IS NULL`；退出时设置 `revoked_at`（软撤销，保留审计痕迹）；
  配合 FK ON DELETE CASCADE 硬删除
- 过期/撤销/无效统一返回 `AUTH_INVALID_TOKEN`（防枚举）

> **实现澄清（2026-08-05）**: 规划初稿中"绝对过期 24h + 距过期 < 7 天续期"存在内部矛盾
> （24h 绝对过期下任何会话都距过期 < 7 天，续期将永不封顶）。实现阶段将其修正为
> "24h 空闲滑动 + 7 天绝对上限"，见 token.go 的 `SessionIsValid` 与 data-model.md。

**Alternatives considered**:
- 仅绝对过期: 活跃用户被迫频繁重新登录，体验差
- 仅滑动过期（无上限）: 长期活跃会话永不失效，安全风险
- 短 TTL + 刷新令牌: 本阶段非目标

---

## 决策 4: 速率限制 — 内存滑动窗口

**Decision**: 内存滑动窗口实现（`internal/ratelimit`），配置化开关

**Rationale**:
- 宪章 I 明确要求"速率限制与防账户枚举措施"
- 单实例部署，内存实现足够（无外部依赖，避免引入 Redis 的过度设计）
- 按 IP + 按用户名双维度：注册按 IP（每 IP/小时 10 次）；登录按 IP（每 IP/15 分钟 10 次）
  且按用户名（每用户名/15 分钟 5 次）
- 限流触发返回 `RATE_LIMITED` (429)，响应含 `Retry-After` 头
- 配置化（`RATE_LIMIT_ENABLED`），测试可关闭

**Alternatives considered**:
- Redis 分布式限流: 多实例才需要；单实例下过度设计（违反宪章 II）
- 固定窗口: 实现简单但有边界突刺问题；滑动窗口更平稳
- 令牌桶: 适合突发流量，但防暴力场景滑动窗口语义更直观

---

## 决策 5: 认证架构 — 服务层分离

**Decision**: `internal/auth` 包内 service / password / token / handler 分离；中间件独立

**Rationale**:
- 宪章 IV: "认证与业务规则 MUST 位于可独立测试的服务层，HTTP 处理器只负责协议适配、校验和响应映射"
- `service.go` 只依赖数据库接口（sqlc 生成的 Querier 接口），可注入 mock 独立单测
- `password.go` / `token.go` 纯函数，易测
- `handler.go` 只做 HTTP 协议适配（解析、校验、调用 service、映射响应/错误）
- `middleware/auth.go` 解析 Bearer → 校验令牌 → 注入用户上下文

**Alternatives considered**:
- 处理器直连 DB（方案 B）: 违反宪章 IV，不可接受
- DDD 仓储模式（方案 C）: 违反宪章 II（引入未被需求使用的抽象），拒绝

---

## 决策 6: 防账户枚举 — 统一登录失败响应

**Decision**: 登录失败统一返回 `AUTH_INVALID_CREDENTIALS` (401)，不区分"用户名不存在"/"密码错误"

**Rationale**:
- 宪章 I: 防账户枚举
- 即使用户名不存在，也执行一次虚拟哈希比较（恒定时间路径），避免时序侧信道
- 注册端点 `AUTH_USERNAME_TAKEN` (409) 属业务语义（注册时公开占用情况是必要的 UX），
  但响应不泄露账户敏感信息

**Alternatives considered**:
- 区分错误（"用户不存在"/"密码错误"）: 便于诊断但泄露账户枚举信息，违反宪章
- 仅凭 200/401 区分: 已采用统一 401

---

## 决策 7: 前端认证接线模式

**Decision**: 仅接线认证相关接口；保留现有 axios 拦截器架构；令牌存 localStorage

**Rationale**:
- 宪章 II 阶段边界: 本阶段仅认证，角色/权限/菜单保持 Mock
- 现有 `axios/service.ts` 已有拦截器骨架，在其上附加 Bearer 令牌
- 401 统一处理: 拦截器清除 token → 跳登录页
- `VITE_API_BASE_URL` 通过 `.env.development` 配置，生产环境走 `.env.production`
- 用户信息以 `/auth/me` 返回为准（前端启动时验证令牌有效性）

**Alternatives considered**:
- 全接口接线: 超出宪章阶段 1 范围
- HttpOnly Cookie 存储: 与令牌方案（Bearer）不匹配
- sessionStorage: 刷新丢失，localStorage 更符合持久登录预期

---

## 决策 8: sqlc 代码组织

**Decision**: `users.sql` / `sessions.sql` 两个查询文件，生成到 `internal/database/sqlc/`；
注册事务（建用户 + 建会话）在服务层用 pgx 事务封装

**Rationale**:
- 宪章 III: SQL 为单一事实来源，生成代码不手工修改
- sqlc 生成 Querier 接口（`emit_interface: true` 已在 sqlc.yaml 配置），便于服务层注入 mock
- 多写操作原子性: 注册 = INSERT user + INSERT session，使用 `pgxpool.BeginTx` 单事务
  （宪章 III: "涉及多个写操作的身份状态变更 MUST 在单个事务中保持原子性"）

**Alternatives considered**:
- 手工 SQL 拼装: 违反宪章 III
- ORM: 宪章禁止

---

## 决策 9: 用户名与密码校验规则

**Decision**:
- 用户名: 3–32 字符，`^[a-zA-Z0-9_]+$`；数据库 UNIQUE 约束兜底
- 密码: 8–72 字节（NIST 建议长度优先，不强制字符组合）；Argon2id 输出 32B
  编码后存储长度远小于 72B 上限，无 bcrypt 72B 截断问题

**Rationale**:
- 3–32 字符规则简单明确，避免用户名含空格/特殊字符导致的 URL/前端转义问题
- NIST SP 800-63B: 长度优先，允许所有可打印字符，不强制复杂字符组合
- 上限 72 字节兼容未来切换到 bcrypt 的可能性（防御性上限）

**Alternatives considered**:
- 强制大小写+数字+符号组合: NIST 已明确不推荐（降低可用性，收益有限）
- 邮箱作登录标识: 本阶段非目标（Assumptions 已声明）

---

## 决策汇总

| # | 决策域 | 选择 | 核心原因 |
|---|--------|------|----------|
| 1 | 密码哈希 | Argon2id t=3/m=64MB/p=4, PHC 编码 | OWASP 首选，内存硬，参数可升级 |
| 2 | 会话机制 | 32B 随机令牌 + SHA-256 存储哈希 | 可撤销，无 CSRF，泄漏安全 |
| 3 | 令牌生命周期 | 24h 绝对过期 + 滑动续期 | 安全上限 + 活跃用户体验 |
| 4 | 速率限制 | 内存滑动窗口，IP+用户名双维度 | 满足宪章，单实例足够 |
| 5 | 认证架构 | 服务层分离（service/password/token/handler） | 宪章 IV 强制 |
| 6 | 防枚举 | 统一登录失败 401 | 宪章 I 强制 |
| 7 | 前端接线 | 仅认证接口，Bearer + localStorage | 阶段边界，架构契合 |
| 8 | 数据访问 | sqlc 生成 + 注册事务 | 宪章 III 强制 |
| 9 | 校验规则 | 用户名 3–32 字母数字下划线；密码 8–72B | NIST 长度优先 |
