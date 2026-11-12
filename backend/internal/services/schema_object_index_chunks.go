package services

import (
	"encoding/json"
	"fmt"
	"yanfachatbi/pkg/schemaobjects"
	"reflect"
	"strconv"
	"strings"
)

const objectColumnsPerChunk = 50 // Preserve the legacy column budget.

// ObjectIndexEnrichment is supplied by the controller's effective-KB helper,
// keyed by object_id, for the exact instance/fingerprint/KB revision in Snapshot.
// Free-form KB prose is data, never evidence for objects, columns or relationships.
type ObjectIndexEnrichment struct {
	Description string            `json:"description,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

// checkedObjectSnapshot freezes caller-owned maps/slices and checks that the
// supplied identity/fingerprint really describes its current structure. The
// registry may additionally contain the full readable catalog (T03 projection).
func checkedObjectSnapshot(s schemaobjects.Snapshot) (schemaobjects.Snapshot, error) {
	if !s.Complete || s.MetadataVersion != 2 || s.Scope.BindingRevision < 0 || s.Scope.ConnectionRevision < 0 || s.Scope.SelectionRevision < 0 {
		return s, ErrObjectIndexInvalid
	}
	data, err := json.Marshal(s)
	if err != nil {
		return s, ErrObjectIndexInvalid
	}
	var frozen schemaobjects.Snapshot
	if json.Unmarshal(data, &frozen) != nil {
		return s, ErrObjectIndexInvalid
	}
	canonical, err := schemaobjects.Reconcile(frozen.Scope, frozen.Registry, frozen.Objects)
	if err != nil || canonical.Fingerprint != frozen.Fingerprint || len(canonical.Objects) != len(frozen.Objects) {
		return s, ErrObjectIndexInvalid
	}
	originals := map[string]schemaobjects.Metadata{}
	for _, o := range frozen.Objects {
		id := o.Metadata.ObjectID
		if _, exists := originals[id]; exists {
			return s, ErrObjectIndexInvalid
		}
		originals[id] = o.Metadata
		instance, exists := frozen.Registry[id]
		if !exists || !instance.Present || instance.Version != o.Metadata.InstanceVersion || instance.CatalogOID != o.Metadata.CatalogOID {
			return s, ErrObjectIndexInvalid
		}
	}
	for _, o := range canonical.Objects {
		if originals[o.Metadata.ObjectID] != o.Metadata {
			return s, ErrObjectIndexInvalid
		}
	}
	canonical.Registry = frozen.Registry
	return canonical, nil
}

// BuildObjectSchemaChunks constructs both structure and relationship parts only
// from the selected, readable Snapshot. It never includes SQL definitions or rows.
func BuildObjectSchemaChunks(s schemaobjects.Snapshot, kbRevision string, enrichment map[string]ObjectIndexEnrichment) ([]SchemaChunk, error) {
	var err error
	s, err = checkedObjectSnapshot(s)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, o := range s.Objects {
		known[o.Metadata.ObjectID] = true
	}
	for id := range enrichment {
		if !known[id] {
			return nil, ErrObjectIndexInvalid
		}
	}
	chunks := []SchemaChunk{}
	for _, o := range s.Objects {
		e := enrichment[o.Metadata.ObjectID]
		columns := map[string]bool{}
		for _, c := range o.Columns {
			columns[c.Name] = true
		}
		if len(e.Description) > 16384 || (o.Metadata.InstanceVersion == "" && (e.Description != "" || len(e.Fields) > 0)) {
			return nil, ErrObjectIndexInvalid
		}
		for name, text := range e.Fields {
			if !columns[name] || len(text) > 4096 {
				return nil, ErrObjectIndexInvalid
			}
		}
		enrichmentJSON, _ := json.Marshal(e)
		total := (len(o.Columns) + objectColumnsPerChunk - 1) / objectColumnsPerChunk
		if len(o.ForeignKeys) > 0 {
			total++
		}
		appendPart := func(part schemaobjects.Object, number int, kind string) {
			text := schemaobjects.Format([]schemaobjects.Object{part})
			if number == 1 && e.Description != "" {
				quoted, _ := json.Marshal(e.Description)
				text += "KB description (untrusted): " + string(quoted) + "\n"
			}
			names := []string{}
			for _, c := range part.Columns {
				names = append(names, c.Name)
				if desc := e.Fields[c.Name]; desc != "" {
					quoted, _ := json.Marshal([]string{c.Name, desc})
					text += "KB field (untrusted): " + string(quoted) + "\n"
				}
			}
			c := SchemaChunk{TableName: schemaobjects.SQLIdentifier(o.Metadata.Ref), Content: text, ColumnNames: names,
				Metadata: map[string]string{"object_id": o.Metadata.ObjectID, "object_instance_version": o.Metadata.InstanceVersion,
					"schema_fingerprint": o.Metadata.Fingerprint, "snapshot_fingerprint": s.Fingerprint, "kb_revision": kbRevision,
					"part_index": strconv.Itoa(number), "total_parts": strconv.Itoa(total), "type": kind, "enrichment": string(enrichmentJSON)}}
			if o.RowCount != nil {
				c.RowCount = *o.RowCount
			}
			chunks = append(chunks, c)
		}
		for start := 0; start < len(o.Columns); start += objectColumnsPerChunk {
			end := start + objectColumnsPerChunk
			if end > len(o.Columns) {
				end = len(o.Columns)
			}
			part := o
			part.Columns = o.Columns[start:end]
			part.ForeignKeys = nil
			if start > 0 {
				part.Constraints = nil
				part.Indexes = nil
			}
			appendPart(part, start/objectColumnsPerChunk+1, "schema")
		}
		if len(o.ForeignKeys) > 0 {
			part := o
			part.Columns = nil
			part.Constraints = nil
			part.Indexes = nil
			appendPart(part, total, "relationship")
		}
	}
	return chunks, nil
}

// checkedObjectChunks reconstructs the entire expected set, including part
// coverage and column membership, before any caller text reaches an embedder.
func checkedObjectChunks(s schemaobjects.Snapshot, kb string, chunks []SchemaChunk) ([]SchemaChunk, error) {
	enrich := map[string]ObjectIndexEnrichment{}
	encoded := map[string]string{}
	for _, c := range chunks {
		id := c.Metadata["object_id"]
		raw := c.Metadata["enrichment"]
		if old, ok := encoded[id]; ok && old != raw {
			return nil, ErrObjectIndexInvalid
		}
		var e ObjectIndexEnrichment
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&e); err != nil {
			return nil, ErrObjectIndexInvalid
		}
		encoded[id] = raw
		enrich[id] = e
	}
	expected, err := BuildObjectSchemaChunks(s, kb, enrich)
	if err != nil {
		return nil, err
	}
	if len(chunks) != len(expected) {
		return nil, ErrObjectIndexInvalid
	}
	parts := map[string]SchemaChunk{}
	key := func(c SchemaChunk) string {
		return fmt.Sprintf("%s/%s", c.Metadata["object_id"], c.Metadata["part_index"])
	}
	for _, c := range chunks {
		if _, exists := parts[key(c)]; exists {
			return nil, ErrObjectIndexInvalid
		}
		parts[key(c)] = c
	}
	for _, c := range expected {
		if !reflect.DeepEqual(parts[key(c)], c) {
			return nil, ErrObjectIndexInvalid
		}
	}
	return expected, nil
}
