// Package schemaobjects provides a read-only projection of selected, readable
// database objects. Callers must collect the catalog with the current reader.
package schemaobjects

type Kind string

const (
	Table Kind = "table"
	View  Kind = "view"
)

type Ref struct {
	Schema string `json:"schema" bson:"schema"`
	Name   string `json:"name" bson:"name"`
	Kind   Kind   `json:"kind" bson:"kind"`
}
type Selection struct {
	Version      int    `json:"selection_version" bson:"selection_version"`
	Mode         string `json:"mode" bson:"mode"`
	Objects      []Ref  `json:"objects,omitempty" bson:"objects,omitempty"`
	IncludeKinds []Kind `json:"include_kinds,omitempty" bson:"include_kinds,omitempty"`
}
type Scope struct {
	OwnerID            string `json:"owner_id" bson:"owner_id"`
	ChatID             string `json:"chat_id" bson:"chat_id"`
	SourceKey          string `json:"source_key" bson:"source_key"`
	BindingRevision    int64  `json:"binding_revision" bson:"binding_revision"`
	ConnectionRevision int64  `json:"connection_revision" bson:"connection_revision"`
	SelectionRevision  int64  `json:"selection_revision" bson:"selection_revision"`
}
type Metadata struct {
	Ref              Ref    `json:"ref" bson:"ref"`
	CatalogOID       uint32 `json:"catalog_oid" bson:"catalog_oid"`
	ObjectID         string `json:"object_id" bson:"object_id"`
	InstanceVersion  string `json:"object_instance_version" bson:"object_instance_version"`
	Fingerprint      string `json:"schema_fingerprint" bson:"schema_fingerprint"`
	DefinitionStatus string `json:"definition_status" bson:"definition_status"`
	// Only an optional digest, never SQL text. No definition is needed for readiness.
	DefinitionFingerprint string `json:"definition_fingerprint,omitempty" bson:"definition_fingerprint,omitempty"`
	PartialColumns        bool   `json:"partial_columns" bson:"partial_columns"`
}
type Column struct {
	DefaultValue string `json:"default_value,omitempty" bson:"default_value,omitempty"`
	Name         string `json:"name" bson:"name"`
	Type         string `json:"type" bson:"type"`
	Comment      string `json:"comment,omitempty" bson:"comment,omitempty"`
	Ordinal      int    `json:"ordinal" bson:"ordinal"`
	Nullable     *bool  `json:"nullable" bson:"nullable"`
}
type Constraint struct {
	Name    string   `json:"name" bson:"name"`
	Type    string   `json:"type" bson:"type"`
	Columns []string `json:"columns" bson:"columns"`
}
type Index struct {
	Name    string   `json:"name" bson:"name"`
	Columns []string `json:"columns" bson:"columns"`
	Unique  bool     `json:"unique" bson:"unique"`
}
type ForeignKey struct {
	Name          string   `json:"name" bson:"name"`
	Columns       []string `json:"columns" bson:"columns"`
	Target        Ref      `json:"target" bson:"target"`
	TargetColumns []string `json:"target_columns" bson:"target_columns"`
	OnDelete      string   `json:"on_delete,omitempty" bson:"on_delete,omitempty"`
	OnUpdate      string   `json:"on_update,omitempty" bson:"on_update,omitempty"`
}
type Object struct {
	Metadata       Metadata     `json:"metadata" bson:"metadata"`
	Columns        []Column     `json:"columns" bson:"columns"`
	Comment        string       `json:"comment,omitempty" bson:"comment,omitempty"`
	RowCount       *int64       `json:"row_count" bson:"row_count"`
	RowCountStatus string       `json:"row_count_status" bson:"row_count_status"`
	Constraints    []Constraint `json:"constraints,omitempty" bson:"constraints,omitempty"`
	Indexes        []Index      `json:"indexes,omitempty" bson:"indexes,omitempty"`
	ForeignKeys    []ForeignKey `json:"foreign_keys,omitempty" bson:"foreign_keys,omitempty"`
}
type Instance struct {
	CatalogOID uint32 `json:"catalog_oid" bson:"catalog_oid"`
	Version    string `json:"version" bson:"version"`
	Present    bool   `json:"present" bson:"present"`
}
type Registry map[string]Instance
type Snapshot struct {
	Scope           Scope    `json:"scope" bson:"scope"`
	MetadataVersion int      `json:"metadata_version" bson:"metadata_version"`
	Objects         []Object `json:"objects" bson:"objects"`
	Registry        Registry `json:"registry" bson:"registry"`
	Fingerprint     string   `json:"fingerprint" bson:"fingerprint"`
	Complete        bool     `json:"complete" bson:"complete"`
}
