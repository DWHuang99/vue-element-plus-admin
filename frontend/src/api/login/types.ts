export interface UserLoginType {
  username: string
  password: string
}

/** Backend user payload (contracts/auth-api.md). */
export interface AuthUser {
  id: number
  username: string
  created_at: string
}

/** Backend auth success payload returned by register/login. */
export interface AuthResult {
  token: string
  token_type: string
  expires_in: number
  user: AuthUser
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
