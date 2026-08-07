import request from '@/axios'
import type { AuthResult, AuthUser, UserLoginType, UserType } from './types'

interface RoleParams {
  roleName: string
}

/** 登录（真实后端）。成功返回会话令牌 + 用户信息。 */
export const loginApi = (data: UserLoginType): Promise<IResponse<AuthResult>> => {
  return request.post({ url: '/api/v1/auth/login', data })
}

/** 注册（真实后端）。注册即登录，成功返回会话令牌 + 用户信息。 */
export const registerApi = (data: UserLoginType): Promise<IResponse<AuthResult>> => {
  return request.post({ url: '/api/v1/auth/register', data })
}

/** 退出（真实后端）。撤销当前会话。 */
export const loginOutApi = (): Promise<IResponse> => {
  return request.post({ url: '/api/v1/auth/logout' })
}

/** 获取当前用户（真实后端）。用于验证持久化令牌有效性。 */
export const getUserInfoApi = (): Promise<IResponse<{ user: AuthUser }>> => {
  return request.get({ url: '/api/v1/auth/me' })
}

// ---- 以下为角色/权限 Mock 接口（本阶段保持不变，宪章阶段边界） ----

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
