<script setup lang="tsx">
import { Form, FormSchema } from '@/components/Form'
import { reactive, ref, unref } from 'vue'
import { useI18n } from '@/hooks/web/useI18n'
import { useForm } from '@/hooks/web/useForm'
import { FormRules } from 'element-plus'
import { useValidator } from '@/hooks/web/useValidator'
import { BaseButton } from '@/components/Button'
import { UserLoginType } from '@/api/login/types'
import { useUserStore } from '@/store/modules/user'
import { useRouter } from 'vue-router'

const emit = defineEmits(['to-login'])

const { formRegister, formMethods } = useForm()
const { getElFormExpose, getFormData } = formMethods

const { t } = useI18n()

const { required } = useValidator()

const userStore = useUserStore()

const { push } = useRouter()

const schema = reactive<FormSchema[]>([
  {
    field: 'title',
    colProps: {
      span: 24
    },
    formItemProps: {
      slots: {
        default: () => {
          return <h2 class="text-2xl font-bold text-center w-[100%]">{t('login.register')}</h2>
        }
      }
    }
  },
  {
    field: 'username',
    label: t('login.username'),
    value: '',
    component: 'Input',
    colProps: {
      span: 24
    },
    componentProps: {
      placeholder: t('login.usernamePlaceholder')
    }
  },
  {
    field: 'password',
    label: t('login.password'),
    value: '',
    component: 'InputPassword',
    colProps: {
      span: 24
    },
    componentProps: {
      style: {
        width: '100%'
      },
      strength: true,
      placeholder: t('login.passwordPlaceholder')
    }
  },
  {
    field: 'check_password',
    label: t('login.checkPassword'),
    value: '',
    component: 'InputPassword',
    colProps: {
      span: 24
    },
    componentProps: {
      style: {
        width: '100%'
      },
      placeholder: t('login.passwordPlaceholder')
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
          return <div class="text-red-500 text-14px text-left w-[100%]">{unref(errorMessage)}</div>
        }
      }
    }
  },
  {
    field: 'register',
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
                  type="primary"
                  class="w-[100%]"
                  loading={loading.value}
                  onClick={loginRegister}
                >
                  {t('login.register')}
                </BaseButton>
              </div>
              <div class="w-[100%] mt-15px">
                <BaseButton class="w-[100%]" onClick={toLogin}>
                  {t('login.hasUser')}
                </BaseButton>
              </div>
            </>
          )
        }
      }
    }
  }
])

const rules: FormRules = {
  username: [required()],
  password: [
    required(),
    {
      validator: (_r, v: string, callback) => {
        if (v && v.length < 8) {
          callback(new Error('密码至少 8 个字符'))
        } else {
          callback()
        }
      }
    }
  ],
  check_password: [
    required(),
    {
      asyncValidator: async (_r, v: string, callback) => {
        // 与 EditPassword.vue 保持一致：直接从表单数据中读取密码字段进行比较，
        // 避免依赖 InputPassword 组件的事件透传（onInput 会收到原生事件对象）。
        const formData = await getFormData<{ password: string }>()
        if (v && v !== formData.password) {
          callback(new Error('两次输入的密码不一致'))
        } else {
          callback()
        }
      }
    }
  ]
}

const toLogin = () => {
  emit('to-login')
}

const loading = ref(false)

const errorMessage = ref('')

const loginRegister = async () => {
  const formRef = await getElFormExpose()
  formRef?.validate(async (valid) => {
    if (valid) {
      loading.value = true
      errorMessage.value = ''
      try {
        const formData = await getFormData<UserLoginType>()
        const ok = await userStore.register(formData)
        if (ok) {
          // 路由守卫统一根据路由模式和 effective_permissions 安装可访问路由。
          push('/')
        }
      } catch (error: any) {
        errorMessage.value =
          error?.response?.data?.error?.message || error?.message || '注册失败，请稍后重试'
      } finally {
        loading.value = false
      }
    }
  })
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
