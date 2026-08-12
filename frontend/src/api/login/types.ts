export interface UserLoginType {
  username: string
  password: string
}

export interface UserType {
  id: number
  username: string
  roleId: number
  roleCode: string
  roleName: string
  permissions: string[]
  isActive: boolean
  createdAt: string
  updatedAt: string
}

export interface LoginResponse {
  accessToken: string
  exist: boolean
  message: string
}

export interface RefreshResponse {
  accessToken: string
}

export interface RegisterType {
  username: string
  password: string
  check_password: string
  code: string
  iAgree: boolean
}
