package services

import (
	"context"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestChatMetadataToolRejectsNullWithoutAllFallback(t *testing.T) {
	snapshot := objectContextFixture(t)
	callback := generationMetadataTool(func(_ context.Context, args map[string]interface{}) (string, error) {
		if args == nil {
			return "ALL CURRENT OBJECTS", nil
		}
		objects, err := resolveQueryObjectArguments(snapshot, args)
		if err != nil {
			return "", err
		}
		return schemaobjects.Format(objects), nil
	})
	cases := []struct {
		name  string
		args  map[string]interface{}
		valid bool
	}{
		{"null", nil, false}, {"empty", map[string]interface{}{}, false},
		{"both", map[string]interface{}{"table_names": []string{"sales_view"}, "object_refs": []schemaobjects.Ref{}}, false},
		{"unknown", map[string]interface{}{"sql": "SELECT 1"}, false},
		{"unavailable", map[string]interface{}{"table_names": []string{"private_base"}}, false},
		{"name", map[string]interface{}{"table_names": []string{"sales_view"}}, true},
		{"ref", map[string]interface{}{"object_refs": []schemaobjects.Ref{snapshot.Objects[0].Metadata.Ref}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := callback(context.Background(), llm.ToolCall{ID: "call", Name: llm.GetTableInfoToolName, Arguments: tc.args})
			if got == nil || got.CallID != "call" || got.Name != llm.GetTableInfoToolName {
				t.Fatal("missing tool identity")
			}
			if tc.valid {
				if err != nil || got.IsError {
					t.Fatalf("valid metadata rejected: %+v %v", got, err)
				}
			} else if !got.IsError || got.Content == "ALL CURRENT OBJECTS" {
				t.Fatalf("invalid metadata input became all: %+v %v", got, err)
			}
		})
	}
}
