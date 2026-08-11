<script setup lang="tsx">
import { computed, reactive, ref, unref } from 'vue'
import { getRoleListApi, saveRoleApi, deleteRoleApi } from '@/api/role'
import type { RoleListItem } from '@/api/role/types'
import { useTable } from '@/hooks/web/useTable'
import { useI18n } from '@/hooks/web/useI18n'
import { Table, TableColumn } from '@/components/Table'
import { ContentWrap } from '@/components/ContentWrap'
import { FormSchema } from '@/components/Form'
import Write from './components/Write.vue'
import Detail from './components/Detail.vue'
import { Dialog } from '@/components/Dialog'
import { BaseButton } from '@/components/Button'
import { useUserStore } from '@/store/modules/user'
import { hasEffectivePermission } from '@/utils/accessControl'

const { t } = useI18n()
const userStore = useUserStore()
const canWrite = computed(() =>
  hasEffectivePermission(userStore.getEffectivePermissions, 'roles.write')
)

// 角色表单字段：真实后端角色仅包含 名称 + 编码。
const roleFormSchema = reactive<FormSchema[]>([
  {
    field: 'roleName',
    label: t('role.roleName'),
    component: 'Input'
  },
  {
    field: 'code',
    label: t('role.code'),
    component: 'Input'
  }
])

const ids = ref<string[]>([])

const { tableRegister, tableState, tableMethods } = useTable({
  fetchDataApi: async () => {
    const res = await getRoleListApi()
    return {
      list: res.data.list || [],
      total: res.data.total
    }
  },
  fetchDelApi: async () => {
    const res = await deleteRoleApi(unref(ids))
    return !!res
  }
})

const { dataList, loading, total } = tableState
const { getList, getElTableExpose, delList } = tableMethods

const tableColumns = reactive<TableColumn[]>([
  {
    field: 'selection',
    type: 'selection',
    selectable: (row: RoleListItem) => !row.isBuiltin
  },
  {
    field: 'index',
    label: t('userDemo.index'),
    type: 'index'
  },
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
  },
  {
    field: 'action',
    label: t('userDemo.action'),
    width: 240,
    slots: {
      default: (data: any) => {
        const row = data.row
        return (
          <>
            {unref(canWrite) ? (
              <BaseButton type="primary" onClick={() => action(row, 'edit')}>
                {t('exampleDemo.edit')}
              </BaseButton>
            ) : null}
            <BaseButton type="success" onClick={() => action(row, 'detail')}>
              {t('exampleDemo.detail')}
            </BaseButton>
            {unref(canWrite) && !row.isBuiltin ? (
              <BaseButton type="danger" onClick={() => delData(row)}>
                {t('exampleDemo.del')}
              </BaseButton>
            ) : null}
          </>
        )
      }
    }
  }
])

const dialogVisible = ref(false)
const dialogTitle = ref('')

const currentRow = ref()
const actionType = ref('')

const writeRef = ref<ComponentRef<typeof Write>>()

const saveLoading = ref(false)

const action = (row: any, type: string) => {
  dialogTitle.value = t(type === 'edit' ? 'exampleDemo.edit' : 'exampleDemo.detail')
  actionType.value = type
  currentRow.value = row
  dialogVisible.value = true
}

const AddAction = () => {
  dialogTitle.value = t('exampleDemo.add')
  currentRow.value = undefined
  dialogVisible.value = true
  actionType.value = ''
}

const delLoading = ref(false)

const delData = async (row?: any) => {
  const elTableExpose = await getElTableExpose()
  ids.value = row ? [row.id] : elTableExpose?.getSelectionRows().map((v: any) => v.id) || []
  delLoading.value = true
  await delList(unref(ids).length).finally(() => {
    delLoading.value = false
  })
}

const save = async () => {
  const write = unref(writeRef)
  const formData = await write?.submit()
  if (formData) {
    saveLoading.value = true
    try {
      // 编辑时带上当前行 id（表单不含 id 字段），新建时为空 → 创建。
      const res = await saveRoleApi({ ...formData, id: unref(currentRow)?.id })
      if (res) {
        dialogVisible.value = false
        getList()
      }
    } finally {
      saveLoading.value = false
    }
  }
}
</script>

<template>
  <ContentWrap>
    <div class="mb-10px">
      <BaseButton v-if="canWrite" type="primary" @click="AddAction">
        {{ t('exampleDemo.add') }}
      </BaseButton>
      <BaseButton v-if="canWrite" :loading="delLoading" type="danger" @click="delData()">
        {{ t('exampleDemo.del') }}
      </BaseButton>
    </div>
    <Table
      :columns="tableColumns"
      default-expand-all
      node-key="id"
      :data="dataList"
      :loading="loading"
      :pagination="{
        total
      }"
      @register="tableRegister"
    />
  </ContentWrap>

  <Dialog v-model="dialogVisible" :title="dialogTitle">
    <Write
      v-if="actionType !== 'detail'"
      ref="writeRef"
      :form-schema="roleFormSchema"
      :current-row="currentRow"
    />
    <Detail v-else :current-row="currentRow" />

    <template #footer>
      <BaseButton
        v-if="actionType !== 'detail' && canWrite"
        type="primary"
        :loading="saveLoading"
        @click="save"
      >
        {{ t('exampleDemo.save') }}
      </BaseButton>
      <BaseButton @click="dialogVisible = false">{{ t('dialogDemo.close') }}</BaseButton>
    </template>
  </Dialog>
</template>
