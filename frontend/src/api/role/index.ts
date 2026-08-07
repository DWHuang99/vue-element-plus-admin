import request from '@/axios'
import type { RoleListItem, RoleListResponse } from './types'

/** 角色列表（真实后端）。响应 name/created_at → roleName/createTime，id → 字符串。 */
export const getRoleListApi = async (): Promise<IResponse<RoleListResponse>> => {
  const res = await request.get<{ list: any[]; total: number }>({ url: '/api/v1/roles' })
  const list: RoleListItem[] = (res.data?.list || []).map((r) => ({
    id: String(r.id),
    roleName: r.name,
    code: r.code,
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
