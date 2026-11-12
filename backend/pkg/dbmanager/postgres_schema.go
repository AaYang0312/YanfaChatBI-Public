package dbmanager

import (
	"context"
	"sort"
)

// PostgresSchemaFetcher is the compatibility entrypoint. It uses exactly the
// same permission-aware, metadata-only path as the registered PostgresDriver.
type PostgresSchemaFetcher struct{ db DBExecutor }

func (f *PostgresSchemaFetcher) FetchSchema(ctx context.Context) (*SchemaInfo, error) {
	return (&PostgresDriver{}).GetSchema(ctx, f.db, []string{"ALL"})
}
func (f *PostgresSchemaFetcher) GetSchema(ctx context.Context, db DBExecutor, selectedTables []string) (*SchemaInfo, error) {
	return (&PostgresDriver{}).GetSchema(ctx, db, selectedTables)
}
func (f *PostgresSchemaFetcher) FetchTableList(ctx context.Context) ([]string, error) {
	s, err := f.FetchSchema(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(s.Tables))
	for name := range s.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func (f *PostgresSchemaFetcher) GetTableChecksum(ctx context.Context, table string) (string, error) {
	return (&PostgresDriver{}).GetTableChecksum(ctx, f.db, table)
}

// Explicit data API only; schema discovery never calls this method.
func (f *PostgresSchemaFetcher) FetchExampleRecords(ctx context.Context, db DBExecutor, table string, limit int) ([]map[string]interface{}, error) {
	return (&PostgresDriver{}).FetchExampleRecords(ctx, db, table, limit)
}
