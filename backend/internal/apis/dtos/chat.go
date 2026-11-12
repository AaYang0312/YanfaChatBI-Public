package dtos

import (
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
)

type CreateChatSettings struct {
	AutoExecuteQuery          *bool `json:"auto_execute_query"`
	ShareDataWithAI           *bool `json:"share_data_with_ai"` // Deprecated input, ignored.
	NonTechMode               *bool `json:"non_tech_mode"`
	AutoGenerateVisualization *bool `json:"auto_generate_visualization"`
	AutoAnalyzeResults        *bool `json:"auto_analyze_results"`
}

type ChatSettingsResponse struct {
	AutoExecuteQuery          bool `json:"auto_execute_query"`
	ShareDataWithAI           bool `json:"share_data_with_ai"` // Deprecated; always false.
	NonTechMode               bool `json:"non_tech_mode"`
	AutoGenerateVisualization bool `json:"auto_generate_visualization"`
	AutoAnalyzeResults        bool `json:"auto_analyze_results"`
}
type CreateConnectionRequest struct {
	Type         string  `json:"type" binding:"required,oneof=postgresql yugabytedb timescaledb mysql starrocks clickhouse mongodb redis neo4j cassandra spreadsheet google_sheets"`
	Host         string  `json:"host"`
	Port         *string `json:"port"`
	Username     string  `json:"username"`
	Password     *string `json:"password"`
	Database     string  `json:"database"`
	AuthDatabase *string `json:"auth_database,omitempty"` // Database to authenticate against (for MongoDB)

	// SSL/TLS Configuration
	UseSSL         bool    `json:"use_ssl"`
	SSLMode        *string `json:"ssl_mode,omitempty"` // type: disable, require, verify-ca, verify-full
	SSLCertURL     *string `json:"ssl_cert_url,omitempty"`
	SSLKeyURL      *string `json:"ssl_key_url,omitempty"`
	SSLRootCertURL *string `json:"ssl_root_cert_url,omitempty"`

	// Google Sheets specific fields
	GoogleSheetID      *string `json:"google_sheet_id,omitempty"`
	GoogleSheetURL     *string `json:"google_sheet_url,omitempty"`
	GoogleAuthToken    *string `json:"google_auth_token,omitempty"`
	GoogleRefreshToken *string `json:"google_refresh_token,omitempty"`
}

type ConnectionResponse struct {
	ID          string  `json:"id" binding:"required"`
	Type        string  `json:"type" binding:"required"`
	Host        string  `json:"host" binding:"required"`
	Port        *string `json:"port"`
	Username    string  `json:"username" binding:"required"`
	Database    string  `json:"database" binding:"required"`
	IsExampleDB bool    `json:"is_example_db"`
	// Password not exposed in response

	// SSL/TLS Configuration
	UseSSL         bool    `json:"use_ssl"`
	SSLMode        *string `json:"ssl_mode,omitempty"` // type: disable, require, verify-ca, verify-full
	SSLCertURL     *string `json:"ssl_cert_url,omitempty"`
	SSLKeyURL      *string `json:"ssl_key_url,omitempty"`
	SSLRootCertURL *string `json:"ssl_root_cert_url,omitempty"`

	// Google Sheets specific fields (no tokens exposed in response)
	GoogleSheetID  *string `json:"google_sheet_id,omitempty"`
	GoogleSheetURL *string `json:"google_sheet_url,omitempty"`
}

type CreateChatRequest struct {
	SelectedObjects     *schemaobjects.Selection `json:"selected_objects,omitempty"`
	SelectedCollections *string                  `json:"selected_collections"` // Omitted preserves ALL; explicit values must be nonempty table names or ALL.
	DisplayName         *string                  `json:"display_name"`
	Connection          CreateConnectionRequest  `json:"connection" binding:"required"`
	Settings            CreateChatSettings       `json:"settings,omitempty"`
}

type UpdateChatRequest struct {
	SelectedObjects     *schemaobjects.Selection `json:"selected_objects,omitempty"`
	SelectionRevision   *int64                   `json:"selection_revision,omitempty"`
	DisplayName         *string                  `json:"display_name"` // nil (omitted/null) preserves; blank clears
	Connection          *CreateConnectionRequest `json:"connection"`
	SelectedCollections *string                  `json:"selected_collections"` // "ALL" or comma-separated table names
	Settings            *CreateChatSettings      `json:"settings"`
	PreferredLLMModel   *string                  `json:"preferred_llm_model"` // User's preferred LLM model for this chat
	// PreferredReasoningLevel selects the thinking intensity for models that
	// declare one; empty means "use the endpoint/model default".
	PreferredReasoningLevel *string `json:"preferred_reasoning_level"`
}

