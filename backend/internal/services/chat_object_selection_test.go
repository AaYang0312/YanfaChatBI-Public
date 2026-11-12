package services

import (
	"errors"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestObjectSelectionInput(t *testing.T) {
	v := &schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "reporting", Name: "v", Kind: schemaobjects.View}}}
	legacy := "ALL"
	zero := int64(0)
	if _, _, _, err := prepareObjectSelection(nil, v, &legacy, nil); err == nil {
		t.Fatal("conflicting legacy/v2 accepted")
	}
	selected, compat, changed, err := prepareObjectSelection(nil, v, nil, nil)
	if err != nil || !changed || selected == nil || compat == "" || compat == "ALL" {
		t.Fatalf("view-only initial scope downgraded: %v %q", err, compat)
	}
	chat := &models.Chat{SelectedObjects: v, SelectionRevision: 3}
	if _, _, _, err := prepareObjectSelection(chat, nil, &legacy, nil); !errors.Is(err, ErrSelectionConflict) {
		t.Fatalf("old client overrode v2: %v", err)
	}
	if _, _, _, err := prepareObjectSelection(chat, v, nil, &zero); !errors.Is(err, ErrSelectionConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	current := int64(3)
	if _, _, _, err := prepareObjectSelection(chat, v, nil, &current); err != nil {
		t.Fatal(err)
	}
	empty := &schemaobjects.Selection{Version: 2, Mode: "explicit"}
	if _, _, _, err := prepareObjectSelection(nil, empty, nil, nil); err == nil {
		t.Fatal("empty explicit expanded")
	}
}
