import { defineStore } from 'pinia'
import { store } from '../index'
import { AuthUser, UserLoginType } from '@/api/login/types'
import { ElMessageBox } from 'element-plus'
import { useI18n } from '@/hooks/web/useI18n'
import { loginOutApi, loginApi, registerApi, getUserInfoApi } from '@/api/login'
import { useTagsViewStore } from './tagsView'
import router from '@/router'

interface UserState {
  userInfo?: AuthUser
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
      tokenKey: 'Authorization',
      token: '',
      roleRouters: undefined,
      // 记住我
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
    setRoleRouters(roleRouters: string[] | AppCustomRouteRecordRaw[]) {
      this.roleRouters = roleRouters
    },
    /** 登录：调用真实后端，成功后保存令牌与用户信息。 */
    async login(formData: UserLoginType): Promise<boolean> {
      const res = await loginApi(formData)
      this.applyAuthResult(res?.data)
      return !!res?.data?.token
    },
    /** 注册：调用真实后端，注册即登录。 */
    async register(formData: UserLoginType): Promise<boolean> {
      const res = await registerApi(formData)
      this.applyAuthResult(res?.data)
      return !!res?.data?.token
    },
    /** 用 /auth/me 验证持久化令牌有效性并恢复用户信息。 */
    async fetchUserInfo(): Promise<boolean> {
      const res = await getUserInfoApi()
      if (res?.data?.user) {
        this.setUserInfo(res.data.user)
        return true
      }
      return false
    },
    applyAuthResult(result?: { token: string; user: AuthUser }) {
      if (result?.token) {
        this.setToken(result.token)
        this.setUserInfo(result.user)
      }
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
    reset() {
      const tagsViewStore = useTagsViewStore()
      tagsViewStore.delAllViews()
      this.setToken('')
      this.setUserInfo(undefined)
      this.setRoleRouters([])
      router.replace('/login')
    },
    /** 退出：调用真实后端撤销会话（失败也本地登出），随后清空本地状态。 */
    async logout() {
      try {
        await loginOutApi()
      } catch (e) {
        // 会话可能已失效（401），忽略后照常本地登出。
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
  persist: true
})

export const useUserStoreWithOut = () => {
  return useUserStore(store)
}
