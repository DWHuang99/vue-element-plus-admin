/** 角色列表项（真实后端响应 name/created_at 已映射为 roleName/createTime）。 */
export interface RoleListItem {
  id: string
  roleName: string
  code: string
  isBuiltin: boolean
  createTime: string
}

export interface RoleListResponse {
  list: RoleListItem[]
  total: number
}
