import request from '@/axios'
import type { LoginResponse, RefreshResponse, RegisterType, UserLoginType, UserType } from './types'

interface RoleParams {
  roleName: string
}

export const loginApi = (data: UserLoginType): Promise<IResponse<LoginResponse>> => {
  return request.post({ url: '/api/v1/auth/login', data })
}

export const registerApi = (data: RegisterType): Promise<IResponse> => {
  return request.post({ url: '/api/v1/auth/register', data })
}

export const refreshApi = (): Promise<IResponse<RefreshResponse>> => {
  return request.post({ url: '/api/v1/auth/refresh' })
}

export const getCurrentUserApi = (): Promise<IResponse<UserType>> => {
  return request.get({ url: '/api/v1/users/me' })
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

export const getAdminRoleApi = (
  params: RoleParams
): Promise<IResponse<AppCustomRouteRecordRaw[]>> => {
  return request.get({ url: '/mock/role/list', params })
}

export const getTestRoleApi = (params: RoleParams): Promise<IResponse<string[]>> => {
  return request.get({ url: '/mock/role/list2', params })
}
