import request from '@/axios'
import type { UserParams, UserResponse } from './types'

export const getUserListApi = (params: UserParams) => {
  return request.get<UserResponse>({ url: '/api/v1/users', params })
}

export const deleteUserApi = (ids: string[] | number[]) => {
  return request.delete({ url: '/api/v1/users', data: { ids } })
}

export const saveUserApi = (data: any) => {
  const payload = { ...data, departmentId: data.departmentId || data.department?.id }
  delete payload.department
  return data.id
    ? request.put({ url: `/api/v1/users/${data.id}`, data: payload })
    : request.post({ url: '/api/v1/users', data: payload })
}
