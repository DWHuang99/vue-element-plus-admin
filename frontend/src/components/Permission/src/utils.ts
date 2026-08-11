import { useI18n } from '@/hooks/web/useI18n'
import { useUserStoreWithOut } from '@/store/modules/user'
import { hasEffectivePermission, isManagementPermission } from '@/utils/accessControl'
import router from '@/router'

export const hasPermi = (value: string) => {
  const { t } = useI18n()
  if (!value) {
    throw new Error(t('permission.hasPermission'))
  }

  if (isManagementPermission(value)) {
    return hasEffectivePermission(useUserStoreWithOut().getEffectivePermissions, value)
  }

  const permission = (router.currentRoute.value.meta.permission || []) as string[]
  return permission.includes(value)
}
