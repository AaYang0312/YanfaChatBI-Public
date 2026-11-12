package schemaobjects

import "encoding/json"

// MarshalJSON exposes the display type without a second mutable kind.
// BSON persistence retains Ref.Kind as the canonical value; API and index
// payload adapters derive object_type from it at their output boundary.
func (m Metadata) MarshalJSON() ([]byte, error) {
	type metadata Metadata
	return json.Marshal(struct {
		metadata
		ObjectType Kind `json:"object_type"`
	}{metadata: metadata(m), ObjectType: m.Ref.Kind})
}
