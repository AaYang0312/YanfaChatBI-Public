package repositories

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
)

type ObjectStateRepository interface {
	Read(context.Context, schemaobjects.Scope) (*models.State, error)
	CompareAndSwap(context.Context, schemaobjects.Scope, int64, models.State) error
}
type ObjectSelectionRepository interface {
	UpdateObjectSelectionCAS(context.Context, *models.Chat, *schemaobjects.Selection, string) error
}

func (r *chatRepository) Read(ctx context.Context, scope schemaobjects.Scope) (*models.State, error) {
	filter, err := objectScopeFilter(scope)
	if err != nil {
		return nil, err
	}
	var chat models.Chat
	if err = r.primaryChats().FindOne(ctx, filter, options.FindOne().SetProjection(bson.M{"object_state": 1})).Decode(&chat); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrObjectConflict
		}
		return nil, err
	}
	if chat.ObjectState == nil {
		return &models.State{Scope: scope, Registry: schemaobjects.Registry{}}, nil
	}
	if chat.ObjectState.Scope.SourceKey != "" && chat.ObjectState.Scope.SourceKey != scope.SourceKey {
		return nil, ErrObjectConflict
	}
	return chat.ObjectState, nil
}
func (r *chatRepository) CompareAndSwap(ctx context.Context, scope schemaobjects.Scope, expected int64, next models.State) error {
	if next.Revision != expected+1 || expected < 0 || next.Scope != scope {
		return ErrObjectConflict
	}
	filter, err := objectScopeFilter(scope)
	if err != nil {
		return err
	}
	filter["object_state.revision"] = revisionMatch(expected)
	if expected > 0 {
		filter["object_state.scope.source_key"] = scope.SourceKey
	}
	err = r.patchChat(ctx, filter, bson.M{"object_state": next})
	if errors.Is(err, ErrChatBindingConflict) {
		return ErrObjectConflict
	}
	return err
}
func (r *chatRepository) UpdateObjectSelectionCAS(ctx context.Context, previous *models.Chat, selection *schemaobjects.Selection, legacy string) error {
	if previous == nil {
		return ErrObjectConflict
	}
	if selection != nil {
		if selection.Version != 2 {
			return schemaobjects.ErrSelection
		}
		if err := schemaobjects.ValidateSelection(*selection); err != nil {
			return err
		}
	} else if previous.SelectedObjects != nil {
		return ErrObjectConflict
	}
	filter := chatBindingFilter(previous)
	filter["selection_revision"] = revisionMatch(previous.SelectionRevision)
	fields := bson.M{"selected_objects": selection, "selected_collections": legacy,
		"selection_revision": previous.SelectionRevision + 1, "connection.current_schema": nil, "connection.schema_updated_at": nil}
	// Do not overwrite the registry from a stale in-memory Chat. Old index workers
	// lose their selection-revision CAS; readers reject their old manifest scope.
	err := r.patchChat(ctx, filter, fields)
	if errors.Is(err, ErrChatBindingConflict) {
		return ErrObjectConflict
	}
	return err
}

type ObjectConnectionRepository interface {
	UpdateIndependentObjectConnectionCAS(context.Context, *models.Chat, *models.Chat) error
}

func (r *chatRepository) UpdateIndependentObjectConnectionCAS(ctx context.Context, previous, next *models.Chat) error {
	if previous == nil || next == nil || previous.ID != next.ID || previous.UserID != next.UserID || previous.ERPAssociated || previous.SourceID != "" || previous.SourceBindingRevision != 0 {
		return ErrObjectConflict
	}
	fields, err := objectRebindingFields(previous, next)
	if err != nil {
		return err
	}
	if next.SelectedObjects == nil {
		return ErrObjectConflict
	}
	connection := next.Connection.Clone()
	connection.CurrentSchema, connection.SchemaUpdatedAt = nil, nil
	fields["connection"] = connection
	fields["connection_revision"] = previous.ConnectionRevision + 1
	fields["object_state"] = nil
	return r.patchChat(ctx, chatBindingFilter(previous), fields)
}

// LegacyConnectionRepository is optional for existing ChatRepository adapters.
// Connection changes through Update require this atomic capability; they must
// never fall back to separately persisting selection and then the connection.
type LegacyConnectionRepository interface {
	UpdateIndependentLegacyConnectionCAS(context.Context, *models.Chat, *models.Chat) error
}

func (r *chatRepository) UpdateIndependentLegacyConnectionCAS(ctx context.Context, previous, next *models.Chat) error {
	if previous == nil || next == nil || previous.ID != next.ID || previous.UserID != next.UserID ||
		previous.ERPAssociated || previous.SourceID != "" || previous.SourceBindingRevision != 0 ||
		previous.SelectedObjects != nil || next.SelectedObjects != nil || next.SelectionRevision != previous.SelectionRevision+1 {
		return ErrObjectConflict
	}
	fields, err := objectRebindingFields(previous, next)
	if err != nil {
		return err
	}
	connection := next.Connection.Clone()
	connection.CurrentSchema, connection.SchemaUpdatedAt = nil, nil
	fields["connection"] = connection
	fields["connection_revision"] = previous.ConnectionRevision + 1
	fields["object_state"] = nil
	filter := chatBindingFilter(previous)
	filter["selected_objects"] = nil
	return r.patchChat(ctx, filter, fields)
}
