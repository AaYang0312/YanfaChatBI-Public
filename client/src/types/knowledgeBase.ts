// Knowledge Base types matching backend DTOs

export type KnowledgeProvenance = 'legacy_unknown' | 'database_comment' | 'user_edit' | 'ai_generated';
export type KnowledgeAssociationState = 'pending' | 'mapped';

export interface FieldDescription {
  column_type?: string;
  provenance?: KnowledgeProvenance;
  state?: KnowledgeAssociationState;
  updated_at?: string;
  field_name: string;
  description: string;
}

export interface TableDescription {
  object_id?: string;
  object_instance_version?: string;
  provenance?: KnowledgeProvenance;
  state?: KnowledgeAssociationState;
  updated_at?: string;
  table_name: string;
  description: string;
  field_descriptions: FieldDescription[];
}

export interface KnowledgeBaseResponse {
  kb_revision?: string;
  chat_id: string;
  table_descriptions: TableDescription[];
  created_at: string;
  updated_at: string;
}

export interface UpdateKnowledgeBaseRequest {
  expected_kb_revision?: string;
  table_descriptions: TableDescription[];
}
