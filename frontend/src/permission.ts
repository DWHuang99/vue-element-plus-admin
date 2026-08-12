import router from './router'
import { useAppStoreWithOut } from '@/store/modules/app'
import type { RouteRecordRaw } from 'vue-router'
import { useTitle } from '@/hooks/web/useTitle'
import { useNProgress } from '@/hooks/web/useNProgress'
import { usePermissionStoreWithOut } from '@/store/modules/permission'
import { usePageLoading } from '@/hooks/web/usePageLoading'
import { NO_REDIRECT_WHITE_LIST } from '@/constants'
import { useUserStoreWithOut } from '@/store/modules/user'
import { getAdminRoleApi, getCurrentUserApi, getTestRoleApi } from '@/api/login'

const { start, done } = useNProgress()

const { loadStart, loadDone } = usePageLoading()

router.beforeEach(async (to, from, next) => {
  start()
  loadStart()
  const permissionStore = usePermissionStoreWithOut()
  const appStore = useAppStoreWithOut()
  const userStore = useUserStoreWithOut()

  if (!userStore.getUserInfo && userStore.getToken) {
    try {
      const currentUser = await getCurrentUserApi()
      userStore.setUserInfo(currentUser.data)
    } catch {
      userStore.setToken('')
      userStore.setUserInfo(undefined)
      userStore.setRoleRouters([])
    }
  }

  if (userStore.getUserInfo) {
    if (to.path === '/login') {
      next({ path: '/' })
    } else {
      if (permissionStore.getIsAddRouters) {
        next()
        return
      }

      // 开发者可根据实际情况进行修改
      let roleRouters = userStore.getRoleRouters || []

      // 浏览器保留了登录信息，但没有角色路由时重新获取，避免首页进入 404。
      if (appStore.getDynamicRouter && roleRouters.length === 0) {
        const params = { roleName: userStore.getUserInfo.username }
        const res = appStore.getServerDynamicRouter
          ? await getAdminRoleApi(params)
          : await getTestRoleApi(params)
        roleRouters = res.data || []
        userStore.setRoleRouters(roleRouters)
      }

      // 是否使用动态路由
      if (appStore.getDynamicRouter) {
        appStore.getServerDynamicRouter
          ? await permissionStore.generateRoutes('server', roleRouters as AppCustomRouteRecordRaw[])
          : await permissionStore.generateRoutes('frontEnd', roleRouters as string[])
      } else {
        await permissionStore.generateRoutes('static')
      }

      permissionStore.getAddRouters.forEach((route) => {
        router.addRoute(route as unknown as RouteRecordRaw) // 动态添加可访问路由表
      })
      const redirectPath = from.query.redirect || to.path
      const redirect = decodeURIComponent(redirectPath as string)
      const nextData = to.path === redirect ? { ...to, replace: true } : { path: redirect }
      permissionStore.setIsAddRouters(true)
      next(nextData)
    }
  } else {
    if (NO_REDIRECT_WHITE_LIST.indexOf(to.path) !== -1) {
      next()
    } else {
      next(`/login?redirect=${to.path}`) // 否则全部重定向到登录页
    }
  }
})

router.afterEach((to) => {
  useTitle(to?.meta?.title as string)
  done() // 结束Progress
  loadDone()
})
