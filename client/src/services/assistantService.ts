import axios from './axiosConfig'
import { sourceAccess } from '../contexts/SourceAccessContext'
import type {
  AnalysisRequest,
  CreateRunResponse,
  DraftRequest,
  Resolution,
  RunView,
} from '../types/assistant'

const API_URL = import.meta.env.VITE_API_URL

interface Envelope<T> {
  success?: boolean
  data: T
}

function unwrap<T>(body: T | Envelope<T>): T {
  if (body && typeof body === 'object' && 'data' in body && (body as Envelope<T>).data) {
    return (body as Envelope<T>).data
  }
  return body as T
}

function chatPath(chatId: string, suffix: string): string {
  return `${API_URL}/chats/${encodeURIComponent(chatId)}/assistant/${suffix}`
}

const assistantService = {
  async resolve(chatId: string, draft: DraftRequest): Promise<Resolution> {
    const access = sourceAccess()
    const target = await access.admit(chatId)
    access.assert(target)
    const response = await axios.post<Resolution | Envelope<Resolution>>(chatPath(chatId, 'resolve'), draft)
    access.assertCurrent(target)
    return unwrap(response.data)
  },

  async createRun(chatId: string, request: AnalysisRequest): Promise<CreateRunResponse> {
    const access = sourceAccess()
    const target = await access.admit(chatId)
    access.assert(target)
    const response = await axios.post<CreateRunResponse | Envelope<CreateRunResponse>>(chatPath(chatId, 'runs'), request)
    access.assertCurrent(target)
    return unwrap(response.data)
  },

  async getRun(chatId: string, runId: string): Promise<RunView> {
    const response = await axios.get<RunView | Envelope<RunView>>(
      chatPath(chatId, `runs/${encodeURIComponent(runId)}`),
    )
    return unwrap(response.data)
  },

  async cancelRun(chatId: string, runId: string): Promise<RunView> {
    const response = await axios.post<RunView | Envelope<RunView>>(
      chatPath(chatId, `runs/${encodeURIComponent(runId)}/cancel`),
    )
    return unwrap(response.data)
  },
}

export default assistantService
