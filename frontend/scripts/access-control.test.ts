import assert from 'node:assert/strict'
import { filterRoutesByPermissions, hasEffectivePermission } from '../src/utils/accessControl'

const authorizationRoutes = [
  {
    path: '/authorization',
    name: 'Authorization',
    redirect: '/authorization/user',
    meta: {},
    children: [
      {
        path: 'department',
        name: 'Department',
        meta: { requiredPermission: 'departments.read' }
      },
      {
        path: 'user',
        name: 'User',
        meta: { requiredPermission: 'users.read' }
      },
      {
        path: 'role',
        name: 'Role',
        meta: { requiredPermission: 'roles.read' }
      }
    ]
  }
] as AppRouteRecordRaw[]

assert.equal(hasEffectivePermission(['users.read'], 'users.read'), true)
assert.equal(hasEffectivePermission([], 'users.read'), false)

const userOnly = filterRoutesByPermissions(authorizationRoutes, ['users.read'])
assert.equal(userOnly.length, 1)
assert.deepEqual(
  userOnly[0].children?.map((route) => route.name),
  ['User']
)
assert.equal(userOnly[0].redirect, '/authorization/user')

const roleOnly = filterRoutesByPermissions(authorizationRoutes, ['roles.read'])
assert.deepEqual(
  roleOnly[0].children?.map((route) => route.name),
  ['Role']
)
assert.equal(roleOnly[0].redirect, '/authorization/role')

assert.deepEqual(filterRoutesByPermissions(authorizationRoutes, []), [])
assert.equal(authorizationRoutes[0].children?.length, 3)

const demoRoutes = [
  {
    path: '/demo',
    name: 'Demo',
    meta: {},
    children: [{ path: 'dashboard', name: 'Dashboard', meta: {} }]
  }
] as AppRouteRecordRaw[]
assert.equal(filterRoutesByPermissions(demoRoutes, []).length, 1)

console.log('access-control tests passed')
