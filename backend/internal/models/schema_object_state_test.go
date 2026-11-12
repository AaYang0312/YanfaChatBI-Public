package models

import (
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestObjectSelectionWireRoundTrip(t *testing.T) {
	chat := NewChat(primitive.NewObjectID(), Connection{Type: "postgresql"}, DefaultChatSettings())
	chat.SelectedObjects = &schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "Reports.X", Name: "Sales, \"Q\"", Kind: schemaobjects.View}}}
	chat.SelectionRevision = 3
	chat.ObjectState = &State{Revision: 8, Scope: schemaobjects.Scope{SourceKey: "source", SelectionRevision: 3}, Manifest: Manifest{FormatVersion: 2, Status: "ready", ActiveGeneration: "generation", ExpectedPoints: 1}}
	raw, err := bson.Marshal(chat)
	if err != nil {
		t.Fatal(err)
	}
	var restored Chat
	if err := bson.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.SelectedObjects == nil || restored.SelectedObjects.Objects[0].Name != "Sales, \"Q\"" || restored.ObjectState.Revision != 8 || restored.SelectionRevision != 3 {
		t.Fatalf("BSON lost selection/state: %#v", restored)
	}
	raw, err = json.Marshal(chat)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	selected := wire["selected_objects"].(map[string]any)
	if selected["selection_version"] != float64(2) || wire["selection_revision"] != float64(3) {
		t.Fatalf("wrong JSON wire: %s", raw)
	}
	if _, ok := wire["object_state"]; ok {
		t.Fatal("private registry leaked into Chat JSON")
	}
}
func TestObjectSelectionLegacyAndV2Authority(t *testing.T) {
	chat := NewChat(primitive.NewObjectID(), Connection{}, DefaultChatSettings())
	selection, err := chat.QuerySelection()
	if err != nil || selection.Mode != "all" || len(selection.IncludeKinds) != 1 || selection.IncludeKinds[0] != schemaobjects.Table {
		t.Fatal("legacy ALL expanded")
	}
	chat.SelectedObjects = &schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "public", Name: "v", Kind: schemaobjects.View}}}
	selection, err = chat.QuerySelection()
	if err != nil || selection.Objects[0].Kind != schemaobjects.View {
		t.Fatal("v2 view downgraded to legacy ALL")
	}
	selection.Objects[0].Name = "mutated"
	if chat.SelectedObjects.Objects[0].Name != "v" {
		t.Fatal("selection projection mutated stored authority")
	}
	chat.SelectedObjects.Objects = nil
	if _, err := chat.QuerySelection(); err == nil {
		t.Fatal("invalid v2 fell back to legacy ALL")
	}
}
