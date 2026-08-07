<script setup lang="ts">
import { ref } from 'vue'
import { Descriptions, DescriptionsSchema } from '@/components/Descriptions'
import { useI18n } from '@/hooks/web/useI18n'
import { PropType } from 'vue'
import { RoleListItem } from '@/api/role/types'

const { t } = useI18n()

defineProps({
  currentRow: {
    type: Object as PropType<Nullable<RoleListItem>>,
    default: () => undefined
  }
})

const detailSchema = ref<DescriptionsSchema[]>([
  {
    field: 'roleName',
    label: t('role.roleName')
  },
  {
    field: 'code',
    label: t('role.code')
  },
  {
    field: 'createTime',
    label: t('tableDemo.displayTime')
  }
])
</script>

<template>
  <Descriptions :schema="detailSchema" :data="currentRow || {}" />
</template>
