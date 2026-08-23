export interface GmailHeader {
  name: string
  value: string
}

export interface GmailMessageBody {
  attachmentId?: string
  size: number
  data?: string
}

export interface GmailMessagePart {
  partId: string
  mimeType: string
  filename: string
  headers: GmailHeader[]
  body: GmailMessageBody
  parts?: GmailMessagePart[]
}

export interface GmailMessage {
  id: string
  threadId: string
  labelIds: string[]
  snippet: string
  historyId: string
  internalDate: string
  sizeEstimate: number
  payload: GmailMessagePart
}

export interface GmailUnreadResponse {
  messages: GmailMessage[]
  nextPageToken?: string
  resultSizeEstimate: number
}
