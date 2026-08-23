import request from '@/axios'
import type { GmailUnreadResponse } from './types'

export const getUnreadGmailApi = (): Promise<IResponse<GmailUnreadResponse>> => {
  return request.get({ url: '/api/v1/gmail/unread' })
}
