import request from '@/axios'
import type { LoginResponse, RefreshResponse, RegisterType, UserLoginType, UserType } from './types'

export const loginApi = (data: UserLoginType): Promise<IResponse<LoginResponse>> => {
  return request.post({ url: '/api/v1/auth/login', data })
}

export const registerApi = (data: RegisterType): Promise<IResponse> => {
  return request.post({ url: '/api/v1/auth/register', data })
}

export const refreshApi = (): Promise<IResponse<RefreshResponse>> => {
  return request.post({ url: '/api/v1/auth/refresh' })
}

export const getOIDCLoginURL = (): string => {
  const apiBasePath = import.meta.env.VITE_API_BASE_PATH.replace(/\/$/, '')
  return `${apiBasePath}/api/v1/oauth/login`
}

export const getCurrentUserApi = (): Promise<IResponse<UserType>> => {
  return request.get({ url: '/api/v1/users/me' })
}

export const getCurrentUserMenusApi = (): Promise<
  IResponse<{ list: AppCustomRouteRecordRaw[] }>
> => {
  return request.get({ url: '/api/v1/users/me/menus' })
}

export const loginOutApi = (): Promise<IResponse> => {
  return request.post({ url: '/api/v1/auth/logout' })
}

export const getUserListApi = ({ params }: AxiosConfig) => {
  return request.get<{
    code: string
    data: {
      list: UserType[]
      total: number
    }
  }>({ url: '/mock/user/list', params })
}
