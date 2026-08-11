import request from '@/axios'
import type { RoleListItem, RoleListResponse } from './types'

/** 角色列表（真实后端）。响应 name/created_at → roleName/createTime，id → 字符串。 */
export const getRoleListApi = async (): Promise<IResponse<RoleListResponse>> => {
  const res = await request.get<{ list: any[]; total: number }>({ url: '/api/v1/roles' })
  const list: RoleListItem[] = (res.data?.list || []).map((r) => ({
    id: String(r.id),
    roleName: r.name,
    code: r.code,
    isBuiltin: Boolean(r.is_builtin),
    createTime: r.created_at
  }))
  return {
    code: 0,
    data: {
      list,
      total: res.data?.total || 0
    }
  }
}

/** 保存角色（POST /api/v1/roles）。表单 roleName/code → name/code；id 用于编辑。 */
export const saveRoleApi = (data: any): Promise<IResponse> => {
  return request.post({
    url: '/api/v1/roles',
    data: {
      id: data.id ? Number(data.id) : undefined,
      name: data.roleName,
      code: data.code
    }
  })
}

/** 删除角色（POST /api/v1/roles/delete）。 */
export const deleteRoleApi = (ids: string[] | number[]): Promise<IResponse> => {
  return request.post({
    url: '/api/v1/roles/delete',
    data: { ids: ids.map(Number) }
  })
}
