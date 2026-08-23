<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { isAxiosError } from 'axios'
import { ElButton, ElCard, ElEmpty, ElResult, ElSkeleton, ElTag } from 'element-plus'
import { ContentWrap } from '@/components/ContentWrap'
import { getUnreadGmailApi } from '@/api/gmail'
import type { GmailHeader, GmailMessage, GmailMessagePart } from '@/api/gmail/types'
import { getOIDCLoginURL } from '@/api/login'
import { useI18n } from '@/hooks/web/useI18n'

type ErrorKind = '' | 'authorization' | 'request'

const { t } = useI18n()
const loading = ref(false)
const messages = ref<GmailMessage[]>([])
const selectedMessageID = ref('')
const resultSizeEstimate = ref(0)
const errorKind = ref<ErrorKind>('')

const selectedMessage = computed(() => {
  return messages.value.find((message) => message.id === selectedMessageID.value)
})

const getHeader = (headers: GmailHeader[] | undefined, name: string) => {
  return headers?.find((header) => header.name.toLowerCase() === name.toLowerCase())?.value || ''
}

const getSubject = (message: GmailMessage) => {
  return getHeader(message.payload.headers, 'Subject') || t('gmail.noSubject')
}

const getSender = (message: GmailMessage) => {
  return getHeader(message.payload.headers, 'From') || t('gmail.unknownSender')
}

const getRecipient = (message: GmailMessage) => {
  return getHeader(message.payload.headers, 'To')
}

const formatMessageDate = (message: GmailMessage) => {
  const internalTimestamp = Number(message.internalDate)
  const headerDate = getHeader(message.payload.headers, 'Date')
  const date =
    Number.isFinite(internalTimestamp) && internalTimestamp > 0
      ? new Date(internalTimestamp)
      : new Date(headerDate)

  if (Number.isNaN(date.getTime())) return ''
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  }).format(date)
}

const decodeBase64URL = (value?: string) => {
  if (!value) return ''
  try {
    const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=')
    const binary = window.atob(padded)
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0))
    return new TextDecoder().decode(bytes)
  } catch {
    return ''
  }
}

const findBodyPart = (
  part: GmailMessagePart,
  mimeType: 'text/plain' | 'text/html'
): GmailMessagePart | undefined => {
  if (part.mimeType.toLowerCase() === mimeType && part.body?.data) return part
  for (const child of part.parts || []) {
    const matched = findBodyPart(child, mimeType)
    if (matched) return matched
  }
  return undefined
}

const htmlToPlainText = (html: string) => {
  const document = new DOMParser().parseFromString(html, 'text/html')
  return document.body.textContent?.trim() || ''
}

const getMessageBody = (message: GmailMessage) => {
  const plainPart = findBodyPart(message.payload, 'text/plain')
  const plainText = decodeBase64URL(plainPart?.body.data)
  if (plainText.trim()) return plainText.trim()

  const htmlPart = findBodyPart(message.payload, 'text/html')
  const htmlText = htmlToPlainText(decodeBase64URL(htmlPart?.body.data))
  return htmlText || message.snippet || t('gmail.noContent')
}

const selectMessage = (message: GmailMessage) => {
  selectedMessageID.value = message.id
}

const authorizeGoogle = () => {
  window.location.href = getOIDCLoginURL()
}

const loadUnreadMessages = async () => {
  loading.value = true
  errorKind.value = ''
  try {
    const response = await getUnreadGmailApi()
    messages.value = response?.data?.messages || []
    resultSizeEstimate.value = response?.data?.resultSizeEstimate || 0
    if (!messages.value.some((message) => message.id === selectedMessageID.value)) {
      selectedMessageID.value = messages.value[0]?.id || ''
    }
  } catch (error) {
    const code = isAxiosError<IResponse>(error) ? error.response?.data?.code : undefined
    errorKind.value = code === 40901 || code === 40902 ? 'authorization' : 'request'
    messages.value = []
    selectedMessageID.value = ''
  } finally {
    loading.value = false
  }
}

onMounted(loadUnreadMessages)
</script>

