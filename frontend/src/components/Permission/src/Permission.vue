<script setup lang="ts">
import { propTypes } from '@/utils/propTypes'
import { computed, unref } from 'vue'
import { useRouter } from 'vue-router'
import { useUserStore } from '@/store/modules/user'
import { hasEffectivePermission, isManagementPermission } from '@/utils/accessControl'

const { currentRoute } = useRouter()
const userStore = useUserStore()

const props = defineProps({
  permission: propTypes.string.def()
})

const hasPermission = computed(() => {
  const permission = unref(props.permission)
  if (!permission) {
    return true
  }

  if (isManagementPermission(permission)) {
    return hasEffectivePermission(userStore.getEffectivePermissions, permission)
  }

  return ((unref(currentRoute)?.meta?.permission || []) as string[]).includes(permission)
})
</script>

<template>
  <template v-if="hasPermission">
    <slot></slot>
  </template>
</template>
