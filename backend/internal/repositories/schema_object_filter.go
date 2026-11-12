package repositories

import (
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
)

var ErrObjectConflict = errors.New("QUERY_OBJECT_REVISION_CONFLICT")

func revisionMatch(value int64) any {
	if value == 0 {
		return bson.M{"$in": bson.A{nil, int64(0)}}
	}
	return value
}
func objectScopeFilter(scope schemaobjects.Scope) (bson.M, error) {
	owner, err := primitive.ObjectIDFromHex(scope.OwnerID)
	if err != nil {
		return nil, ErrObjectConflict
	}
	chat, err := primitive.ObjectIDFromHex(scope.ChatID)
	if err != nil || scope.SourceKey == "" || scope.SelectionRevision < 0 || scope.BindingRevision < 0 || scope.ConnectionRevision < 0 {
		return nil, ErrObjectConflict
	}
	return bson.M{"_id": chat, "user_id": owner, "source_binding_revision": revisionMatch(scope.BindingRevision), "connection_revision": revisionMatch(scope.ConnectionRevision), "selection_revision": revisionMatch(scope.SelectionRevision)}, nil
}

func objectRebindingFields(previous, next *models.Chat) (bson.M, error) {
	fields := bson.M{}
	if previous == nil || next == nil {
		return nil, ErrObjectConflict
	}
	if previous.SelectedObjects == nil && next.SelectedObjects == nil {
		// Initial legacy ERP creation already persisted this exact selection.
		// Any changed selection must advance the revision in the binding write.
		if next.SelectionRevision == previous.SelectionRevision && next.SelectedCollections == previous.SelectedCollections {
			return fields, nil
		}
		if next.SelectionRevision != previous.SelectionRevision+1 {
			return nil, ErrObjectConflict
		}
		fields["selected_objects"] = nil
		fields["selected_collections"] = next.SelectedCollections
		fields["selection_revision"] = next.SelectionRevision
		return fields, nil
	}
	if next.SelectedObjects == nil || next.SelectedObjects.Version != 2 || next.SelectionRevision != previous.SelectionRevision+1 || next.SelectedCollections == "" || next.SelectedCollections == "ALL" {
		return nil, ErrObjectConflict
	}
	if err := schemaobjects.ValidateSelection(*next.SelectedObjects); err != nil {
		return nil, err
	}
	fields["selected_objects"] = next.SelectedObjects
	fields["selected_collections"] = next.SelectedCollections
	fields["selection_revision"] = next.SelectionRevision
	return fields, nil
}
