export interface UserLoginType {
  username: string
  password: string
}

export type ManagementPermission =
  | 'roles.read'
  | 'roles.write'
  | 'departments.read'
  | 'departments.write'
  | 'users.read'
  | 'users.write'

export interface DepartmentProfile {
  id: number
  name: string
}

export interface RoleProfile {
  id: number
  name: string
  code: string
}

/** Backend user profile returned by GET /api/v1/auth/me. */
export interface AuthUser {
  id: number
  username: string
  account: string
  email: string
  created_at: string
  department: DepartmentProfile | null
  roles: RoleProfile[]
  effective_permissions: ManagementPermission[]
}

export interface AuthSessionUser {
  id: number
  username: string
  created_at: string
}

/** Backend auth success payload returned by register/login. */
export interface AuthResult {
  token: string
  token_type: string
  expires_in: number
  user: AuthSessionUser
}

/**
 * Mock user shape retained for role/permission mock APIs.
 * Only used by the (unchanged) mock role endpoints.
 */
export interface UserType {
  username: string
  password: string
  role: string
  roleId: string
}
