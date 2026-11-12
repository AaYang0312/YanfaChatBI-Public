import axios from './axiosConfig'
import type { Chat } from '../types/chat'
import type { DataSyncJob, DataSyncStatus } from '../types/dataSync'

interface Envelope<T> { success: boolean; data: T; error?: string }
const path = (chatId: string) => `${import.meta.env.VITE_API_URL}/chats/${encodeURIComponent(chatId)}`
function unwrap<T>(body: Envelope<T>): T {
  if (!body.success || !body.data) throw new Error(body.error || '同步状态暂不可确认')
  return body.data
}

export const dataSyncService = {
  async getChat(chatId: string, signal?: AbortSignal): Promise<Chat> {
    const response = await axios.get<Envelope<Chat>>(path(chatId), { signal, timeout: 10000 })
    return unwrap(response.data)
  },
  async getStatus(chatId: string, signal?: AbortSignal, requestId?: string): Promise<DataSyncStatus> {
    const response = await axios.get<Envelope<DataSyncStatus>>(`${path(chatId)}/data-sync/status`, {
      signal, timeout: 10000, params: requestId ? { request_id: requestId } : undefined,
    })
    return unwrap(response.data)
  },
  async createJob(chatId: string, requestId: string, signal?: AbortSignal): Promise<DataSyncJob> {
    const response = await axios.post<Envelope<DataSyncJob>>(`${path(chatId)}/data-sync/jobs`, { request_id: requestId }, { signal, timeout: 10000 })
    return unwrap(response.data)
  },
  async getJob(chatId: string, jobId: string, signal?: AbortSignal): Promise<DataSyncJob> {
    const response = await axios.get<Envelope<DataSyncJob>>(`${path(chatId)}/data-sync/jobs/${encodeURIComponent(jobId)}`, { signal, timeout: 10000 })
    return unwrap(response.data)
  },
}
