import type { KnowledgeBaseResponse, TableDescription } from './knowledgeBase'

// Evidence is response-only. Mutations contain text and a server-resolved row/object identity.
export interface ObjectKnowledgeResponse extends KnowledgeBaseResponse {
  effective_descriptions?: TableDescription[]
  query_objects_enabled?: boolean
  index_status?: string
  saved?: boolean
  refresh_required?: boolean
}
export interface KnowledgeTextPatch {
  description_index?: number
  object_id?: string
  table_name?: string
  description?: string
  field_descriptions?: { field_name: string; description: string }[]
}
export interface KnowledgeTextRequest {
  expected_kb_revision?: string
  table_descriptions: KnowledgeTextPatch[]
}
export interface KnowledgeMapping { description_index: number; object_id: string }
export interface KnowledgeMappingsRequest {
  expected_kb_revision: string
  mappings: KnowledgeMapping[]
}
