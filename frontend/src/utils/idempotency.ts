/**
 * 幂等键注册表（T060 / contracts/http-api-compatibility.md Managed-user idempotency）。
 *
 * 后端写端点（POST /api/v1/users、/api/v1/users/delete）要求一次逻辑提交携带一个
 * `Idempotency-Key`（16–128 位 `[A-Za-z0-9._:-]+`）：
 *   - 同 key + 同 safe 请求 → 重放已存储结果（成功/拒绝），绝不重复副作用；
 *   - 同 key + 冲突 payload → 409 IDEMPOTENCY_CONFLICT；
 *   - 同 key 的 `awaiting_client_input` 工作流 → 续跑原工作流（credential 在内存中补交）。
 *
 * 客户端规则（Checkpoint E）：
 *   - 一次逻辑提交 = 一个 key：超时/网络失败（结果未知）后重试复用原 key 与原 credential，
 *     让后端 resolve-first 重放或续跑；
 *   - 收到明确终态响应（成功/4xx/其余 5xx）→ 下一次提交用新 key；
 *   - 有意修改 credential（如密码）→ 即使上次结果未知也用新 key（credential 不在请求
 *     指纹内，新密码属于新意图）。
 */

/** 一次逻辑提交的登记项。 */
interface IdempotencyEntry {
  key: string
  /** 上次提交的 credential（save: 密码字段；delete: 空串）。变化 → 新 key。 */
  credential: string
  /** 目标指纹（save: username+roles；delete: 排序后的 ids）。变化 → 新 key。 */
  payload: string
  /** false = 上次提交已有明确终态；true = 结果未知，同 credential/payload 可复用 key。 */
  outcomeUnknown: boolean
}

const registry = new Map<string, IdempotencyEntry>()

/** 注册表上限：防止长期使用后内存无界增长（FIFO 淘汰最旧项）。 */
const REGISTRY_MAX_ENTRIES = 100

const KEY_CHARSET = /^[A-Za-z0-9._:-]+$/

/** 生成合规幂等键：crypto.randomUUID()（36 位 `[0-9a-f-]`）天然满足 16–128 与字符集。 */
export const genIdempotencyKey = (): string => {
  const key = crypto.randomUUID()
  // 防御性校验：randomUUID 恒合规，此分支理论上不可达。
  if (KEY_CHARSET.test(key) && key.length >= 16 && key.length <= 128) {
    return key
  }
  throw new Error('idempotency key generation failed')
}

/**
 * 取该逻辑提交的幂等键。scope 标识逻辑目标（save: 用户 id 或 create；delete: 目标集合）：
 * 上次结果未知且 credential/payload 均未变 → 复用原 key；否则生成新 key 并登记。
 */
export const idempotencyKeyFor = (scope: string, credential: string, payload: string): string => {
  const prev = registry.get(scope)
  if (prev && prev.outcomeUnknown && prev.credential === credential && prev.payload === payload) {
    return prev.key
  }
  if (registry.size >= REGISTRY_MAX_ENTRIES) {
    const oldest = registry.keys().next().value
    if (oldest) registry.delete(oldest)
  }
  const key = genIdempotencyKey()
  registry.set(scope, { key, credential, payload, outcomeUnknown: true })
  return key
}

/** 提交已有明确终态（成功或确定的服务器拒绝）→ 后续提交用新 key。 */
export const markIdempotencyKnown = (scope: string): void => {
  const prev = registry.get(scope)
  if (prev) prev.outcomeUnknown = false
}

/**
 * 传输层失败判定：无响应（超时/断网）或 502/503/504 → 结果未知，key 必须保留；
 * 409 OPERATION_IN_PROGRESS 同样非终态——后端会附 Retry-After，同 key 复查即可；
 * 其余（有响应信封，含 409 OPERATION_EXPIRED/IDEMPOTENCY_CONFLICT、500 等）→ 终态。
 */
export const isDefiniteOutcome = (err: unknown): boolean => {
  const resp = (
    err as {
      response?: { status?: number; data?: { error?: { code?: string } } }
    } | null
  )?.response
  if (!resp) return false
  if ([502, 503, 504].includes(resp.status ?? 0)) return false
  if (resp.status === 409 && resp.data?.error?.code === 'OPERATION_IN_PROGRESS') return false
  return true
}
