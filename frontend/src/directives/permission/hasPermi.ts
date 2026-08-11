import type { App, Directive, DirectiveBinding } from 'vue'
import { useI18n } from '@/hooks/web/useI18n'
import { useUserStoreWithOut } from '@/store/modules/user'
import { hasEffectivePermission, isManagementPermission } from '@/utils/accessControl'
import router from '@/router'

const { t } = useI18n()

const hasPermission = (value: string): boolean => {
  if (!value) {
    throw new Error(t('permission.hasPermission'))
  }

  if (isManagementPermission(value)) {
    return hasEffectivePermission(useUserStoreWithOut().getEffectivePermissions, value)
  }

  const permission = (router.currentRoute.value.meta.permission || []) as string[]
  return permission.includes(value)
}

function hasPermi(el: Element, binding: DirectiveBinding) {
  if (!hasPermission(binding.value)) {
    el.parentNode?.removeChild(el)
  }
}

const permiDirective: Directive = {
  mounted: hasPermi
}

export const setupPermissionDirective = (app: App<Element>) => {
  app.directive('hasPermi', permiDirective)
}

export default permiDirective
