import request from '@/axios'

export const getRoleListApi = (params?: any) => {
  return request.get({ url: '/api/v1/roles', params })
}

export const getRoleDetailApi = (id: string | number) => {
  return request.get({ url: `/api/v1/roles/${id}` })
}

export const saveRoleApi = (data: any) => {
  return data.id
    ? request.put({ url: `/api/v1/roles/${data.id}`, data })
    : request.post({ url: '/api/v1/roles', data })
}

export const deleteRoleApi = (id: string | number) => {
  return request.delete({ url: `/api/v1/roles/${id}` })
}
