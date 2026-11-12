package vectordb

import (
	"yanfachatbi/internal/constants"
	"strings"
	"testing"
)

func TestObjectCollectionNamesIsolation(t *testing.T) {
	for _, tc := range []struct{ schema, messages, objects, want string }{
		{"", "", "", constants.SchemaCollectionName + "_objects_v2"},
		{"custom_schema", "custom_messages", "", "custom_schema_objects_v2"},
		{"custom_schema", "custom_messages", "isolated_objects", "isolated_objects"},
	} {
		name, err := ResolveObjectCollectionName(tc.schema, tc.messages, tc.objects)
		if err != nil || name != tc.want {
			t.Fatalf("%+v got %q %v", tc, name, err)
		}
	}
	for _, bad := range []string{constants.SchemaCollectionName, constants.MessageCollectionName, "custom_schema", "custom_messages", "/bad", " a", strings.Repeat("a", 256)} {
		if _, err := ResolveObjectCollectionName("custom_schema", "custom_messages", bad); err == nil {
			t.Fatalf("collision/invalid name accepted: %q", bad)
		}
	}
	if _, err := ResolveObjectCollectionName("custom", "", "objects"); err == nil {
		t.Fatal("invalid existing pair accepted")
	}
	if _, err := ResolveObjectCollectionName("schema", "schema_objects_v2", ""); err == nil {
		t.Fatal("derived name collided with messages")
	}
	a, err := ResolveObjectCollectionName(strings.Repeat("s", 255), "messages", "")
	if err != nil || len(a) > 255 {
		t.Fatalf("long derived name %q %v", a, err)
	}
	b, err := ResolveObjectCollectionName(strings.Repeat("s", 254)+"x", "messages", "")
	if err != nil || a == b {
		t.Fatal("truncated physical names collided")
	}
}