type ChatResponse struct {
	SelectedObjects         *schemaobjects.Selection `json:"selected_objects,omitempty"`
	SelectionRevision       int64                    `json:"selection_revision"`
	QueryObjectIndex        *models.Manifest         `json:"query_object_index,omitempty"`
	ID                      string                   `json:"id"`
	UserID                  string                   `json:"user_id"`
	DisplayName             string                   `json:"display_name,omitempty"`
	SourceID                string                   `json:"source_id,omitempty"`
	SourceBindingRevision   int64                    `json:"source_binding_revision,omitempty"`
	SourceClassification    string                   `json:"source_classification,omitempty"`
	Connection              ConnectionResponse       `json:"connection"`
	SelectedCollections     string                   `json:"selected_collections"`
	CreatedAt               string                   `json:"created_at"`
	UpdatedAt               string                   `json:"updated_at"`
	Settings                ChatSettingsResponse     `json:"settings"`
	PreferredLLMModel       *string                  `json:"preferred_llm_model"`
	PreferredReasoningLevel string                   `json:"preferred_reasoning_level,omitempty"`
}

// ContextStatusResponse exposes compaction state for debugging and UI hints.
type ContextStatusResponse struct {
	PromptBudget          int     `json:"prompt_budget"`
	EstimatedPromptTokens int     `json:"estimated_prompt_tokens"`
	CompactionEnabled     bool    `json:"compaction_enabled"`
	SummaryRevision       int64   `json:"summary_revision"`
	CoveredCount          int64   `json:"covered_count"`
	CoveredUntil          *string `json:"covered_until,omitempty"`
	SummaryTokens         int     `json:"summary_tokens"`
	StaleSource           bool    `json:"stale_source"`
}

type ChatListResponse struct {
	Chats []ChatResponse `json:"chats"`
	Total int64          `json:"total"`
}

// TableInfo represents a table with its columns
type TableInfo struct {
	ObjectID        string             `json:"object_id,omitempty"`
	InstanceVersion string             `json:"object_instance_version,omitempty"`
	SchemaName      string             `json:"schema_name,omitempty"`
	ObjectName      string             `json:"object_name,omitempty"`
	ObjectType      schemaobjects.Kind `json:"object_type,omitempty"`
	SQLIdentifier   string             `json:"sql_identifier,omitempty"`
	RowCountStatus  string             `json:"row_count_status,omitempty"`
	PartialColumns  bool               `json:"partial_columns,omitempty"`
	Name            string             `json:"name"`
	Columns         []ColumnInfo       `json:"columns"`
	IsSelected      bool               `json:"is_selected"`
	RowCount        int64              `json:"row_count"`
	SizeBytes       int64              `json:"size_bytes"`
}

// ColumnInfo represents a column in a table
type ColumnInfo struct {
	Nullable   *bool  `json:"nullable"`
	Ordinal    int    `json:"ordinal,omitempty"`
	Comment    string `json:"comment,omitempty"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	IsNullable bool   `json:"is_nullable"`
}

// TablesResponse represents the response for the get tables API
type TablesResponse struct {
	MetadataVersion     int                      `json:"metadata_version,omitempty"`
	SelectionRevision   int64                    `json:"selection_revision"`
	SelectedObjects     *schemaobjects.Selection `json:"selected_objects,omitempty"`
	QueryObjectsEnabled bool                     `json:"query_objects_enabled"`
	Tables              []TableInfo              `json:"tables"`
}

// Query Recommendations DTOs
type QueryRecommendation struct {
	Text string `json:"text"`
}

type QueryRecommendationsResponse struct {
	Recommendations []QueryRecommendation `json:"recommendations"`
}

// Cached Recommendations DTOs
type CachedQueryRecommendation struct {
	Text   string `json:"text"`
	Picked bool   `json:"picked"`
}

type CachedQueryRecommendations struct {
	Scope           string                      `json:"scope,omitempty"` // Current owner, binding, selection and schema evidence.
	Recommendations []CachedQueryRecommendation `json:"recommendations"`
	CreatedAt       int64                       `json:"created_at"`
}
