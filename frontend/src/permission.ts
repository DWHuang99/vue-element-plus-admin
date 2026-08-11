import router from './router'
import { useAppStoreWithOut } from '@/store/modules/app'
import type { RouteRecordRaw } from 'vue-router'
import { useTitle } from '@/hooks/web/useTitle'
import { useNProgress } from '@/hooks/web/useNProgress'
import { usePermissionStoreWithOut } from '@/store/modules/permission'
import { usePageLoading } from '@/hooks/web/usePageLoading'
import { NO_REDIRECT_WHITE_LIST } from '@/constants'
import { useUserStoreWithOut } from '@/store/modules/user'
import { getAdminRoleApi, getTestRoleApi } from '@/api/login'

const { start, done } = useNProgress()
const { loadStart, loadDone } = usePageLoading()

router.beforeEach(async (to, from, next) => {
  start()
  loadStart()
  const permissionStore = usePermissionStoreWithOut()
  const appStore = useAppStoreWithOut()
  const userStore = useUserStoreWithOut()

  if (userStore.getToken && !userStore.getProfileHydrated) {
    try {
      const ok = await userStore.fetchUserInfo()
      if (!ok) {
        userStore.reset(false)
      }
    } catch (_error) {
      userStore.reset(false)
    }
  }

  if (userStore.getUserInfo && userStore.getProfileHydrated) {
    if (to.path === '/login') {
      next({ path: '/' })
      return
    }

    if (permissionStore.getIsAddRouters) {
      next()
      return
    }

    let routeType: 'server' | 'frontEnd' | 'static' = 'static'
    let roleRouters: string[] | AppCustomRouteRecordRaw[] = []

    if (appStore.getDynamicRouter) {
      const params = { roleName: userStore.getUserInfo.username }
      try {
        if (appStore.getServerDynamicRouter) {
          routeType = 'server'
          const res = await getAdminRoleApi(params)
          roleRouters = res?.data || []
        } else {
          routeType = 'frontEnd'
          const res = await getTestRoleApi(params)
          roleRouters = res?.data || []
        }
        userStore.setRoleRouters(roleRouters)
      } catch (_error) {
        // Demo 路由 Mock 不可用时回退静态 Demo；真实业务权限仍由 /auth/me 决定。
        routeType = 'static'
        roleRouters = []
        userStore.setRoleRouters(undefined)
      }
    }

    await permissionStore.generateRoutes(routeType, roleRouters, userStore.getEffectivePermissions)

    permissionStore.getAddRouters.forEach((route) => {
      router.addRoute(route as unknown as RouteRecordRaw)
    })

    const redirectPath = from.query.redirect || to.path
    const redirect = decodeURIComponent(redirectPath as string)
    const nextData = to.path === redirect ? { ...to, replace: true } : { path: redirect }
    permissionStore.setIsAddRouters(true)
    next(nextData)
    return
  }

  if (NO_REDIRECT_WHITE_LIST.indexOf(to.path) !== -1) {
    next()
  } else {
    next(`/login?redirect=${to.path}`)
  }
})

router.afterEach((to) => {
  useTitle(to?.meta?.title as string)
  done()
  loadDone()
})
