export interface DepartmentItem {
  id: string | number
  parentId?: string | number
  departmentName: string
  status?: number
  createTime?: string
  remark?: string
  children?: DepartmentItem[]
}

export interface DepartmentListResponse {
  list: DepartmentItem[]
}
