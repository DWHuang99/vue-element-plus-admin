import { defineStore } from 'pinia'
import { store } from '../index'
import type { AuthUser, ManagementPermission, UserLoginType } from '@/api/login/types'
import { ElMessageBox } from 'element-plus'
import { useI18n } from '@/hooks/web/useI18n'
import { loginOutApi, loginApi, registerApi, getUserInfoApi } from '@/api/login'
import { useTagsViewStore } from './tagsView'
import { usePermissionStoreWithOut } from './permission'
import router from '@/router'

let authorizationRefreshPromise: Promise<boolean> | undefined

interface UserState {
  userInfo?: AuthUser
  profileHydrated: boolean
  tokenKey: string
  token: string
  roleRouters?: string[] | AppCustomRouteRecordRaw[]
  rememberMe: boolean
  loginInfo?: string
}

export const useUserStore = defineStore('user', {
  state: (): UserState => {
    return {
      userInfo: undefined,
      profileHydrated: false,
      tokenKey: 'Authorization',
      token: '',
      roleRouters: undefined,
      rememberMe: true,
      loginInfo: undefined
    }
  },
  getters: {
    getTokenKey(): string {
      return this.tokenKey
    },
    getToken(): string {
      return this.token
    },
    getUserInfo(): AuthUser | undefined {
      return this.userInfo
    },
    getProfileHydrated(): boolean {
      return this.profileHydrated
    },
    getEffectivePermissions(): ManagementPermission[] {
      return this.userInfo?.effective_permissions || []
    },
    getRoleRouters(): string[] | AppCustomRouteRecordRaw[] | undefined {
      return this.roleRouters
    },
    getRememberMe(): boolean {
      return this.rememberMe
    },
    getLoginInfo(): string | undefined {
      return this.loginInfo
    }
  },
  actions: {
    setTokenKey(tokenKey: string) {
      this.tokenKey = tokenKey
    },
    setToken(token: string) {
      this.token = token
    },
    setUserInfo(userInfo?: AuthUser) {
      this.userInfo = userInfo
    },
    setRoleRouters(roleRouters?: string[] | AppCustomRouteRecordRaw[]) {
      this.roleRouters = roleRouters
    },
    clearAuthorizationState() {
      const tagsViewStore = useTagsViewStore()
      const permissionStore = usePermissionStoreWithOut()
      tagsViewStore.delAllViews()
      permissionStore.resetRoutes()
      this.setUserInfo(undefined)
      this.profileHydrated = false
      this.setRoleRouters(undefined)
    },
    async applyAuthToken(token?: string): Promise<boolean> {
      if (!token) return false

      this.clearAuthorizationState()
      this.setToken(token)
      try {
        const hydrated = await this.fetchUserInfo()
        if (!hydrated) {
          if (this.token) await loginOutApi().catch(() => {})
          this.reset(false)
        }
        return hydrated
      } catch (error) {
        // 登录已创建服务端会话时尽量撤销，避免 /auth/me 临时失败留下孤立会话。
        if (this.token) await loginOutApi().catch(() => {})
        this.reset(false)
        throw error
      }
    },
    /** 登录后以 /auth/me 作为完整用户和权限信息的唯一来源。 */
    async login(formData: UserLoginType): Promise<boolean> {
      const res = await loginApi(formData)
      return this.applyAuthToken(res?.data?.token)
    },
    /** 注册即登录，并立即加载完整用户和权限信息。 */
    async register(formData: UserLoginType): Promise<boolean> {
      const res = await registerApi(formData)
      return this.applyAuthToken(res?.data?.token)
    },
    /** 用 /auth/me 验证令牌并恢复完整用户与有效权限。 */
    async fetchUserInfo(): Promise<boolean> {
      this.profileHydrated = false
      const res = await getUserInfoApi()
      if (res?.data?.user) {
        this.setUserInfo(res.data.user)
        this.profileHydrated = true
        return true
      }
      return false
    },
    /** 403 后重新同步权限，并让全局守卫按最新权限重建动态路由。 */
    refreshAuthorization(): Promise<boolean> {
      if (authorizationRefreshPromise) return authorizationRefreshPromise

      authorizationRefreshPromise = (async () => {
        const hydrated = await this.fetchUserInfo()
        if (!hydrated) return false

        usePermissionStoreWithOut().resetRoutes()
        await router.replace(router.currentRoute.value.fullPath)
        return true
      })().finally(() => {
        authorizationRefreshPromise = undefined
      })

      return authorizationRefreshPromise
    },
    logoutConfirm() {
      const { t } = useI18n()
      ElMessageBox.confirm(t('common.loginOutMessage'), t('common.reminder'), {
        confirmButtonText: t('common.ok'),
        cancelButtonText: t('common.cancel'),
        type: 'warning'
      })
        .then(async () => {
          await this.logout()
        })
        .catch(() => {})
    },
    reset(redirectToLogin = true) {
      this.clearAuthorizationState()
      this.setToken('')
      if (redirectToLogin && router.currentRoute.value.path !== '/login') {
        router.replace('/login')
      }
    },
    /** 退出：调用真实后端撤销会话（失败也本地登出），随后清空本地状态。 */
    async logout() {
      try {
        await loginOutApi()
      } catch (e) {
        console.warn('logout api failed, clearing local state', e)
      }
      this.reset()
    },
    setRememberMe(rememberMe: boolean) {
      this.rememberMe = rememberMe
    },
    setLoginInfo(loginInfo: string | undefined) {
      this.loginInfo = loginInfo
    }
  },
  persist: [
    {
      pick: ['tokenKey', 'token', 'rememberMe', 'loginInfo'],
      storage: localStorage
    }
  ]
})

export const useUserStoreWithOut = () => {
  return useUserStore(store)
}
