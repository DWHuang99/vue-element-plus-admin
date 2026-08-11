import { defineStore } from 'pinia'
import {
  alwaysAvailableRouterMap,
  authorizationRouterMap,
  constantRouterMap,
  demoRouterMap,
  resetRouter
} from '@/router'
import {
  generateRoutesByFrontEnd,
  generateRoutesByServer,
  flatMultiLevelRoutes
} from '@/utils/routerHelper'
import { filterRoutesByPermissions } from '@/utils/accessControl'
import type { ManagementPermission } from '@/api/login/types'
import { store } from '../index'
import { cloneDeep } from 'lodash-es'

export interface PermissionState {
  routers: AppRouteRecordRaw[]
  addRouters: AppRouteRecordRaw[]
  isAddRouters: boolean
  menuTabRouters: AppRouteRecordRaw[]
}

export const usePermissionStore = defineStore('permission', {
  state: (): PermissionState => ({
    routers: [],
    addRouters: [],
    isAddRouters: false,
    menuTabRouters: []
  }),
  getters: {
    getRouters(): AppRouteRecordRaw[] {
      return this.routers
    },
    getAddRouters(): AppRouteRecordRaw[] {
      return flatMultiLevelRoutes(cloneDeep(this.addRouters))
    },
    getIsAddRouters(): boolean {
      return this.isAddRouters
    },
    getMenuTabRouters(): AppRouteRecordRaw[] {
      return this.menuTabRouters
    }
  },
  actions: {
    generateRoutes(
      type: 'server' | 'frontEnd' | 'static',
      routers: AppCustomRouteRecordRaw[] | string[] = [],
      effectivePermissions: readonly ManagementPermission[] = []
    ): Promise<void> {
      return new Promise<void>((resolve) => {
        let demoRoutes: AppRouteRecordRaw[] = []
        if (type === 'server') {
          demoRoutes = generateRoutesByServer(routers as AppCustomRouteRecordRaw[]).filter(
            (route) => route.path === '/demo'
          )
        } else if (type === 'frontEnd') {
          demoRoutes = generateRoutesByFrontEnd(cloneDeep(demoRouterMap), routers as string[])
        } else {
          demoRoutes = cloneDeep(demoRouterMap)
        }

        const businessRoutes = filterRoutesByPermissions(
          cloneDeep(authorizationRouterMap),
          effectivePermissions
        )
        const routerMap = demoRoutes
          .concat(cloneDeep(alwaysAvailableRouterMap))
          .concat(businessRoutes)

        this.addRouters = routerMap.concat([
          {
            path: '/:path(.*)*',
            redirect: '/404',
            name: '404Page',
            meta: {
              hidden: true,
              breadcrumb: false
            }
          }
        ])
        this.routers = cloneDeep(constantRouterMap).concat(routerMap)
        resolve()
      })
    },
    resetRoutes(): void {
      resetRouter()
      this.routers = []
      this.addRouters = []
      this.isAddRouters = false
      this.menuTabRouters = []
    },
    setIsAddRouters(state: boolean): void {
      this.isAddRouters = state
    },
    setMenuTabRouters(routers: AppRouteRecordRaw[]): void {
      this.menuTabRouters = routers
    }
  }
})

export const usePermissionStoreWithOut = () => {
  return usePermissionStore(store)
}
