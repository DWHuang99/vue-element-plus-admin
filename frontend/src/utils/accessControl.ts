import type { ManagementPermission } from '@/api/login/types'

const resolveRoutePath = (parentPath: string, path: string): string => {
  if (/^(https?:)?\/\//.test(path)) return path
  const childPath = path.startsWith('/') || !path ? path : `/${path}`
  return `${parentPath}${childPath}`.replace(/\/\//g, '/').trim()
}

export const MANAGEMENT_PERMISSIONS: readonly ManagementPermission[] = [
  'roles.read',
  'roles.write',
  'departments.read',
  'departments.write',
  'users.read',
  'users.write'
]

const managementPermissionSet = new Set<string>(MANAGEMENT_PERMISSIONS)

export const isManagementPermission = (permission: string): permission is ManagementPermission => {
  return managementPermissionSet.has(permission)
}

export const hasEffectivePermission = (granted: readonly string[], required: string): boolean => {
  return granted.includes(required)
}

export const filterRoutesByPermissions = (
  routes: AppRouteRecordRaw[],
  granted: readonly string[],
  basePath = '/'
): AppRouteRecordRaw[] => {
  return routes.flatMap((route) => {
    const required = route.meta?.requiredPermission
    if (required && !hasEffectivePermission(granted, required)) {
      return []
    }

    const data: AppRouteRecordRaw = { ...route, meta: { ...route.meta } }
    const routePath = resolveRoutePath(basePath, route.path)

    if (route.children) {
      data.children = filterRoutesByPermissions(route.children, granted, routePath)
      if (route.children.length > 0 && data.children.length === 0) {
        return []
      }

      if (typeof data.redirect === 'string' && data.children.length > 0) {
        const redirectStillAvailable = data.children.some(
          (child) => resolveRoutePath(routePath, child.path) === data.redirect
        )
        if (!redirectStillAvailable) {
          data.redirect = resolveRoutePath(routePath, data.children[0].path)
        }
      }
    }

    return [data]
  })
}
