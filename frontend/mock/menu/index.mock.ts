import Mock from 'mockjs'
import { SUCCESS_CODE } from '@/constants'

const timeout = 1000

export default [
  // 列表接口
  {
    url: '/mock/menu/list',
    method: 'get',
    timeout,
    response: () => {
      return {
        code: SUCCESS_CODE,
        data: {
          list: [
            {
              path: '/demo',
              component: '#',
              redirect: '/demo/dashboard/analysis',
              name: 'Demo',
              status: Mock.Random.integer(0, 1),
              id: 1,
              type: 0,
              parentId: undefined,
              title: 'Demo',
              meta: {
                title: 'Demo',
                icon: 'vi-ep:menu',
                alwaysShow: true
              },
              children: [
                {
                  path: 'dashboard',
                  component: '##',
                  redirect: '/demo/dashboard/analysis',
                  name: 'Dashboard',
                  status: Mock.Random.integer(0, 1),
                  id: 2,
                  type: 0,
                  parentId: 1,
                  title: '首页',
                  meta: {
                    title: '首页',
                    icon: 'vi-ant-design:dashboard-filled',
                    alwaysShow: true
                  },
                  children: [
                    {
                      path: 'analysis',
                      component: 'views/Dashboard/Analysis',
                      name: 'Analysis',
                      status: Mock.Random.integer(0, 1),
                      id: 3,
                      type: 1,
                      parentId: 2,
                      title: '分析页',
                      permissionList: [
                        {
                          id: 1,
                          label: '新增',
                          value: 'add'
                        },
                        {
                          id: 2,
                          label: '编辑',
                          value: 'edit'
                        }
                      ],
                      meta: {
                        title: '分析页',
                        noCache: true,
                        permission: ['add', 'edit']
                      }
                    },
                    {
                      path: 'workplace',
                      component: 'views/Dashboard/Workplace',
                      name: 'Workplace',
                      status: Mock.Random.integer(0, 1),
                      id: 4,
                      type: 1,
                      parentId: 2,
                      title: '工作台',
                      permissionList: [
                        {
                          id: 1,
                          label: '新增',
                          value: 'add'
                        },
                        {
                          id: 2,
                          label: '编辑',
                          value: 'edit'
                        },
                        {
                          id: 3,
                          label: '删除',
                          value: 'delete'
                        }
                      ],
                      meta: {
                        title: '工作台',
                        noCache: true
                      }
                    }
                  ]
                },
                {
                  path: 'level',
                  component: '##',
                  redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
                  name: 'Level',
                  status: Mock.Random.integer(0, 1),
                  id: 5,
                  type: 0,
                  parentId: 1,
                  title: '菜单',
                  meta: {
                    title: '菜单',
                    icon: 'vi-carbon:skill-level-advanced'
                  },
                  children: [
                    {
                      path: 'menu1',
                      name: 'Menu1',
                      component: '##',
                      status: Mock.Random.integer(0, 1),
                      id: 6,
                      type: 0,
                      parentId: 5,
                      title: '菜单1',
                      redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
                      meta: {
                        title: '菜单1'
                      },
                      children: [
                        {
                          path: 'menu1-1',
                          name: 'Menu11',
                          component: '##',
                          status: Mock.Random.integer(0, 1),
                          id: 7,
                          type: 0,
                          parentId: 6,
                          title: '菜单1-1',
                          redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
                          meta: {
                            title: '菜单1-1',
                            alwaysShow: true
                          },
                          children: [
                            {
                              path: 'menu1-1-1',
                              name: 'Menu111',
                              component: 'views/Level/Menu111',
                              status: Mock.Random.integer(0, 1),
                              id: 8,
                              type: 1,
                              parentId: 7,
                              title: '菜单1-1-1',
                              meta: {
                                title: '菜单1-1-1'
                              }
                            }
                          ]
                        },
                        {
                          path: 'menu1-2',
                          name: 'Menu12',
                          component: 'views/Level/Menu12',
                          status: Mock.Random.integer(0, 1),
                          id: 9,
                          type: 1,
                          parentId: 6,
                          title: '菜单1-2',
                          meta: {
                            title: '菜单1-2'
                          }
                        }
                      ]
                    },
                    {
                      path: 'menu2',
                      name: 'Menu2Demo',
                      component: 'views/Level/Menu2',
                      status: Mock.Random.integer(0, 1),
                      id: 10,
                      type: 1,
                      parentId: 5,
                      title: '菜单2',
                      meta: {
                        title: '菜单2'
                      }
                    }
                  ]
                },
                {
                  path: 'example',
                  component: '##',
                  redirect: '/demo/example/example-dialog',
                  name: 'Example',
                  status: Mock.Random.integer(0, 1),
                  id: 11,
                  type: 0,
                  parentId: 1,
                  title: '综合示例',
                  meta: {
                    title: '综合示例',
                    icon: 'vi-ep:management',
                    alwaysShow: true
                  },
                  children: [
                    {
                      path: 'example-dialog',
                      component: 'views/Example/Dialog/ExampleDialog',
                      name: 'ExampleDialog',
                      status: Mock.Random.integer(0, 1),
                      id: 12,
                      type: 1,
                      parentId: 11,
                      title: '综合示例-弹窗',
                      permissionList: [
                        {
                          id: 1,
                          label: '新增',
                          value: 'add'
                        },
                        {
                          id: 2,
                          label: '编辑',
                          value: 'edit'
                        },
                        {
                          id: 3,
                          label: '删除',
                          value: 'delete'
                        },
                        {
                          id: 4,
                          label: '查看',
                          value: 'view'
                        }
                      ],
                      meta: {
                        title: '综合示例-弹窗'
                      }
                    },
                    {
                      path: 'example-page',
                      component: 'views/Example/Page/ExamplePage',
                      name: 'ExamplePage',
                      status: Mock.Random.integer(0, 1),
                      id: 13,
                      type: 1,
                      parentId: 11,
                      title: '综合示例-页面',
                      permissionList: [
                        {
                          id: 1,
                          label: '新增',
                          value: 'add'
                        },
                        {
                          id: 2,
                          label: '编辑',
                          value: 'edit'
                        },
                        {
                          id: 3,
                          label: '删除',
                          value: 'delete'
                        },
                        {
                          id: 4,
                          label: '查看',
                          value: 'view'
                        }
                      ],
                      meta: {
                        title: '综合示例-页面'
                      }
                    },
                    {
                      path: 'example-add',
                      component: 'views/Example/Page/ExampleAdd',
                      name: 'ExampleAdd',
                      status: Mock.Random.integer(0, 1),
                      id: 14,
                      type: 1,
                      parentId: 11,
                      title: '综合示例-新增',
                      meta: {
                        title: '综合示例-新增',
                        noTagsView: true,
                        noCache: true,
                        hidden: true,
                        showMainRoute: true,
                        activeMenu: '/demo/example/example-page'
                      }
                    },
                    {
                      path: 'example-edit',
                      component: 'views/Example/Page/ExampleEdit',
                      name: 'ExampleEdit',
                      status: Mock.Random.integer(0, 1),
                      id: 15,
                      type: 1,
                      parentId: 11,
                      title: '综合示例-编辑',
                      meta: {
                        title: '综合示例-编辑',
                        noTagsView: true,
                        noCache: true,
                        hidden: true,
                        showMainRoute: true,
                        activeMenu: '/demo/example/example-page'
                      }
                    },
                    {
                      path: 'example-detail',
                      component: 'views/Example/Page/ExampleDetail',
                      name: 'ExampleDetail',
                      status: Mock.Random.integer(0, 1),
                      id: 16,
                      type: 1,
                      parentId: 11,
                      title: '综合示例-详情',
                      meta: {
                        title: '综合示例-详情',
                        noTagsView: true,
                        noCache: true,
                        hidden: true,
                        showMainRoute: true,
                        activeMenu: '/demo/example/example-page'
                      }
                    }
                  ]
                },
                {
                  path: 'authorization',
                  component: '##',
                  redirect: '/demo/authorization/menu',
                  name: 'DemoAuthorization',
                  status: Mock.Random.integer(0, 1),
                  id: 17,
                  type: 0,
                  parentId: 1,
                  title: '权限认证',
                  meta: {
                    title: '权限认证',
                    icon: 'vi-eos-icons:role-binding',
                    alwaysShow: true
                  },
                  children: [
                    {
                      path: 'menu',
                      component: 'views/Authorization/Menu/Menu',
                      name: 'Menu',
                      status: Mock.Random.integer(0, 1),
                      id: 18,
                      type: 1,
                      parentId: 17,
                      title: 'Mock 菜单管理',
                      meta: {
                        title: 'Mock 菜单管理'
                      }
                    },
                    {
                      path: 'permission',
                      component: 'views/Function/Test',
                      name: 'Test',
                      status: Mock.Random.integer(0, 1),
                      id: 19,
                      type: 1,
                      parentId: 17,
                      title: '按钮权限测试',
                      permissionList: [
                        {
                          id: 1,
                          label: '新增',
                          value: 'add'
                        },
                        {
                          id: 2,
                          label: '编辑',
                          value: 'edit'
                        },
                        {
                          id: 3,
                          label: '删除',
                          value: 'delete'
                        }
                      ],
                      meta: {
                        title: '按钮权限测试',
                        permission: ['add', 'edit', 'delete']
                      }
                    }
                  ]
                }
              ]
            },
            {
              path: '/external-link',
              component: '#',
              meta: {
                title: '文档',
                icon: 'vi-clarity:document-solid'
              },
              name: 'ExternalLink',
              status: Mock.Random.integer(0, 1),
              id: 20,
              type: 0,
              parentId: undefined,
              title: '文档',
              children: [
                {
                  path: 'https://element-plus-admin-doc.cn/',
                  name: 'DocumentLink',
                  status: Mock.Random.integer(0, 1),
                  id: 21,
                  type: 1,
                  parentId: 20,
                  title: '文档',
                  meta: {
                    title: '文档'
                  }
                }
              ]
            }
          ]
        }
      }
    }
  }
]
