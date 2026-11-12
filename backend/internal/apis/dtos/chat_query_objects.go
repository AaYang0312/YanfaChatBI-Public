package dtos

import "encoding/json"

// MarshalJSON preserves the legacy numeric row count while representing unknown
// v2 statistics as null. A view is not an empty table.
func (t TableInfo) MarshalJSON() ([]byte, error) {
	type tableInfo TableInfo
	count := &t.RowCount
	if t.RowCountStatus == "unknown" {
		count = nil
	}
	return json.Marshal(struct {
		tableInfo
		Count *int64 `json:"row_count"`
	}{tableInfo(t), count})
}
