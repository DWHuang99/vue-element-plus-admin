import axios, { AxiosError } from 'axios'
import { defaultRequestInterceptors, defaultResponseInterceptors } from './config'

import { AxiosInstance, InternalAxiosRequestConfig, RequestConfig, AxiosResponse } from './types'
import { ElMessage } from 'element-plus'
import { REQUEST_TIMEOUT } from '@/constants'
import { useUserStoreWithOut } from '@/store/modules/user'

/** 认证相关端点：401 表示"凭据错误"，不应触发全局登出跳转。 */
const AUTH_ENDPOINTS = ['/api/v1/auth/login', '/api/v1/auth/register']

/**
 * 幂等写自动重试（T060）：携带 `Idempotency-Key` 的写请求在传输层失败（无响应/
 * 超时/502/503/504——后端结果未知）时自动重发原请求，**同一 key、同一 payload**，
 * 让后端 resolve-first 重放或续跑 `awaiting_client_input` 工作流。409
 * OPERATION_IN_PROGRESS 同样重试（同 key 复查，尊重 Retry-After）。有明确响应的
 * 其余 4xx（409 IDEMPOTENCY_CONFLICT/OPERATION_EXPIRED 等）是终态，绝不重试。
 */
const WRITE_RETRYABLE_STATUS = new Set([502, 503, 504])
const MAX_WRITE_RETRIES = 2
const WRITE_RETRY_DELAY_MS = 800

export const PATH_URL = import.meta.env.VITE_API_BASE_PATH

const abortControllerMap: Map<string, AbortController> = new Map()

const axiosInstance: AxiosInstance = axios.create({
  timeout: REQUEST_TIMEOUT,
  baseURL: PATH_URL
})

axiosInstance.interceptors.request.use((res: InternalAxiosRequestConfig) => {
  const controller = new AbortController()
  const url = res.url || ''
  res.signal = controller.signal
  abortControllerMap.set(
    import.meta.env.VITE_USE_MOCK === 'true' ? url.replace('/mock', '') : url,
    controller
  )
  return res
})

axiosInstance.interceptors.response.use(
  (res: AxiosResponse) => {
    const url = res.config.url || ''
    abortControllerMap.delete(url)
    // 这里不能做任何处理，否则后面的 interceptors 拿不到完整的上下文了
    return res
  },
  (error: AxiosError) => {
    // 幂等写重试：原请求（含 Idempotency-Key 与 body）原样重发，尊重 Retry-After。
    // 取消判定用属性检查而非 axios.isCancel 谓词（该谓词会把 error 收窄为 never）。
    const cancelled = error.name === 'CanceledError' || error.code === 'ERR_CANCELED'
    const config = error.config as
      | (InternalAxiosRequestConfig & { _writeRetries?: number })
      | undefined
    const respData = error.response?.data as
      | { error?: { code?: string; message?: string } }
      | undefined
    const writeRetryable =
      !!config?.headers?.['Idempotency-Key'] &&
      !cancelled &&
      (config._writeRetries ?? 0) < MAX_WRITE_RETRIES &&
      (!error.response ||
        WRITE_RETRYABLE_STATUS.has(error.response.status) ||
        (error.response.status === 409 && respData?.error?.code === 'OPERATION_IN_PROGRESS'))
    if (writeRetryable) {
      config._writeRetries = (config._writeRetries ?? 0) + 1
      const retryAfter = Number(error.response?.headers?.['retry-after'] ?? 0)
      const delay = retryAfter > 0 ? retryAfter * 1000 : WRITE_RETRY_DELAY_MS
      return new Promise((resolve) => setTimeout(resolve, delay)).then(() =>
        axiosInstance.request(config)
      )
    }

    console.log('err： ' + error) // for debug
    // 提取后端统一错误信封 `{ error: { code, message } }` 中的可读消息。
    const msg = respData?.error?.message || error.message
    ElMessage.error(msg)
    // 受保护端点的 401 表示会话失效 → 全局登出跳转；认证端点除外（凭据错误由表单处理）。
    const url = error.config?.url || ''
    const userStore = useUserStoreWithOut()
    if (error.response?.status === 401 && !AUTH_ENDPOINTS.some((e) => url.startsWith(e))) {
      userStore.reset()
    } else if (error.response?.status === 403 && respData?.error?.code === 'AUTH_FORBIDDEN') {
      // 后端授权实时查询数据库；403 时同步最新权限并重建菜单/动态路由。
      void userStore.refreshAuthorization().catch(() => {})
    }
    return Promise.reject(error)
  }
)

axiosInstance.interceptors.request.use(defaultRequestInterceptors)
axiosInstance.interceptors.response.use(defaultResponseInterceptors)

const service = {
  request: (config: RequestConfig) => {
    return new Promise((resolve, reject) => {
      if (config.interceptors?.requestInterceptors) {
        config = config.interceptors.requestInterceptors(config as any)
      }

      axiosInstance
        .request(config)
        .then((res) => {
          resolve(res)
        })
        .catch((err: any) => {
          reject(err)
        })
    })
  },
  cancelRequest: (url: string | string[]) => {
    const urlList = Array.isArray(url) ? url : [url]
    for (const _url of urlList) {
      abortControllerMap.get(_url)?.abort()
      abortControllerMap.delete(_url)
    }
  },
  cancelAllRequest() {
    for (const [_, controller] of abortControllerMap) {
      controller.abort()
    }
    abortControllerMap.clear()
  }
}

export default service
