import request from '@/axios'
import { DepartmentListResponse } from './types'

export const getDepartmentApi = () => {
  return request.get<DepartmentListResponse>({ url: '/api/v1/departments/tree' })
}

export const saveDepartmentApi = (data: any) => {
  return data.id
    ? request.put({ url: `/api/v1/departments/${data.id}`, data })
    : request.post({ url: '/api/v1/departments', data })
}

export const deleteDepartmentApi = (ids: string[] | number[]) => {
  return request.delete({ url: '/api/v1/departments', data: { ids } })
}

export const getDepartmentTableApi = (params: any) => {
  return request.get({ url: '/api/v1/departments', params })
}
