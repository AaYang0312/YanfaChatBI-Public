package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"math"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"strconv"
)

func objectScopeFilter(s schemaobjects.Scope, generation string) map[string]string {
	return map[string]string{"owner_id": s.OwnerID, "chat_id": s.ChatID, "source_key": s.SourceKey,
		"source_binding_revision": strconv.FormatInt(s.BindingRevision, 10), "connection_revision": strconv.FormatInt(s.ConnectionRevision, 10),
		"selection_revision": strconv.FormatInt(s.SelectionRevision, 10), "index_format_version": "2", "generation_id": generation}
}
func objectFilterConditions(s schemaobjects.Scope, generation string) []vectordb.FilterCondition {
	result := []vectordb.FilterCondition{}
	for k, v := range objectScopeFilter(s, generation) {
		result = append(result, vectordb.FilterCondition{Key: k, Value: v})
	}
	return result
}
func objectPointID(scope schemaobjects.Scope, generation string, o schemaobjects.Object, part string) string {
	b, _ := json.Marshal([]any{"query-object-v2", scope, generation, o.Metadata.ObjectID, o.Metadata.InstanceVersion, part})
	return uuid.NewSHA1(uuid.NameSpaceOID, b).String()
}
func objectContentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func objectChunkPayload(s schemaobjects.Snapshot, kb, generation string, o schemaobjects.Object, c SchemaChunk) map[string]interface{} {
	p := map[string]interface{}{}
	for k, v := range objectScopeFilter(s.Scope, generation) {
		p[k] = v
	}
	p["object_id"] = o.Metadata.ObjectID
	p["object_instance_version"] = o.Metadata.InstanceVersion
	p["object_type"] = string(o.Metadata.Ref.Kind)
	p["schema_name"] = o.Metadata.Ref.Schema
	p["object_name"] = o.Metadata.Ref.Name
	p["table_name"] = schemaobjects.SQLIdentifier(o.Metadata.Ref)
	p["sql_identifier"] = schemaobjects.SQLIdentifier(o.Metadata.Ref)
	p["schema_fingerprint"] = o.Metadata.Fingerprint
	p["snapshot_fingerprint"] = s.Fingerprint
	p["kb_revision"] = kb
	p["part_index"] = c.Metadata["part_index"]
	p["total_parts"] = c.Metadata["total_parts"]
	p["type"] = c.Metadata["type"]
	p["content"] = c.Content
	p["content_digest"] = objectContentDigest(c.Content)
	p["row_count_status"] = o.RowCountStatus
	if o.RowCount != nil {
		p["row_count"] = *o.RowCount
	}
	// JSON keeps delimiter-containing identifiers unambiguous; no legacy join/split.
	columns, _ := json.Marshal(c.ColumnNames)
	p["column_names"] = string(columns)
	return p
}
func validObjectVector(v []float32, dimension int) bool {
	if dimension <= 0 || len(v) != dimension {
		return false
	}
	nonzero := false
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return false
		}
		nonzero = nonzero || x != 0
	}
	return nonzero
}
func completeObjectPoints(expected, actual []vectordb.VectorPoint, dimension int) bool {
	if len(expected) != len(actual) {
		return false
	}
	points := map[string]vectordb.VectorPoint{}
	for _, p := range expected {
		points[p.ID] = p
	}
	for _, p := range actual {
		want, ok := points[p.ID]
		if !ok || !validObjectVector(p.Vector, dimension) {
			return false
		}
		// Compare all persisted version/part/type/content fields. Qdrant normalizes
		// cosine vectors, so vector values are intentionally not compared bytewise.
		a, e1 := json.Marshal(p.Payload)
		b, e2 := json.Marshal(want.Payload)
		if e1 != nil || e2 != nil || string(a) != string(b) {
			return false
		}
		delete(points, p.ID)
	}
	return len(points) == 0
}
