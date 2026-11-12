package dbmanager

import (
	"context"
	"fmt"
	"strings"
)

// Supplied by the current server-owned connection or admitted executor. Cached
// JSON cannot assert a database type or establish connection identity.
type schemaCacheAccess struct {
	Key, DatabaseType string
	IdentityKnown     bool
}

func isPostgresMetadataType(kind string) bool {
	return kind == "postgresql" || kind == "yugabytedb" || kind == "timescaledb"
}
func isLegacySchemaType(kind string) bool {
	switch kind {
	case "mysql", "starrocks", "clickhouse", "mongodb", "spreadsheet", "google_sheets":
		return true
	}
	return false
}
func newSchemaCacheAccess(base, kind string, identityKnown bool) schemaCacheAccess {
	if base == "" || !identityKnown || (!isPostgresMetadataType(kind) && !isLegacySchemaType(kind)) {
		return schemaCacheAccess{}
	}
	if isPostgresMetadataType(kind) {
		base += ":metadata-v2"
	}
	return schemaCacheAccess{Key: base, DatabaseType: kind, IdentityKnown: true}
}
func (a schemaCacheAccess) valid() bool {
	if a.Key == "" || !a.IdentityKnown {
		return false
	}
	if isPostgresMetadataType(a.DatabaseType) {
		return strings.HasSuffix(a.Key, ":metadata-v2")
	}
	return isLegacySchemaType(a.DatabaseType)
}
func loadSchemaCache(ctx context.Context, access schemaCacheAccess, retrieve func(context.Context, string) (*SchemaStorage, error)) (*SchemaStorage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Fail before touching old schema:<chatID> when a restart/disconnect leaves
	// no current database identity. Unknown is not an independent legacy source.
	if !access.valid() {
		return nil, fmt.Errorf("current schema cache identity unavailable")
	}
	if retrieve == nil {
		return nil, fmt.Errorf("schema storage unavailable")
	}
	storage, err := retrieve(ctx, access.Key)
	if err != nil {
		return nil, err
	}
	return validateSchemaCache(storage, access)
}
func validateSchemaCache(storage *SchemaStorage, access schemaCacheAccess) (*SchemaStorage, error) {
	if !access.valid() {
		return nil, fmt.Errorf("current schema cache identity unavailable")
	}
	if storage == nil || storage.FullSchema == nil {
		return nil, fmt.Errorf("invalid or empty schema found (first-time schema storage)")
	}
	// Annotate a copy from current trusted context; never mutate a stored object
	// or trust an old marker. PG accepts only complete modern metadata.
	out := *storage
	full := *storage.FullSchema
	full.legacyDatabaseType = ""
	if isPostgresMetadataType(access.DatabaseType) {
		if full.MetadataVersion != queryObjectMetadataVersion {
			return nil, fmt.Errorf("legacy PostgreSQL schema requires refresh")
		}
	} else {
		if full.MetadataVersion != 0 {
			return nil, fmt.Errorf("schema cache database type mismatch")
		}
		full.legacyDatabaseType = access.DatabaseType
	}
	if !full.QueryObjectsReady() {
		return nil, fmt.Errorf("invalid or empty schema found (first-time schema storage)")
	}
	if full.MetadataVersion != 0 && out.ObjectCacheKey != full.ObjectCacheKey(access.Key) {
		return nil, fmt.Errorf("stale query object cache identity")
	}
	out.FullSchema = &full
	return &out, nil
}