<template>
  <ContentWrap>
    <div class="mail-header">
      <div>
        <div class="mail-title">{{ t('gmail.title') }}</div>
        <div class="mail-description">{{ t('gmail.description') }}</div>
      </div>
      <div class="mail-actions">
        <ElTag v-if="!loading && !errorKind" type="info" effect="plain">
          {{ t('gmail.unreadCount', { count: resultSizeEstimate }) }}
        </ElTag>
        <ElButton type="primary" :loading="loading" @click="loadUnreadMessages">
          {{ t('gmail.refresh') }}
        </ElButton>
      </div>
    </div>

    <ElSkeleton v-if="loading" :rows="8" animated />

    <ElResult
      v-else-if="errorKind === 'authorization'"
      icon="warning"
      :title="t('gmail.authorizationRequired')"
      :sub-title="t('gmail.authorizationDescription')"
    >
      <template #extra>
        <ElButton type="primary" @click="authorizeGoogle">
          {{ t('gmail.authorize') }}
        </ElButton>
      </template>
    </ElResult>

    <ElResult
      v-else-if="errorKind === 'request'"
      icon="error"
      :title="t('gmail.loadFailed')"
      :sub-title="t('gmail.loadFailedDescription')"
    >
      <template #extra>
        <ElButton type="primary" @click="loadUnreadMessages">
          {{ t('gmail.retry') }}
        </ElButton>
      </template>
    </ElResult>

    <ElEmpty v-else-if="messages.length === 0" :description="t('gmail.empty')" />

    <div v-else class="mail-layout">
      <ElCard class="mail-list-card" shadow="never" :body-style="{ padding: '0' }">
        <button
          v-for="message in messages"
          :key="message.id"
          type="button"
          class="mail-list-item"
          :class="{ active: message.id === selectedMessageID }"
          @click="selectMessage(message)"
        >
          <div class="mail-list-row">
            <strong class="mail-sender">{{ getSender(message) }}</strong>
            <span class="mail-date">{{ formatMessageDate(message) }}</span>
          </div>
          <div class="mail-subject">{{ getSubject(message) }}</div>
          <div class="mail-snippet">{{ message.snippet || t('gmail.noContent') }}</div>
        </button>
      </ElCard>

      <ElCard v-if="selectedMessage" class="mail-content-card" shadow="never">
        <div class="message-subject">{{ getSubject(selectedMessage) }}</div>
        <div class="message-meta">
          <div
            ><span>{{ t('gmail.from') }}：</span>{{ getSender(selectedMessage) }}</div
          >
          <div v-if="getRecipient(selectedMessage)">
            <span>{{ t('gmail.to') }}：</span>{{ getRecipient(selectedMessage) }}
          </div>
          <div
            ><span>{{ t('gmail.date') }}：</span>{{ formatMessageDate(selectedMessage) }}</div
          >
        </div>
        <div class="message-body">{{ getMessageBody(selectedMessage) }}</div>
      </ElCard>
    </div>
  </ContentWrap>
</template>

<style scoped>
.mail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}

.mail-title {
  font-size: 22px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.mail-description {
  margin-top: 6px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.mail-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.mail-layout {
  display: grid;
  grid-template-columns: minmax(280px, 36%) minmax(0, 1fr);
  gap: 16px;
  min-height: 560px;
}

.mail-list-card,
.mail-content-card {
  min-width: 0;
}

.mail-list-item {
  display: block;
  width: 100%;
  padding: 16px 18px;
  overflow: hidden;
  color: inherit;
  text-align: left;
  cursor: pointer;
  background: transparent;
  border: 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
  transition: background-color 0.2s ease;
}

.mail-list-item:hover,
.mail-list-item.active {
  background: var(--el-fill-color-light);
}

.mail-list-item.active {
  box-shadow: inset 3px 0 0 var(--el-color-primary);
}

.mail-list-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.mail-sender,
.mail-subject,
.mail-snippet {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mail-sender {
  font-size: 14px;
  color: var(--el-text-color-primary);
}

.mail-date {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  flex: none;
}

.mail-subject {
  margin-top: 8px;
  font-size: 14px;
  font-weight: 500;
  color: var(--el-text-color-regular);
}

.mail-snippet {
  margin-top: 6px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.message-subject {
  padding-bottom: 16px;
  font-size: 22px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.message-meta {
  display: grid;
  padding: 16px 0;
  font-size: 13px;
  color: var(--el-text-color-regular);
  border-bottom: 1px solid var(--el-border-color-lighter);
  gap: 8px;
}

.message-meta span {
  color: var(--el-text-color-secondary);
}

.message-body {
  padding: 22px 4px;
  font-size: 14px;
  line-height: 1.8;
  color: var(--el-text-color-primary);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

@media (width <= 900px) {
  .mail-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .mail-layout {
    grid-template-columns: 1fr;
  }
}
</style>
