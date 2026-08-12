<script setup lang="ts">
import { Error } from '@/components/Error'
import { usePermissionStore } from '@/store/modules/permission'
import { useUserStore } from '@/store/modules/user'
import { useRouter } from 'vue-router'

const { push } = useRouter()

const permissionStore = usePermissionStore()
const userStore = useUserStore()

const errorClick = () => {
  const homePath = permissionStore.addRouters[0]?.path
  if (homePath && homePath !== '/:path(.*)*') {
    push(homePath)
    return
  }

  userStore.reset()
}
</script>

<template>
  <Error @error-click="errorClick" />
</template>
