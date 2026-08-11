import Mock from 'mockjs'
import { SUCCESS_CODE } from '@/constants'
import { toAnyString } from '@/utils'

const timeout = 1000

const adminList = [
  {
    path: '/demo',
    component: '#',
    redirect: '/demo/dashboard/analysis',
    name: 'Demo',
    meta: {
      title: 'router.demo',
      icon: 'vi-ep:menu',
      alwaysShow: true
    },
    children: [
      {
        path: 'dashboard',
        component: '##',
        redirect: '/demo/dashboard/analysis',
        name: 'Dashboard',
        meta: {
          title: 'router.dashboard',
          icon: 'vi-ant-design:dashboard-filled',
          alwaysShow: true
        },
        children: [
          {
            path: 'analysis',
            component: 'views/Dashboard/Analysis',
            name: 'Analysis',
            meta: {
              title: 'router.analysis',
              noCache: true,
              affix: true
            }
          },
          {
            path: 'workplace',
            component: 'views/Dashboard/Workplace',
            name: 'Workplace',
            meta: {
              title: 'router.workplace',
              noCache: true,
              affix: true
            }
          }
        ]
      },
      {
        path: 'guide',
        component: '##',
        name: 'Guide',
        meta: {},
        children: [
          {
            path: 'index',
            component: 'views/Guide/Guide',
            name: 'GuideDemo',
            meta: {
              title: 'router.guide',
              icon: 'vi-cib:telegram-plane'
            }
          }
        ]
      },
      {
        path: 'components',
        component: '##',
        name: 'ComponentsDemo',
        meta: {
          title: 'router.component',
          icon: 'vi-bx:bxs-component',
          alwaysShow: true
        },
        children: [
          {
            path: 'form',
            component: '##',
            name: 'Form',
            meta: {
              title: 'router.form',
              alwaysShow: true
            },
            children: [
              {
                path: 'default-form',
                component: 'views/Components/Form/DefaultForm',
                name: 'DefaultForm',
                meta: {
                  title: 'router.defaultForm'
                }
              },
              {
                path: 'use-form',
                component: 'views/Components/Form/UseFormDemo',
                name: 'UseForm',
                meta: {
                  title: 'UseForm'
                }
              }
            ]
          },
          {
            path: 'table',
            component: '##',
            redirect: '/demo/components/table/default-table',
            name: 'TableDemo',
            meta: {
              title: 'router.table',
              alwaysShow: true
            },
            children: [
              {
                path: 'default-table',
                component: 'views/Components/Table/DefaultTable',
                name: 'DefaultTable',
                meta: {
                  title: 'router.defaultTable'
                }
              },
              {
                path: 'use-table',
                component: 'views/Components/Table/UseTableDemo',
                name: 'UseTable',
                meta: {
                  title: 'UseTable'
                }
              },
              {
                path: 'tree-table',
                component: 'views/Components/Table/TreeTable',
                name: 'TreeTable',
                meta: {
                  title: 'TreeTable'
                }
              },
              {
                path: 'table-image-preview',
                component: 'views/Components/Table/TableImagePreview',
                name: 'TableImagePreview',
                meta: {
                  title: 'router.PicturePreview'
                }
              },
              {
                path: 'table-video-preview',
                component: 'views/Components/Table/TableVideoPreview',
                name: 'TableVideoPreview',
                meta: {
                  title: 'router.tableVideoPreview'
                }
              },
              {
                path: 'card-table',
                component: 'views/Components/Table/CardTable',
                name: 'CardTable',
                meta: {
                  title: 'router.cardTable'
                }
              }
              // {
              //   path: 'ref-table',
              //   component: 'views/Components/Table/RefTable',
              //   name: 'RefTable',
              //   meta: {
              //     title: 'RefTable'
              //   }
              // }
            ]
          },
          {
            path: 'editor-demo',
            component: '##',
            redirect: '/demo/components/editor-demo/editor',
            name: 'EditorDemo',
            meta: {
              title: 'router.editor',
              alwaysShow: true
            },
            children: [
              {
                path: 'editor',
                component: 'views/Components/Editor/Editor',
                name: 'Editor',
                meta: {
                  title: 'router.richText'
                }
              },
              {
                path: 'json-editor',
                component: 'views/Components/Editor/JsonEditor',
                name: 'JsonEditor',
                meta: {
                  title: 'router.jsonEditor'
                }
              },
              {
                path: 'code-editor',
                component: 'views/Components/Editor/CodeEditor',
                name: 'CodeEditor',
                meta: {
                  title: 'router.codeEditor'
                }
              }
            ]
          },
          {
            path: 'search',
            component: 'views/Components/Search',
            name: 'Search',
            meta: {
              title: 'router.search'
            }
          },
          {
            path: 'descriptions',
            component: 'views/Components/Descriptions',
            name: 'Descriptions',
            meta: {
              title: 'router.descriptions'
            }
          },
          {
            path: 'image-viewer',
            component: 'views/Components/ImageViewer',
            name: 'ImageViewer',
            meta: {
              title: 'router.imageViewer'
            }
          },
          {
            path: 'dialog',
            component: 'views/Components/Dialog',
            name: 'Dialog',
            meta: {
              title: 'router.dialog'
            }
          },
          {
            path: 'icon',
            component: 'views/Components/Icon',
            name: 'Icon',
            meta: {
              title: 'router.icon'
            }
          },
          {
            path: 'icon-picker',
            component: 'views/Components/IconPicker',
            name: 'IconPicker',
            meta: {
              title: 'router.iconPicker'
            }
          },
          {
            path: 'echart',
            component: 'views/Components/Echart',
            name: 'Echart',
            meta: {
              title: 'router.echart'
            }
          },
          {
            path: 'count-to',
            component: 'views/Components/CountTo',
            name: 'CountTo',
            meta: {
              title: 'router.countTo'
            }
          },
          {
            path: 'qrcode',
            component: 'views/Components/Qrcode',
            name: 'Qrcode',
            meta: {
              title: 'router.qrcode'
            }
          },
          {
            path: 'highlight',
            component: 'views/Components/Highlight',
            name: 'Highlight',
            meta: {
              title: 'router.highlight'
            }
          },
          {
            path: 'infotip',
            component: 'views/Components/Infotip',
            name: 'Infotip',
            meta: {
              title: 'router.infotip'
            }
          },
          {
            path: 'input-password',
            component: 'views/Components/InputPassword',
            name: 'InputPassword',
            meta: {
              title: 'router.inputPassword'
            }
          },
          {
            path: 'waterfall',
            component: 'views/Components/Waterfall',
            name: 'Waterfall',
            meta: {
              title: 'router.waterfall'
            }
          },
          {
            path: 'image-cropping',
            component: 'views/Components/ImageCropping',
            name: 'ImageCropping',
            meta: {
              title: 'router.imageCropping'
            }
          },
          {
            path: 'video-player',
            component: 'views/Components/VideoPlayer',
            name: 'VideoPlayer',
            meta: {
              title: 'router.videoPlayer'
            }
          },
          {
            path: 'avatars',
            component: 'views/Components/Avatars',
            name: 'Avatars',
            meta: {
              title: 'router.avatars'
            }
          },
          {
            path: 'i-agree',
            component: 'views/Components/IAgree',
            name: 'IAgree',
            meta: {
              title: 'router.iAgree'
            }
          },
          {
            path: 'tree',
            component: 'views/Components/Tree',
            name: 'Tree',
            meta: {
              title: 'router.tree'
            }
          }
        ]
      },
      {
        path: 'function',
        component: '##',
        redirect: '/demo/function/multiple-tabs',
        name: 'Function',
        meta: {
          title: 'router.function',
          icon: 'vi-ri:function-fill',
          alwaysShow: true
        },
        children: [
          {
            path: 'multiple-tabs',
            component: 'views/Function/MultipleTabs',
            name: 'MultipleTabs',
            meta: {
              title: 'router.multipleTabs'
            }
          },
          {
            path: 'multiple-tabs-demo/:id',
            component: 'views/Function/MultipleTabsDemo',
            name: 'MultipleTabsDemo',
            meta: {
              hidden: true,
              title: 'router.details',
              canTo: true,
              activeMenu: '/demo/function/multiple-tabs'
            }
          },
          {
            path: 'request',
            component: 'views/Function/Request',
            name: 'Request',
            meta: {
              title: 'router.request'
            }
          }
        ]
      },
      {
        path: 'hooks',
        component: '##',
        redirect: '/demo/hooks/useWatermark',
        name: 'Hooks',
        meta: {
          title: 'hooks',
          icon: 'vi-ic:outline-webhook',
          alwaysShow: true
        },
        children: [
          {
            path: 'useWatermark',
            component: 'views/hooks/useWatermark',
            name: 'UseWatermark',
            meta: {
              title: 'useWatermark'
            }
          },
          {
            path: 'useTagsView',
            component: 'views/hooks/useTagsView',
            name: 'UseTagsView',
            meta: {
              title: 'useTagsView'
            }
          },
          {
            path: 'useValidator',
            component: 'views/hooks/useValidator',
            name: 'UseValidator',
            meta: {
              title: 'useValidator'
            }
          },
          {
            path: 'useCrudSchemas',
            component: 'views/hooks/useCrudSchemas',
            name: 'UseCrudSchemas',
            meta: {
              title: 'useCrudSchemas'
            }
          },
          {
            path: 'useClipboard',
            component: 'views/hooks/useClipboard',
            name: 'UseClipboard',
            meta: {
              title: 'useClipboard'
            }
          },
          {
            path: 'useNetwork',
            component: 'views/hooks/useNetwork',
            name: 'UseNetwork',
            meta: {
              title: 'useNetwork'
            }
          }
        ]
      },
      {
        path: 'level',
        component: '##',
        redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
        name: 'Level',
        meta: {
          title: 'router.level',
          icon: 'vi-carbon:skill-level-advanced'
        },
        children: [
          {
            path: 'menu1',
            name: 'Menu1',
            component: '##',
            redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
            meta: {
              title: 'router.menu1'
            },
            children: [
              {
                path: 'menu1-1',
                name: 'Menu11',
                component: '##',
                redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
                meta: {
                  title: 'router.menu11',
                  alwaysShow: true
                },
                children: [
                  {
                    path: 'menu1-1-1',
                    name: 'Menu111',
                    component: 'views/Level/Menu111',
                    meta: {
                      title: 'router.menu111'
                    }
                  }
                ]
              },
              {
                path: 'menu1-2',
                name: 'Menu12',
                component: 'views/Level/Menu12',
                meta: {
                  title: 'router.menu12'
                }
              }
            ]
          },
          {
            path: 'menu2',
            name: 'Menu2Demo',
            component: 'views/Level/Menu2',
            meta: {
              title: 'router.menu2'
            }
          }
        ]
      },
      {
        path: 'example',
        component: '##',
        redirect: '/demo/example/example-dialog',
        name: 'Example',
        meta: {
          title: 'router.example',
          icon: 'vi-ep:management',
          alwaysShow: true
        },
        children: [
          {
            path: 'example-dialog',
            component: 'views/Example/Dialog/ExampleDialog',
            name: 'ExampleDialog',
            meta: {
              title: 'router.exampleDialog'
            }
          },
          {
            path: 'example-page',
            component: 'views/Example/Page/ExamplePage',
            name: 'ExamplePage',
            meta: {
              title: 'router.examplePage'
            }
          },
          {
            path: 'example-add',
            component: 'views/Example/Page/ExampleAdd',
            name: 'ExampleAdd',
            meta: {
              title: 'router.exampleAdd',
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
            meta: {
              title: 'router.exampleEdit',
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
            meta: {
              title: 'router.exampleDetail',
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
        meta: {
          title: 'router.permissionAuthentication',
          icon: 'vi-eos-icons:role-binding',
          alwaysShow: true
        },
        children: [
          {
            path: 'menu',
            component: 'views/Authorization/Menu/Menu',
            name: 'Menu',
            meta: {
              title: 'router.menuManagement'
            }
          },
          {
            path: 'permission',
            component: 'views/Function/Test',
            name: 'Test',
            meta: {
              title: 'router.permission',
              permission: ['add', 'edit', 'delete']
            }
          }
        ]
      },
      {
        path: 'error',
        component: '##',
        redirect: '/demo/error/404-demo',
        name: 'Error',
        meta: {
          title: 'router.errorPage',
          icon: 'vi-ci:error',
          alwaysShow: true
        },
        children: [
          {
            path: '404-demo',
            component: 'views/Error/404',
            name: '404Demo',
            meta: {
              title: '404'
            }
          },
          {
            path: '403-demo',
            component: 'views/Error/403',
            name: '403Demo',
            meta: {
              title: '403'
            }
          },
          {
            path: '500-demo',
            component: 'views/Error/500',
            name: '500Demo',
            meta: {
              title: '500'
            }
          }
        ]
      }
    ]
  }
]

const testList: string[] = [
  '/demo',
  '/demo/dashboard',
  '/demo/dashboard/analysis',
  '/demo/dashboard/workplace',
  '/demo/guide',
  '/demo/guide/index',
  '/demo/components',
  '/demo/components/form',
  '/demo/components/form/default-form',
  '/demo/components/form/use-form',
  '/demo/components/table',
  '/demo/components/table/default-table',
  '/demo/components/table/use-table',
  '/demo/components/table/tree-table',
  '/demo/components/table/table-image-preview',
  '/demo/components/table/table-video-preview',
  '/demo/components/table/card-table',
  '/demo/components/editor-demo',
  '/demo/components/editor-demo/editor',
  '/demo/components/editor-demo/json-editor',
  '/demo/components/editor-demo/code-editor',
  '/demo/components/search',
  '/demo/components/descriptions',
  '/demo/components/image-viewer',
  '/demo/components/dialog',
  '/demo/components/icon',
  '/demo/components/icon-picker',
  '/demo/components/echart',
  '/demo/components/count-to',
  '/demo/components/qrcode',
  '/demo/components/highlight',
  '/demo/components/infotip',
  '/demo/components/input-password',
  '/demo/components/waterfall',
  '/demo/components/image-cropping',
  '/demo/components/video-player',
  '/demo/components/avatars',
  '/demo/components/i-agree',
  '/demo/components/tree',
  '/demo/function',
  '/demo/function/multiple-tabs',
  '/demo/function/multiple-tabs-demo/:id',
  '/demo/function/request',
  '/demo/hooks',
  '/demo/hooks/useWatermark',
  '/demo/hooks/useTagsView',
  '/demo/hooks/useValidator',
  '/demo/hooks/useCrudSchemas',
  '/demo/hooks/useClipboard',
  '/demo/hooks/useNetwork',
  '/demo/level',
  '/demo/level/menu1',
  '/demo/level/menu1/menu1-1',
  '/demo/level/menu1/menu1-1/menu1-1-1',
  '/demo/level/menu1/menu1-2',
  '/demo/level/menu2',
  '/demo/example',
  '/demo/example/example-dialog',
  '/demo/example/example-page',
  '/demo/example/example-add',
  '/demo/example/example-edit',
  '/demo/example/example-detail',
  '/demo/authorization',
  '/demo/authorization/menu',
  '/demo/authorization/permission',
  '/demo/error',
  '/demo/error/404-demo',
  '/demo/error/403-demo',
  '/demo/error/500-demo'
]
const List: any[] = []

const roleNames = ['超级管理员', '管理员', '普通用户', '游客']
const menus = [
  [
    {
      path: '/demo',
      component: '#',
      redirect: '/demo/dashboard/analysis',
      name: 'Demo',
      status: Mock.Random.integer(0, 1),
      id: 18,
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
          id: 1,
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
              id: 2,
              meta: {
                title: '分析页',
                noCache: true
              }
            },
            {
              path: 'workplace',
              component: 'views/Dashboard/Workplace',
              name: 'Workplace',
              status: Mock.Random.integer(0, 1),
              id: 3,
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
          id: 6,
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
              id: 7,
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
                  id: 8,
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
                      id: 9,
                      permission: ['edit', 'add', 'delete'],
                      meta: {
                        title: '菜单1-1-1',
                        permission: ['edit', 'add', 'delete']
                      }
                    }
                  ]
                },
                {
                  path: 'menu1-2',
                  name: 'Menu12',
                  component: 'views/Level/Menu12',
                  status: Mock.Random.integer(0, 1),
                  id: 10,
                  permission: ['edit', 'add', 'delete'],
                  meta: {
                    title: '菜单1-2',
                    permission: ['edit', 'add', 'delete']
                  }
                }
              ]
            },
            {
              path: 'menu2',
              name: 'Menu2Demo',
              component: 'views/Level/Menu2',
              status: Mock.Random.integer(0, 1),
              id: 11,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '菜单2',
                permission: ['edit', 'add', 'delete']
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
          id: 12,
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
              id: 13,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '综合示例-弹窗',
                permission: ['edit', 'add', 'delete']
              }
            },
            {
              path: 'example-page',
              component: 'views/Example/Page/ExamplePage',
              name: 'ExamplePage',
              status: Mock.Random.integer(0, 1),
              id: 14,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '综合示例-页面',
                permission: ['edit', 'add', 'delete']
              }
            },
            {
              path: 'example-add',
              component: 'views/Example/Page/ExampleAdd',
              name: 'ExampleAdd',
              status: Mock.Random.integer(0, 1),
              id: 15,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '综合示例-新增',
                noTagsView: true,
                noCache: true,
                hidden: true,
                showMainRoute: true,
                activeMenu: '/demo/example/example-page',
                permission: ['edit', 'add', 'delete']
              }
            },
            {
              path: 'example-edit',
              component: 'views/Example/Page/ExampleEdit',
              name: 'ExampleEdit',
              status: Mock.Random.integer(0, 1),
              id: 16,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '综合示例-编辑',
                noTagsView: true,
                noCache: true,
                hidden: true,
                showMainRoute: true,
                activeMenu: '/demo/example/example-page',
                permission: ['edit', 'add', 'delete']
              }
            },
            {
              path: 'example-detail',
              component: 'views/Example/Page/ExampleDetail',
              name: 'ExampleDetail',
              status: Mock.Random.integer(0, 1),
              id: 17,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '综合示例-详情',
                noTagsView: true,
                noCache: true,
                hidden: true,
                showMainRoute: true,
                activeMenu: '/demo/example/example-page',
                permission: ['edit', 'add', 'delete']
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
      id: 4,
      children: [
        {
          path: 'https://element-plus-admin-doc.cn/',
          name: 'DocumentLink',
          status: Mock.Random.integer(0, 1),
          id: 5,
          meta: {
            title: '文档'
          }
        }
      ]
    }
  ],
  [
    {
      path: '/demo',
      component: '#',
      redirect: '/demo/dashboard/analysis',
      name: 'Demo',
      status: Mock.Random.integer(0, 1),
      id: 18,
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
          id: 1,
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
              id: 2,
              meta: {
                title: '分析页',
                noCache: true
              }
            },
            {
              path: 'workplace',
              component: 'views/Dashboard/Workplace',
              name: 'Workplace',
              status: Mock.Random.integer(0, 1),
              id: 3,
              meta: {
                title: '工作台',
                noCache: true
              }
            }
          ]
        }
      ]
    }
  ],
  [
    {
      path: '/demo',
      component: '#',
      redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
      name: 'Demo',
      status: Mock.Random.integer(0, 1),
      id: 18,
      meta: {
        title: 'Demo',
        icon: 'vi-ep:menu',
        alwaysShow: true
      },
      children: [
        {
          path: 'level',
          component: '##',
          redirect: '/demo/level/menu1/menu1-1/menu1-1-1',
          name: 'Level',
          status: Mock.Random.integer(0, 1),
          id: 6,
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
              id: 7,
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
                  id: 8,
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
                      id: 9,
                      permission: ['edit', 'add', 'delete'],
                      meta: {
                        title: '菜单1-1-1',
                        permission: ['edit', 'add', 'delete']
                      }
                    }
                  ]
                },
                {
                  path: 'menu1-2',
                  name: 'Menu12',
                  component: 'views/Level/Menu12',
                  status: Mock.Random.integer(0, 1),
                  id: 10,
                  permission: ['edit', 'add', 'delete'],
                  meta: {
                    title: '菜单1-2',
                    permission: ['edit', 'add', 'delete']
                  }
                }
              ]
            },
            {
              path: 'menu2',
              name: 'Menu2Demo',
              component: 'views/Level/Menu2',
              status: Mock.Random.integer(0, 1),
              id: 11,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '菜单2',
                permission: ['edit', 'add', 'delete']
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
      id: 4,
      children: [
        {
          path: 'https://element-plus-admin-doc.cn/',
          name: 'DocumentLink',
          status: Mock.Random.integer(0, 1),
          id: 5,
          meta: {
            title: '文档'
          }
        }
      ]
    }
  ],
  [
    {
      path: '/demo',
      component: '#',
      redirect: '/demo/example/example-dialog',
      name: 'Demo',
      status: Mock.Random.integer(0, 1),
      id: 18,
      meta: {
        title: 'Demo',
        icon: 'vi-ep:menu',
        alwaysShow: true
      },
      children: [
        {
          path: 'example',
          component: '##',
          redirect: '/demo/example/example-dialog',
          name: 'Example',
          status: Mock.Random.integer(0, 1),
          id: 12,
          meta: {
            title: '综合示例',
            icon: 'vi-ep:management',
            alwaysShow: true
          },
          children: [
            {
              path: 'example-detail',
              component: 'views/Example/Page/ExampleDetail',
              name: 'ExampleDetail',
              status: Mock.Random.integer(0, 1),
              id: 17,
              permission: ['edit', 'add', 'delete'],
              meta: {
                title: '综合示例-详情',
                noTagsView: true,
                noCache: true,
                hidden: true,
                showMainRoute: true,
                activeMenu: '/demo/example/example-page',
                permission: ['edit', 'add', 'delete']
              }
            }
          ]
        }
      ]
    }
  ]
]

for (let i = 0; i < 4; i++) {
  List.push(
    Mock.mock({
      id: toAnyString(),
      // timestamp: +Mock.Random.date('T'),
      roleName: roleNames[i],
      role: '@first',
      status: Mock.Random.integer(0, 1),
      createTime: '@datetime',
      remark: '@cword(10, 15)',
      menu: menus[i]
    })
  )
}

export default [
  // 列表接口
  {
    url: '/mock/role/list',
    method: 'get',
    timeout,
    response: () => {
      return {
        code: SUCCESS_CODE,
        data: adminList
      }
    }
  },
  {
    url: '/mock/role/table',
    method: 'get',
    timeout,
    response: () => {
      return {
        code: SUCCESS_CODE,
        data: {
          list: List,
          total: 4
        }
      }
    }
  },
  // 列表接口
  {
    url: '/mock/role/list2',
    method: 'get',
    timeout,
    response: () => {
      return {
        code: SUCCESS_CODE,
        data: testList
      }
    }
  },
  {
    url: '/mock/role/table',
    method: 'get',
    timeout,
    response: () => {
      return {
        code: SUCCESS_CODE,
        data: {
          list: List,
          total: 4
        }
      }
    }
  }
]
