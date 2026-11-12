package models

import (
	"time"

	"yanfachatbi/pkg/schemaobjects"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// KnowledgeProvenance records origin, never business certification.
type KnowledgeProvenance string

const (
	KnowledgeLegacyUnknown   KnowledgeProvenance = "legacy_unknown"
	KnowledgeDatabaseComment KnowledgeProvenance = "database_comment"
	KnowledgeUserEdit        KnowledgeProvenance = "user_edit"
	KnowledgeAIGenerated     KnowledgeProvenance = "ai_generated"
)

type KnowledgeAssociationState string

const (
	KnowledgePending KnowledgeAssociationState = "pending"
	KnowledgeMapped  KnowledgeAssociationState = "mapped"
)

// FieldDescription stores the user-provided (or AI-generated) description for a single field/column.
type FieldDescription struct {
	// ColumnType is server-stamped when mapping; missing/changed types stay pending.
	ColumnType  string                    `bson:"column_type,omitempty" json:"column_type,omitempty"`
	Provenance  KnowledgeProvenance       `bson:"provenance,omitempty" json:"provenance,omitempty"`
	State       KnowledgeAssociationState `bson:"state,omitempty" json:"state,omitempty"`
	UpdatedAt   time.Time                 `bson:"updated_at,omitempty" json:"updated_at,omitempty"`
	FieldName   string                    `bson:"field_name" json:"field_name"`
	Description string                    `bson:"description" json:"description"`
}

// TableDescription stores the description for a table/collection and its fields.
type TableDescription struct {
	ObjectID        string `bson:"object_id,omitempty" json:"object_id,omitempty"`
	InstanceVersion string `bson:"object_instance_version,omitempty" json:"object_instance_version,omitempty"`
	// ObjectScope is server-stamped evidence. Selection is checked against the
	// current Snapshot; a later selection revision alone does not sever identity.
	ObjectScope       *schemaobjects.Scope      `bson:"object_scope,omitempty" json:"object_scope,omitempty"`
	Provenance        KnowledgeProvenance       `bson:"provenance,omitempty" json:"provenance,omitempty"`
	State             KnowledgeAssociationState `bson:"state,omitempty" json:"state,omitempty"`
	UpdatedAt         time.Time                 `bson:"updated_at,omitempty" json:"updated_at,omitempty"`
	TableName         string                    `bson:"table_name" json:"table_name"`
	Description       string                    `bson:"description" json:"description"`
	FieldDescriptions []FieldDescription        `bson:"field_descriptions" json:"field_descriptions"`
}

// KnowledgeBase stores the per-chat knowledge base consisting of table and field descriptions.
// One document per chat in the `knowledge_bases` MongoDB collection.
type KnowledgeBase struct {
	StorageRevision  int64 `bson:"storage_revision,omitempty" json:"-"`
	KnowledgeVersion int   `bson:"knowledge_version,omitempty" json:"-"`
	// KBRevision is a derived token, recomputed with the current Snapshot before use.
	KBRevision        string             `bson:"kb_revision,omitempty" json:"kb_revision,omitempty"`
	ChatID            primitive.ObjectID `bson:"chat_id" json:"chat_id"`
	UserID            primitive.ObjectID `bson:"user_id" json:"user_id"`
	TableDescriptions []TableDescription `bson:"table_descriptions" json:"table_descriptions"`
	Base              `bson:",inline"`
}

// NewKnowledgeBase creates a new KnowledgeBase instance for a chat.
func NewKnowledgeBase(chatID primitive.ObjectID) *KnowledgeBase {
	return &KnowledgeBase{
		ChatID:            chatID,
		TableDescriptions: []TableDescription{},
		Base:              NewBase(),
	}
}

// GetTableDescription returns the description for a specific table, or nil if not found.
func (kb *KnowledgeBase) GetTableDescription(tableName string) *TableDescription {
	for i := range kb.TableDescriptions {
		if kb.TableDescriptions[i].TableName == tableName {
			return &kb.TableDescriptions[i]
		}
	}
	return nil
}
