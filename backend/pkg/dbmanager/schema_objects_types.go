package dbmanager

import (
	"yanfachatbi/pkg/schemaobjects"
	"time"
)

// SchemaInfo represents database schema information
type SchemaInfo struct {
	// Set only after fresh collection or a cache read with current connection
	// identity and an explicitly supported non-PG type. Never restored from JSON.
	legacyDatabaseType string
	ObjectSelection    *schemaobjects.Selection  `json:"object_selection,omitempty"`
	MetadataVersion    int                       `json:"metadata_version,omitempty"`
	MetadataComplete   bool                      `json:"metadata_complete,omitempty"`
	SelectionRevision  int64                     `json:"selection_revision,omitempty"`
	ObjectScope        schemaobjects.Scope       `json:"object_scope,omitempty"`
	Tables             map[string]TableSchema    `json:"tables"`
	Views              map[string]ViewSchema     `json:"views,omitempty"`
	Sequences          map[string]SequenceSchema `json:"sequences,omitempty"`
	Enums              map[string]EnumSchema     `json:"enums,omitempty"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	Checksum           string                    `json:"checksum"`
}

type TableSchema struct {
	Metadata       *schemaobjects.Metadata   `json:"metadata,omitempty"`
	RowCountStatus string                    `json:"row_count_status,omitempty"`
	Name           string                    `json:"name"`
	Columns        map[string]ColumnInfo     `json:"columns"`
	Indexes        map[string]IndexInfo      `json:"indexes"`
	ForeignKeys    map[string]ForeignKey     `json:"foreign_keys"`
	Constraints    map[string]ConstraintInfo `json:"constraints"`
	Comment        string                    `json:"comment,omitempty"`
	Checksum       string                    `json:"checksum"`
	RowCount       int64                     `json:"row_count"`
	SizeBytes      int64                     `json:"size_bytes"`
}

type ColumnInfo struct {
	Ordinal      int    `json:"ordinal,omitempty"`
	Nullable     *bool  `json:"nullable"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	IsNullable   bool   `json:"is_nullable"`
	DefaultValue string `json:"default_value,omitempty"`
	Comment      string `json:"comment,omitempty"`
}

type IndexInfo struct {
	Name     string   `json:"name"`
	Columns  []string `json:"columns"`
	IsUnique bool     `json:"is_unique"`
}

type ForeignKey struct {
	Columns    []string           `json:"columns,omitempty"`
	RefColumns []string           `json:"ref_columns,omitempty"`
	Target     *schemaobjects.Ref `json:"target,omitempty"`
	Name       string             `json:"name"`
	ColumnName string             `json:"column_name"`
	RefTable   string             `json:"ref_table"`
	RefColumn  string             `json:"ref_column"`
	OnDelete   string             `json:"on_delete"`
	OnUpdate   string             `json:"on_update"`
}

// SchemaDiff represents changes in schema
type SchemaDiff struct {
	AddedViews      []string             `json:"added_views,omitempty"`
	RemovedViews    []string             `json:"removed_views,omitempty"`
	ModifiedViews   []string             `json:"modified_views,omitempty"`
	MetadataChanged bool                 `json:"metadata_changed,omitempty"`
	AddedTables     []string             `json:"added_tables,omitempty"`
	RemovedTables   []string             `json:"removed_tables,omitempty"`
	ModifiedTables  map[string]TableDiff `json:"modified_tables,omitempty"`
	UpdatedAt       time.Time            `json:"updated_at"`
	IsFirstTime     bool                 `json:"is_first_time,omitempty"`
	FullSchema      *SchemaInfo          `json:"full_schema,omitempty"`
}

type TableDiff struct {
	AddedConstraints   []string `json:"added_constraints,omitempty"`
	RemovedConstraints []string `json:"removed_constraints,omitempty"`
	MetadataChanged    bool     `json:"metadata_changed,omitempty"`
	AddedColumns       []string `json:"added_columns,omitempty"`
	RemovedColumns     []string `json:"removed_columns,omitempty"`
	ModifiedColumns    []string `json:"modified_columns,omitempty"`
	AddedIndexes       []string `json:"added_indexes,omitempty"`
	RemovedIndexes     []string `json:"removed_indexes,omitempty"`
	AddedFKs           []string `json:"added_fks,omitempty"`
	RemovedFKs         []string `json:"removed_fks,omitempty"`
}

// SchemaStorage handles efficient schema storage
type SchemaStorage struct {
	ObjectCacheKey string `json:"object_cache_key,omitempty"`
	// Full schema with all details (for diffing and internal use)
	FullSchema *SchemaInfo `json:"full_schema"`

	// Simplified schema for LLM (only essential info)
	LLMSchema *LLMSchemaInfo `json:"llm_schema"`

	// Table-level checksums for quick change detection
	TableChecksums map[string]string `json:"table_checksums"`

	UpdatedAt time.Time `json:"updated_at"`
}

// LLMSchemaInfo is a simplified schema representation for the LLM
type LLMSchemaInfo struct {
	Tables        map[string]LLMTableInfo `json:"tables"`
	Relationships []SchemaRelationship    `json:"relationships"`
}

type LLMTableInfo struct {
	Name           string                   `json:"name"`
	Description    string                   `json:"description"`
	Columns        []LLMColumnInfo          `json:"columns"`
	PrimaryKey     string                   `json:"primary_key,omitempty"`
	RowCount       int64                    `json:"row_count"`
	ExampleRecords []map[string]interface{} `json:"example_records,omitempty"`
}

type LLMColumnInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	IsNullable  bool   `json:"is_nullable"`
	IsIndexed   bool   `json:"is_indexed,omitempty"`
}

type SchemaRelationship struct {
	FromTable string `json:"from_table"`
	ToTable   string `json:"to_table"`
	Type      string `json:"type"`              // "one_to_one", "one_to_many", etc.
	Through   string `json:"through,omitempty"` // For many-to-many relationships
}

// Add new schema types
type ViewSchema struct {
	Metadata       *schemaobjects.Metadata `json:"metadata,omitempty"`
	Columns        map[string]ColumnInfo   `json:"columns,omitempty"`
	Comment        string                  `json:"comment,omitempty"`
	RowCount       *int64                  `json:"row_count"`
	RowCountStatus string                  `json:"row_count_status,omitempty"`
	Checksum       string                  `json:"checksum,omitempty"`
	Name           string                  `json:"name"`
	Definition     string                  `json:"definition"`
}

type SequenceSchema struct {
	Name       string `json:"name"`
	StartValue int64  `json:"start_value"`
	Increment  int64  `json:"increment"`
	MinValue   int64  `json:"min_value"`
	MaxValue   int64  `json:"max_value"`
	CacheSize  int64  `json:"cache_size"`
	IsCycled   bool   `json:"is_cycled"`
}

type EnumSchema struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
	Schema string   `json:"schema"`
}

type ConstraintInfo struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Definition string   `json:"definition,omitempty"`
	Columns    []string `json:"columns,omitempty"`
}
