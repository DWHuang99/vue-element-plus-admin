# Health Check API Contract

**Feature**: 001-backend-scaffold
**Date**: 2026-08-04
**Version**: 1.0.0

## Overview

本契约定义了后端脚手架的健康检查 HTTP API。该 API 提供两个端点，允许外部系统
（编排平台、负载均衡器、开发者）区分"进程存活"和"服务就绪"两种状态。

---

## Base URL

```
http://{host}:{port}
```

- `host`: 由 `SERVER_HOST` 环境变量配置，默认 `0.0.0.0`
- `port`: 由 `SERVER_PORT` 环境变量配置，默认 `8080`

---

## Endpoints

### GET /health/live — 存活检查

验证服务进程正在运行且能够响应 HTTP 请求。

**Request**

```
GET /health/live HTTP/1.1
Host: localhost:8080
```

无请求体。无查询参数。无认证头。

**Response — 200 OK**

```json
{
  "status": "ok",
  "timestamp": "2026-08-04T10:00:00Z"
}
```

**Response — 5xx**

此端点 **不** 返回 5xx。只要进程存活即返回 200。即使数据库不可用也返回 200。

**Constraints**

- 不在日志中记录每次存活检查（避免高频日志噪音 — Edge Case 要求）
- 不执行任何持久化副作用
- P95 响应时间 < 50ms

---

### GET /health/ready — 就绪检查

验证服务所有关键依赖可用且处于健康状态，可以处理业务请求。

**Request**

```
GET /health/ready HTTP/1.1
Host: localhost:8080
```

无请求体。无查询参数。无认证头。

**Response — 200 OK（就绪）**

```json
{
  "status": "ok",
  "timestamp": "2026-08-04T10:00:00Z",
  "checks": {
    "database": "ok"
  }
}
```

**Response — 503 Service Unavailable（未就绪）**

```json
{
  "status": "degraded",
  "timestamp": "2026-08-04T10:00:00Z",
  "checks": {
    "database": "unavailable"
  }
}
```

Possible `database` check values:

| Value | Meaning |
|-------|---------|
| `"ok"` | 数据库 ping 成功 AND 迁移已执行 AND dirty=false |
| `"unavailable"` | 连接失败、超时、或迁移存在脏状态 |

**Constraints**

- 不记录每次成功的就绪检查日志；记录就绪状态变更（ok → unavailable 或反之）
- 不执行持久化副作用
- P95 响应时间 < 50ms（数据库 ping 应在 1 秒内超时）

---

## Common Behaviors

### Error Responses

所有端点遵守统一的错误响应格式（用于未匹配路径等场景，健康检查本身不生成此格式）:

```json
{
  "error": {
    "code": "NOT_FOUND",
    "message": "The requested path was not found"
  }
}
```

Error codes:

| HTTP Status | Code | Meaning |
|-------------|------|---------|
| 404 | `NOT_FOUND` | 请求的路径未匹配到任何处理器 |
| 405 | `METHOD_NOT_ALLOWED` | HTTP 方法未匹配 |

### Security

- 所有端点 **不要求认证**（本阶段无认证系统）
- 响应体 **不包含** 秘密、令牌、密码或内部配置值
- 错误响应 **不包含** 堆栈追踪或内部实现细节

### Content Type

所有响应使用 `Content-Type: application/json; charset=utf-8`.

### Headers

| Header | Value |
|--------|-------|
| `X-Request-Id` | 请求 ID（由中间件注入，UUID v4） |
| `X-Response-Time` | 端点处理耗时（毫秒） |

---

## Contract Tests

以下测试必须通过才能声明契约合规:

1. **存活检查 — 基本**: GET /health/live → 200, body.status="ok", body.timestamp 有效
2. **存活检查 — 无副作用**: 连续调用 100 次 GET /health/live → 全部 200，无日志洪水，
   无数据库写入
3. **就绪检查 — 数据库可用**: GET /health/ready → 200, body.status="ok",
   body.checks.database="ok"
4. **就绪检查 — 数据库不可用**: 停止数据库后 GET /health/ready → 503,
   body.status="degraded", body.checks.database="unavailable"
5. **就绪检查 — 恢复**: 重新启动数据库后 GET /health/ready → 200, body.status="ok"
6. **未匹配路径**: GET /nonexistent → 404, body.error.code="NOT_FOUND"
7. **错误方法**: POST /health/live → 405 (如果已实现方法检查; 否则 gin 默认 404)
8. **秘密泄露**: 对所有响应字符串匹配 secret/password/token/key → 零命中
9. **性能**: 100 次采样 P95 < 50ms

---

## Versioning

本契约采用语义化版本。当前版本 1.0.0。

- **向后兼容**: 在不修改已有字段或状态码的前提下添加字段或端点
- **破坏性变更**: 必须提供新版端点 (e.g., `/v2/health/live`) 或经批准的同步迁移方案
