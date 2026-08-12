import axios, { AxiosError } from 'axios'
import { defaultRequestInterceptors, defaultResponseInterceptors } from './config'

import { AxiosInstance, InternalAxiosRequestConfig, RequestConfig, AxiosResponse } from './types'
import { ElMessage } from 'element-plus'
import { REQUEST_TIMEOUT } from '@/constants'
import { useUserStoreWithOut } from '@/store/modules/user'
import type { RefreshResponse } from '@/api/login/types'

export const PATH_URL = import.meta.env.VITE_API_BASE_PATH

const abortControllerMap: Map<string, AbortController> = new Map()

const axiosInstance: AxiosInstance = axios.create({
  timeout: REQUEST_TIMEOUT,
  baseURL: PATH_URL,
  withCredentials: true
})

const refreshClient = axios.create({
  timeout: REQUEST_TIMEOUT,
  baseURL: PATH_URL,
  withCredentials: true
})

interface RetryRequestConfig extends InternalAxiosRequestConfig {
  _retry?: boolean
}

let refreshRequest: Promise<string> | null = null

const requestNewAccessToken = () => {
  if (!refreshRequest) {
    refreshRequest = refreshClient
      .post<IResponse<RefreshResponse>>('/api/v1/auth/refresh')
      .then((response) => {
        if (response.data.code !== 0 || !response.data.data?.accessToken) {
          throw new Error('refresh access token failed')
        }
        return response.data.data.accessToken
      })
      .finally(() => {
        refreshRequest = null
      })
  }

  return refreshRequest
}

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
    return Promise.reject(error)
  }
)

axiosInstance.interceptors.request.use(defaultRequestInterceptors)
axiosInstance.interceptors.response.use(defaultResponseInterceptors)
axiosInstance.interceptors.response.use(undefined, async (error: AxiosError<IResponse>) => {
  const originalRequest = error.config as RetryRequestConfig | undefined
  const userStore = useUserStoreWithOut()
  const isAuthEndpoint = originalRequest?.url?.startsWith('/api/v1/auth/') ?? false

  if (
    error.response?.status === 401 &&
    originalRequest &&
    !originalRequest._retry &&
    !isAuthEndpoint &&
    Boolean(userStore.getToken)
  ) {
    originalRequest._retry = true

    try {
      const accessToken = await requestNewAccessToken()
      userStore.setToken(accessToken)
      originalRequest.headers.Authorization = `Bearer ${accessToken}`
      return axiosInstance.request(originalRequest)
    } catch {
      userStore.logout()
      return Promise.reject(error)
    }
  }

  ElMessage.error(error.response?.data?.message || error.message)
  return Promise.reject(error)
})

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
