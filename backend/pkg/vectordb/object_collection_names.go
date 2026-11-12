package vectordb

import (
	"crypto/sha256"
	"fmt"
	"yanfachatbi/internal/constants"
)

// ResolveObjectCollectionName derives v2 storage from the configured physical
// schema collection. It neither renames messages nor changes embedding settings.
func ResolveObjectCollectionName(schema, messages, objects string) (string, error) {
	pair, err := ResolveCollectionNames(schema, messages)
	if err != nil {
		return "", err
	}
	if objects == "" {
		const suffix = "_objects_v2"
		base := pair.Schema
		if len(base)+len(suffix) > 255 {
			digest := sha256.Sum256([]byte(base))
			base = fmt.Sprintf("%s_%x", base[:255-len(suffix)-17], digest[:8])
		}
		objects = base + suffix
	}
	if !safeCollectionName.MatchString(objects) {
		return "", fmt.Errorf("invalid EMBEDDING_OBJECT_SCHEMA_COLLECTION")
	}
	for _, reserved := range []string{pair.Schema, pair.Messages, constants.SchemaCollectionName, constants.MessageCollectionName} {
		if objects == reserved {
			return "", fmt.Errorf("object collection must be isolated from schema and message collections")
		}
	}
	return objects, nil
}
