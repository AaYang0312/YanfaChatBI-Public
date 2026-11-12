package vectordb

import (
	"strings"
	"testing"
)

func TestResolveCollectionNames(t *testing.T) {
	for _, tc := range []struct {
		name, schema, messages string
		want                   CollectionNames
		invalid                bool
	}{
		{name: "default", want: CollectionNames{Schema: "neobase_schema", Messages: "neobase_messages"}},
		{name: "explicit default", schema: "neobase_schema", messages: "neobase_messages", want: CollectionNames{Schema: "neobase_schema", Messages: "neobase_messages"}},
		{name: "isolated", schema: "ERP_schema-v2", messages: "ERP_messages-v2", want: CollectionNames{Schema: "ERP_schema-v2", Messages: "ERP_messages-v2"}},
		{name: "missing message", schema: "schema_v2", invalid: true},
		{name: "missing schema", messages: "messages_v2", invalid: true},
		{name: "duplicate", schema: "same", messages: "same", invalid: true},
		{name: "crossed", schema: "neobase_messages", messages: "neobase_schema", invalid: true},
		{name: "reuse schema", schema: "neobase_schema", messages: "messages_v2", invalid: true},
		{name: "reuse message", schema: "schema_v2", messages: "neobase_messages", invalid: true},
		{name: "cross reuse schema", schema: "schema_v2", messages: "neobase_schema", invalid: true},
		{name: "cross reuse message", schema: "neobase_messages", messages: "messages_v2", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCollectionNames(tc.schema, tc.messages)
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted unsafe collection pair")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
			wantDefault := tc.name == "default" || tc.name == "explicit default"
			if got.IsDefault() != wantDefault {
				t.Fatalf("IsDefault = %v", got.IsDefault())
			}
		})
	}
	for _, bad := range []string{" ", " padded", "trailing ", "a/b", "a\\b", "a.b", "-leading", "_leading", "中文", "name\n", strings.Repeat("x", 256)} {
		for _, schemaBad := range []bool{true, false} {
			schema, messages := "safe_schema", "safe_messages"
			if schemaBad {
				schema = bad
			} else {
				messages = bad
			}
			if _, err := ResolveCollectionNames(schema, messages); err == nil {
				t.Errorf("accepted bad name %q", bad)
			}
		}
	}
}

func TestNewQdrantClientRejectsInvalidPair(t *testing.T) {
	client, err := NewQdrantClient(QdrantConfig{Host: "unused.invalid", Port: "6334", SchemaCollection: "schema_v2"})
	if client != nil {
		client.Close()
		t.Fatal("constructed client for partial pair")
	}
	if err == nil {
		t.Fatal("partial pair must fail before any connection")
	}
}
