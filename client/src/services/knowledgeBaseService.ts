import axios from './axiosConfig'
import chatService from './chatService'
import { sourceAccess, type SourceTarget } from '../contexts/SourceAccessContext'
import { getErrorMessage } from '../utils/message'
import type { ObjectKnowledgeResponse, KnowledgeTextRequest, KnowledgeMappingsRequest } from '../types/objectKnowledge'
const API_URL = import.meta.env.VITE_API_URL
interface Envelope { success: boolean; data: ObjectKnowledgeResponse; error?: string }

export class KnowledgeRequestError extends Error {
  constructor(message: string, public readonly conflict = false, public readonly saved = false) { super(message); this.name = 'KnowledgeRequestError' }
}
async function admission(chatId: string) {
  const authority = sourceAccess()
  const target = await authority.admit(chatId)
  authority.assert(target)
  return { target, revision: authority.view(chatId).chat?.selection_revision }
}
function assertCurrent(chatId: string, permit: { target: SourceTarget; revision?: number }) {
  const authority = sourceAccess()
  authority.assertCurrent(permit.target)
  if (authority.view(chatId).chat?.selection_revision !== permit.revision) throw new KnowledgeRequestError('对象选择已变化，请重新加载知识库。', true)
}
function decode(response: Envelope, chatId: string, mutation: boolean, v2: boolean) {
  if (!response.success) throw new KnowledgeRequestError(response.error || '知识库请求失败，请重试。')
  const data = response.data
  if (!data || data.chat_id !== chatId) throw new KnowledgeRequestError('知识库响应不完整，请重新加载。')
  if (mutation && v2 && data.saved !== true) throw new KnowledgeRequestError('服务端未确认保存；本地草稿已保留，请重新加载。', true)
  if (mutation && v2 && (!data.kb_revision || !Array.isArray(data.table_descriptions))) throw new KnowledgeRequestError('写入后的版本尚未确认；本地草稿仍保留，请重新加载后采用服务器版本。', true, true)
  if (!Array.isArray(data.table_descriptions)) throw new KnowledgeRequestError('知识库响应不完整，请重新加载。')
  const normalize = (rows: ObjectKnowledgeResponse['table_descriptions']) => rows.map(row => ({ ...row, field_descriptions: row.field_descriptions ?? [] }))
  return { ...data, table_descriptions: normalize(data.table_descriptions), effective_descriptions: normalize(data.effective_descriptions ?? []) }
}
function failure(error: unknown): never {
  if (error instanceof KnowledgeRequestError) throw error
  if (axios.isAxiosError(error) && error.response?.status === 409) throw new KnowledgeRequestError('知识库版本已变化；请重新加载后明确采用服务器版本，本地草稿仍保留。', true)
  throw new KnowledgeRequestError(getErrorMessage(error, '知识库请求失败，请重新加载后重试。'))
}
const knowledgeBaseService = {
  async getKnowledgeBase(chatId: string): Promise<ObjectKnowledgeResponse> {
    const permit = await admission(chatId)
    try {
      const response = await axios.get<Envelope>(`${API_URL}/chats/${chatId}/knowledge-base`)
      assertCurrent(chatId, permit)
      return decode(response.data, chatId, false, false)
    } catch (error) { return failure(error) }
  },
  async getSelectedObjects(chatId: string) {
    const permit = await admission(chatId)
    const directory = await chatService.getTables(chatId)
    assertCurrent(chatId, permit)
    if (directory.query_objects_enabled !== true || directory.metadata_version !== 2) throw new KnowledgeRequestError('对象功能未启用；请恢复后重新加载映射目标。')
    if (directory.selection_revision !== permit.revision) throw new KnowledgeRequestError('对象目录版本已变化，请重新加载映射目标。')
    return directory.tables.filter(object => object.is_selected && !!object.object_id)
  },
  async updateKnowledgeBase(chatId: string, request: KnowledgeTextRequest): Promise<ObjectKnowledgeResponse> {
    const permit = await admission(chatId)
    // Whitelist text patches even if a caller passes a richer response object.
    const body: KnowledgeTextRequest = {
      ...(request.expected_kb_revision === undefined ? {} : { expected_kb_revision: request.expected_kb_revision }),
      table_descriptions: request.table_descriptions.map(edit => ({
        ...(edit.description_index === undefined ? {} : { description_index: edit.description_index }),
        ...(edit.object_id === undefined ? {} : { object_id: edit.object_id }),
        ...(edit.table_name === undefined ? {} : { table_name: edit.table_name }),
        ...(edit.description === undefined ? {} : { description: edit.description }),
        ...(edit.field_descriptions === undefined ? {} : { field_descriptions: edit.field_descriptions.map(field => ({ field_name: field.field_name, description: field.description })) }),
      })),
    }
    try {
      const response = await axios.put<Envelope>(`${API_URL}/chats/${chatId}/knowledge-base`, body)
      assertCurrent(chatId, permit)
      return decode(response.data, chatId, true, request.expected_kb_revision !== undefined)
    } catch (error) { return failure(error) }
  },
  async confirmMappings(chatId: string, request: KnowledgeMappingsRequest): Promise<ObjectKnowledgeResponse> {
    const permit = await admission(chatId)
    try {
      const response = await axios.post<Envelope>(`${API_URL}/chats/${chatId}/knowledge-base/mappings`, {
        expected_kb_revision: request.expected_kb_revision,
        mappings: request.mappings.map(mapping => ({ description_index: mapping.description_index, object_id: mapping.object_id })),
      })
      assertCurrent(chatId, permit)
      return decode(response.data, chatId, true, true)
    } catch (error) { return failure(error) }
  },
}
export default knowledgeBaseService
