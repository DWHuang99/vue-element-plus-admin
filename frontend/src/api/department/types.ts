/** 部门树节点（真实后端响应 name→departmentName 已映射；id 为字符串以匹配模板）。
 * parentId 为上级部门 id（根部门为 undefined），编辑时用于回填上级选择。 */
export interface DepartmentItem {
  id: string
  departmentName: string
  parentId?: string
  children?: DepartmentItem[]
}

export interface DepartmentListResponse {
  list: DepartmentItem[]
}

/** 用户列表查询参数：id 即 department_id（后端参数名在 index.ts 中映射）。 */
export interface DepartmentUserParams {
  pageSize: number
  pageIndex: number
  id: string
  username?: string
  account?: string
}

/** 用户列表项（真实后端响应 create_time/role/department.name 已映射）。 */
export interface DepartmentUserItem {
  id: string
  username: string
  account: string
  email: string
  createTime: string
  role: string
  department: DepartmentItem
}

export interface DepartmentUserResponse {
  list: DepartmentUserItem[]
  total: number
}
