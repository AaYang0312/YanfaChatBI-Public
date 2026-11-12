package dtos

import "yanfachatbi/internal/models"

// Updates are patches: omitted descriptions/fields do not delete history.
// Evidence fields from clients are intentionally absent from this DTO.
type KnowledgeBaseFieldEdit struct {
	FieldName   string `json:"field_name" binding:"required"`
	Description string `json:"description"`
}
type KnowledgeBaseDescriptionEdit struct {
	DescriptionIndex  *int                     `json:"description_index,omitempty"`
	ObjectID          string                   `json:"object_id,omitempty"`
	TableName         string                   `json:"table_name,omitempty"`
	Description       *string                  `json:"description,omitempty"`
	FieldDescriptions []KnowledgeBaseFieldEdit `json:"field_descriptions,omitempty"`
}
type UpdateKnowledgeBaseRequest struct {
	ExpectedKBRevision string                         `json:"expected_kb_revision,omitempty"`
	TableDescriptions  []KnowledgeBaseDescriptionEdit `json:"table_descriptions" binding:"required,max=256,dive"`
}

func (r UpdateKnowledgeBaseRequest) LegacyDescriptions() []models.TableDescription {
	rows := make([]models.TableDescription, 0, len(r.TableDescriptions))
	for _, edit := range r.TableDescriptions {
		row := models.TableDescription{TableName: edit.TableName, FieldDescriptions: []models.FieldDescription{}}
		if edit.Description != nil {
			row.Description = *edit.Description
		}
		for _, field := range edit.FieldDescriptions {
			row.FieldDescriptions = append(row.FieldDescriptions, models.FieldDescription{FieldName: field.FieldName, Description: field.Description})
		}
		rows = append(rows, row)
	}
	return rows
}

type KnowledgeBaseResponse struct {
	KBRevision            string                    `json:"kb_revision,omitempty"`
	ChatID                string                    `json:"chat_id"`
	TableDescriptions     []models.TableDescription `json:"table_descriptions"`
	EffectiveDescriptions []models.TableDescription `json:"effective_descriptions,omitempty"`
	QueryObjectsEnabled   bool                      `json:"query_objects_enabled"`
	Saved                 bool                      `json:"saved"`
	RefreshRequired       bool                      `json:"refresh_required,omitempty"`
	IndexStatus           string                    `json:"index_status"`
	CreatedAt             string                    `json:"created_at"`
	UpdatedAt             string                    `json:"updated_at"`
}

// Kept for the independently reviewed single-confirmation helper contract.
type ConfirmKnowledgeBaseMappingRequest struct {
	DescriptionIndex   *int   `json:"description_index" binding:"required,min=0"`
	ObjectID           string `json:"object_id" binding:"required"`
	ExpectedKBRevision string `json:"expected_kb_revision" binding:"required"`
}
type KnowledgeBaseMapping struct {
	DescriptionIndex *int   `json:"description_index" binding:"required,min=0"`
	ObjectID         string `json:"object_id" binding:"required"`
}
type ConfirmKnowledgeBaseMappingsRequest struct {
	ExpectedKBRevision string                 `json:"expected_kb_revision" binding:"required"`
	Mappings           []KnowledgeBaseMapping `json:"mappings" binding:"required,min=1,max=256,dive"`
}
