package dbmanager

import (
	"context"
	"yanfachatbi/pkg/schemaobjects"
	"reflect"
)

// Legacy LLM table structures cannot represent unknown nullability or views.
// Keep this compatibility table projection sample-free; both public formatters
// consume FullSchema.QueryObjects instead, including exact view output columns.
func createObjectLLMSchema(s *SchemaInfo) *LLMSchemaInfo {
	out := &LLMSchemaInfo{Tables: map[string]LLMTableInfo{}}
	for _, o := range s.QueryObjects(s.ObjectScope.SourceKey) {
		if o.Metadata.Ref.Kind != schemaobjects.Table {
			continue
		}
		t := LLMTableInfo{Name: objectStorageName(o.Metadata.Ref), Description: o.Comment}
		for _, c := range o.Columns {
			col := LLMColumnInfo{Name: c.Name, Type: c.Type, Description: c.Comment}
			if c.Nullable != nil {
				col.IsNullable = *c.Nullable
			}
			t.Columns = append(t.Columns, col)
		}
		out.Tables[t.Name] = t
	}
	return out
}
func (sm *SchemaManager) postgresSchemaChanged(ctx context.Context, chatID string, db DBExecutor) (bool, error) {
	objects, err := (&PostgresDriver{}).GetQueryObjects(ctx, db)
	if err != nil {
		return true, err
	}
	key := sm.executorSchemaKey(chatID, db)
	sm.mu.RLock()
	old := sm.schemaCache[key]
	sm.mu.RUnlock()
	if old == nil && sm.storageService != nil {
		if stored, err := sm.getStoredSchemaAt(ctx, key, "postgresql"); err == nil {
			old = stored.FullSchema
		}
	}
	if old == nil || !old.QueryObjectsReady() {
		return true, nil
	}
	refs := []schemaobjects.Ref{}
	for _, o := range old.QueryObjects(old.ObjectScope.SourceKey) {
		refs = append(refs, o.Metadata.Ref)
	}
	if len(refs) == 0 {
		return true, nil
	}
	selection := schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: refs}
	if old.ObjectSelection != nil {
		selection = *old.ObjectSelection
	}
	selected, err := schemaobjects.Select(objects, selection)
	if err != nil {
		return true, err
	}
	current := schemaFromQueryObjects(selected)
	return !reflect.DeepEqual(old.objectChecksums(), current.objectChecksums()), nil
}
