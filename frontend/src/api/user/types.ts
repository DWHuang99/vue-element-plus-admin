import type { DepartmentItem } from '@/api/department/types'

export interface UserParams {
  pageSize: number
  pageIndex: number
  departmentId: string | number
  username?: string
  account?: string
}

export interface UserItem {
  id: string
  username: string
  account: string
  email: string
  createTime: string
  role: string
  roleId: number
  department: DepartmentItem
}

export interface UserResponse {
  list: UserItem[]
  total: number
}
