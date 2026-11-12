package services

import (
	"errors"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
)

var ErrSelectionConflict = errors.New("QUERY_OBJECT_SELECTION_CONFLICT")
var ErrQueryObjectsDisabled = errors.New("QUERY_OBJECTS_DISABLED")

const v2LegacySelection = "__QUERY_OBJECTS_V2__"

// prepareObjectSelection is syntax/authority validation only; readable catalog
// validation is performed under the current source lease before a saved update.
func prepareObjectSelection(chat *models.Chat, v2 *schemaobjects.Selection, legacy *string, expected *int64) (*schemaobjects.Selection, string, bool, error) {
	if v2 != nil && legacy != nil {
		return nil, "", false, schemaobjects.ErrSelection
	}
	currentLegacy := "ALL"
	if chat != nil {
		currentLegacy = chat.SelectedCollections
	}
	if v2 == nil {
		if legacy == nil {
			if chat != nil {
				return chat.SelectedObjects, currentLegacy, false, nil
			}
			return nil, currentLegacy, false, nil
		}
		if chat != nil && chat.SelectedObjects != nil {
			return nil, "", false, ErrSelectionConflict
		}
		value, err := newChatSelection(legacy, currentLegacy)
		return nil, value, true, err
	}
	if v2.Version != 2 {
		return nil, "", false, schemaobjects.ErrSelection
	}
	if err := schemaobjects.ValidateSelection(*v2); err != nil {
		return nil, "", false, err
	}
	if chat != nil && (expected == nil || *expected != chat.SelectionRevision) {
		return nil, "", false, ErrSelectionConflict
	}
	copy := *v2
	copy.Objects = append([]schemaobjects.Ref(nil), v2.Objects...)
	copy.IncludeKinds = append([]schemaobjects.Kind(nil), v2.IncludeKinds...)
	return &copy, v2LegacySelection, true, nil
}
