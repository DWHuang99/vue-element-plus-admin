import request from '@/axios'
import {
  DepartmentItem,
  DepartmentListResponse,
  DepartmentUserItem,
  DepartmentUserParams,
  DepartmentUserResponse
} from './types'

/** 后端部门树节点（name/parent_id/children）→ 前端树节点（departmentName/parentId/children）。 */
const toDepartmentTree = (nodes: any[]): DepartmentItem[] => {
  return (nodes || []).map((n) => ({
    id: String(n.id),
    departmentName: n.name,
    parentId: n.parent_id ? String(n.parent_id) : undefined,
    children: n.children ? toDepartmentTree(n.children) : undefined
  }))
}

/** 后端用户行 → 前端用户行（create_time→createTime、department.name→department.departmentName）。 */
const toUserItem = (u: any): DepartmentUserItem => ({
  id: String(u.id),
  username: u.username,
  account: u.account,
  email: u.email,
  createTime: u.create_time,
  role: u.role,
  department: {
    id: String(u.department?.id ?? ''),
    departmentName: u.department?.name ?? ''
  }
})

/** 部门树（GET /api/v1/departments）——供部门树/表单 TreeSelect 使用。 */
export const getDepartmentApi = async (): Promise<IResponse<DepartmentListResponse>> => {
  const res = await request.get<{ list: any[] }>({ url: '/api/v1/departments' })
  return {
    code: 0,
    data: {
      list: toDepartmentTree(res.data?.list)
    }
  }
}

const flattenTree = (nodes: DepartmentItem[]): DepartmentItem[] => {
  const out: DepartmentItem[] = []
  const walk = (list: DepartmentItem[]) => {
    for (const n of list) {
      out.push(n)
      if (n.children?.length) walk(n.children)
    }
  }
  walk(nodes)
  return out
}

/** 部门表格（扁平化树）——维持 Department.vue useTable 的 {list,total} 契约。
 * 后端部门接口不分页，params 仅保持调用方签名兼容，被忽略。 */
export const getDepartmentTableApi = async (
  _params?: any
): Promise<IResponse<{ list: DepartmentItem[]; total: number }>> => {
  const res = await getDepartmentApi()
  const list = flattenTree(res.data.list)
  return {
    code: 0,
    data: {
      list,
      total: list.length
    }
  }
}

/** 保存部门（POST /api/v1/departments）。表单 departmentName→name、parentId→parent_id；id 用于编辑。 */
export const saveDepartmentApi = (data: any): Promise<IResponse> => {
  return request.post({
    url: '/api/v1/departments',
    data: {
      id: data.id ? Number(data.id) : undefined,
      name: data.departmentName,
      parent_id: data.parentId ? Number(data.parentId) : null
    }
  })
}

/** 删除部门（POST /api/v1/departments/delete）。 */
export const deleteDepartmentApi = (ids: string[] | number[]): Promise<IResponse> => {
  return request.post({
    url: '/api/v1/departments/delete',
    data: { ids: ids.map(Number) }
  })
}

/** 部门用户列表（GET /api/v1/users）：id→department_id、pageIndex→page_index、pageSize→page_size。 */
export const getUserByIdApi = async (
  params: DepartmentUserParams
): Promise<IResponse<DepartmentUserResponse>> => {
  const { id, pageIndex, pageSize, username, account } = params
  const res = await request.get<{ list: any[]; total: number }>({
    url: '/api/v1/users',
    params: {
      department_id: id ? Number(id) : 0,
      page_index: pageIndex,
      page_size: pageSize,
      username,
      account
    }
  })
  return {
    code: 0,
    data: {
      list: (res.data?.list || []).map(toUserItem),
      total: res.data?.total || 0
    }
  }
}

/** 保存用户（POST /api/v1/users）。表单 department→department_id、role→roles。 */
export const saveUserApi = (data: any): Promise<IResponse> => {
  return request.post({
    url: '/api/v1/users',
    data: {
      id: data.id ? Number(data.id) : undefined,
      username: data.username,
      account: data.account,
      email: data.email,
      password: data.password,
      department_id: data.department?.id ? Number(data.department.id) : null,
      roles: (data.role || []).map(Number)
    }
  })
}

/** 删除用户（POST /api/v1/users/delete）。 */
export const deleteUserByIdApi = (ids: string[] | number[]): Promise<IResponse> => {
  return request.post({
    url: '/api/v1/users/delete',
    data: { ids: ids.map(Number) }
  })
}
