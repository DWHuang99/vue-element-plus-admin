<script setup lang="tsx">
import { reactive, ref, watch, onMounted, unref } from 'vue'
import { Form, FormSchema } from '@/components/Form'
import { useI18n } from '@/hooks/web/useI18n'
import { ElCheckbox, ElLink, ElAlert } from 'element-plus'
import { useForm } from '@/hooks/web/useForm'
import {
  loginApi,
  refreshApi,
  getOIDCLoginURL,
  getCurrentUserApi,
  getCurrentUserMenusApi
} from '@/api/login'
import { usePermissionStore } from '@/store/modules/permission'
import { useRouter } from 'vue-router'
import type { RouteLocationNormalizedLoaded, RouteRecordRaw } from 'vue-router'
import type { UserLoginType } from '@/api/login/types'
import { useValidator } from '@/hooks/web/useValidator'
import { Icon } from '@/components/Icon'
import { useUserStore } from '@/store/modules/user'
import { BaseButton } from '@/components/Button'
import { pathResolve } from '@/utils/routerHelper'

const { required } = useValidator()

const emit = defineEmits(['to-register'])

const userStore = useUserStore()

const permissionStore = usePermissionStore()

const { currentRoute, addRoute, push, replace } = useRouter()

const { t } = useI18n()

const oidcEnabled = import.meta.env.VITE_OIDC_ENABLED === 'true'

const rules = {
  username: [required()],
  password: [required()]
}

const schema = reactive<FormSchema[]>([
  {
    field: 'title',
    colProps: {
      span: 24
    },
    formItemProps: {
      slots: {
        default: () => {
          return <h2 class="text-2xl font-bold text-center w-[100%]">{t('login.login')}</h2>
        }
      }
    }
  },
  {
    field: 'username',
    label: t('login.username'),
    // value: 'admin',
    component: 'Input',
    colProps: {
      span: 24
    },
    componentProps: {
      placeholder: 'admin or test'
    }
  },
  {
    field: 'password',
    label: t('login.password'),
    // value: 'admin',
    component: 'InputPassword',
    colProps: {
      span: 24
    },
    componentProps: {
      style: {
        width: '100%'
      },
      placeholder: 'admin or test',
      // 按下enter键触发登录
      onKeydown: (_e: any) => {
        if (_e.key === 'Enter') {
          _e.stopPropagation() // 阻止事件冒泡
          signIn()
        }
      }
    }
  },
  {
    field: 'error',
    colProps: {
      span: 24
    },
    formItemProps: {
      slots: {
        default: () => {
          if (!unref(errorMessage)) return null
          return (
            <ElAlert
              title={unref(errorMessage)}
              type="error"
              show-icon
              closable
              onClose={() => {
                errorMessage.value = ''
              }}
            />
          )
        }
      }
    }
  },
  {
    field: 'tool',
    colProps: {
      span: 24
    },
    formItemProps: {
      slots: {
        default: () => {
          return (
            <>
              <div class="flex justify-between items-center w-[100%]">
                <ElCheckbox v-model={remember.value} label={t('login.remember')} size="small" />
                <ElLink type="primary" underline={false}>
                  {t('login.forgetPassword')}
                </ElLink>
              </div>
            </>
          )
        }
      }
    }
  },
  {
    field: 'login',
    colProps: {
      span: 24
    },
    formItemProps: {
      slots: {
        default: () => {
          return (
            <>
              <div class="w-[100%]">
                <BaseButton
                  loading={loading.value}
                  type="primary"
                  class="w-[100%]"
                  onClick={signIn}
                >
                  {t('login.login')}
                </BaseButton>
              </div>
              <div class="w-[100%] mt-15px">
                <BaseButton class="w-[100%]" onClick={toRegister}>
                  {t('login.register')}
                </BaseButton>
              </div>
            </>
          )
        }
      }
    }
  },
  {
    field: 'other',
    hidden: !oidcEnabled,
    component: 'Divider',
    label: t('login.otherLogin'),
    componentProps: {
      contentPosition: 'center'
    }
  },
  {
    field: 'otherIcon',
    hidden: !oidcEnabled,
    colProps: {
      span: 24
    },
    formItemProps: {
      slots: {
        default: () => {
          return (
            <>
              <div class="w-[100%]">
                <BaseButton class="w-[100%]" loading={loading.value} onClick={signInWithOIDC}>
                  <Icon icon="vi-ant-design:google-circle-filled" class="mr-8px" />
                  {t('login.oidcLogin')}
                </BaseButton>
              </div>
            </>
          )
        }
      }
    }
  }
])

