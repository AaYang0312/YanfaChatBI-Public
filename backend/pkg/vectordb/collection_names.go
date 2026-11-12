package vectordb

import (
	"fmt"
	"yanfachatbi/internal/constants"
	"regexp"
)

// CollectionNames binds the logical schema and message indexes to a physical pair.
// It isolates storage, but does not establish the embedding model's provenance.
type CollectionNames struct {
	Schema   string `json:"schema"`
	Messages string `json:"messages"`
}

var safeCollectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)

// ResolveCollectionNames preserves the legacy pair unless both names are set.
// A custom pair cannot reuse either legacy name or share one physical index.
func ResolveCollectionNames(schema, messages string) (CollectionNames, error) {
	legacy := CollectionNames{Schema: constants.SchemaCollectionName, Messages: constants.MessageCollectionName}
	if schema == "" && messages == "" {
		return legacy, nil
	}
	if schema == "" || messages == "" {
		return CollectionNames{}, fmt.Errorf("EMBEDDING_SCHEMA_COLLECTION and EMBEDDING_MESSAGE_COLLECTION must both be set or both be empty")
	}
	names := CollectionNames{Schema: schema, Messages: messages}
	if names.IsDefault() {
		return names, nil
	}
	if !safeCollectionName.MatchString(schema) || !safeCollectionName.MatchString(messages) {
		return CollectionNames{}, fmt.Errorf("embedding collection names must be 1-255 ASCII letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	if schema == messages {
		return CollectionNames{}, fmt.Errorf("embedding schema and message collections must be distinct")
	}
	if schema == legacy.Schema || schema == legacy.Messages || messages == legacy.Schema || messages == legacy.Messages {
		return CollectionNames{}, fmt.Errorf("custom embedding collections cannot reuse either legacy collection name")
	}
	return names, nil
}

func (names CollectionNames) IsDefault() bool {
	return names.Schema == constants.SchemaCollectionName && names.Messages == constants.MessageCollectionName
}