const remember = ref(userStore.getRememberMe)

const errorMessage = ref('')

const initLoginInfo = () => {
  const savedUsername = userStore.getLoginInfo
  if (savedUsername && unref(remember)) {
    setValues({ username: savedUsername })
  }
}
const { formRegister, formMethods } = useForm()
const { getFormData, getElFormExpose, setValues } = formMethods

const loading = ref(false)

const redirect = ref<string>('')

const findFirstAccessiblePath = (
  routers: AppRouteRecordRaw[],
  parentPath = '/'
): string | undefined => {
  for (const route of routers) {
    if (route.name === '404Page') continue

    const routePath = pathResolve(parentPath, route.path)
    const childPath = route.children?.length
      ? findFirstAccessiblePath(route.children, routePath)
      : undefined

    if (childPath) return childPath
    if (route.component) return routePath
  }
}

const getRedirectPath = () => {
  return redirect.value && redirect.value !== '/404'
    ? redirect.value
    : findFirstAccessiblePath(permissionStore.addRouters) || '/'
}

watch(
  () => currentRoute.value,
  (route: RouteLocationNormalizedLoaded) => {
    redirect.value = route?.query?.redirect as string
  },
  {
    immediate: true
  }
)

watch(
  () => remember.value,
  (newVal) => {
    userStore.setRememberMe(newVal)
    if (!newVal) {
      userStore.setLoginInfo(undefined)
    }
  }
)

// 登录
const establishSession = async (accessToken: string) => {
  userStore.setToken(accessToken)
  const currentUser = await getCurrentUserApi()
  userStore.setUserInfo(currentUser.data)
  await getRole()
}

const signIn = async () => {
  const formRef = await getElFormExpose()
  await formRef?.validate(async (isValid) => {
    if (isValid) {
      loading.value = true
      errorMessage.value = ''
      const formData = await getFormData<UserLoginType>()

      try {
        const res = await loginApi(formData)

        if (res) {
          // 是否记住我 - 只保存用户名
          if (unref(remember)) {
            userStore.setLoginInfo(formData.username)
          } else {
            userStore.setLoginInfo(undefined)
          }
          userStore.setRememberMe(unref(remember))
          await establishSession(res.data.accessToken)
        }
      } catch (error: any) {
        userStore.setToken('')
        userStore.setUserInfo(undefined)
        errorMessage.value = error?.message || '登录失败，请检查用户名和密码'
      } finally {
        loading.value = false
      }
    }
  })
}

const signInWithOIDC = () => {
  errorMessage.value = ''
  window.location.assign(getOIDCLoginURL())
}

const clearOAuthQuery = async () => {
  const query = { ...currentRoute.value.query }
  delete query.oauth
  delete query.error
  await replace({ query })
}

const finishOIDCLogin = async () => {
  loading.value = true
  errorMessage.value = ''
  try {
    const response = await refreshApi()
    await establishSession(response.data.accessToken)
  } catch (error: any) {
    userStore.setToken('')
    userStore.setUserInfo(undefined)
    errorMessage.value = error?.message || t('login.oidcLoginFailed')
    await clearOAuthQuery()
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  initLoginInfo()
  if (currentRoute.value.query.oauth === 'success') {
    await finishOIDCLogin()
  } else if (currentRoute.value.query.oauth === 'error') {
    errorMessage.value = t('login.oidcLoginFailed')
    await clearOAuthQuery()
  }
})

// 获取角色信息
const getRole = async () => {
  const res = await getCurrentUserMenusApi()
  if (res) {
    const routers = res.data.list || []
    userStore.setRoleRouters(routers)
    await permissionStore.generateRoutes('server', routers).catch(() => {})

    permissionStore.getAddRouters.forEach((route) => {
      addRoute(route as RouteRecordRaw) // 动态添加可访问路由表
    })
    permissionStore.setIsAddRouters(true)
    push({ path: getRedirectPath() })
  }
}

// 去注册页面
const toRegister = () => {
  emit('to-register')
}
</script>

<template>
  <Form
    :schema="schema"
    :rules="rules"
    label-position="top"
    hide-required-asterisk
    size="large"
    class="dark:(border-1 border-[var(--el-border-color)] border-solid)"
    @register="formRegister"
  />
</template>
