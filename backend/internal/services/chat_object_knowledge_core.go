package services

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"math"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"time"
)

var ErrObjectKnowledgeInput = errors.New("invalid knowledge edit")
var ErrObjectKnowledgeStore = errors.New("knowledge storage unavailable")

type objectKnowledgeStore interface {
	FindByChatID(context.Context, primitive.ObjectID) (*models.KnowledgeBase, error)
	CompareAndSwapKnowledge(context.Context, *models.KnowledgeBase, *models.KnowledgeBase) (bool, error)
}
type objectKnowledgeStateStore interface {
	Read(context.Context, schemaobjects.Scope) (*models.State, error)
	CompareAndSwap(context.Context, schemaobjects.Scope, int64, models.State) error
}
type objectKnowledgeSession struct {
	Snapshot   func(context.Context) (schemaobjects.Snapshot, error)
	Store      objectKnowledgeStore
	Invalidate func(context.Context, schemaobjects.Scope) error
	Now        time.Time
}

// NormalizeObjectKnowledgeOwner is only for a caller that has already authorized
// the chat and current source. An absent legacy owner is not an arbitrary-owner
// wildcard: chat identity must match, and a nonzero foreign owner is rejected.
func NormalizeObjectKnowledgeOwner(s schemaobjects.Snapshot, kb *models.KnowledgeBase) (*models.KnowledgeBase, error) {
	if kb == nil {
		return nil, nil
	}
	owner, err := primitive.ObjectIDFromHex(s.Scope.OwnerID)
	if err != nil || owner.IsZero() || kb.ChatID.Hex() != s.Scope.ChatID {
		return nil, ErrObjectKnowledgeScope
	}
	if (!kb.UserID.IsZero() && kb.UserID != owner) || (kb.UserID.IsZero() && kb.KnowledgeVersion >= 2) {
		return nil, ErrObjectKnowledgeScope
	}
	result := *kb
	result.UserID = owner
	result.TableDescriptions = cloneKnowledgeDescriptions(kb.TableDescriptions)
	return &result, nil
}

func newObjectKnowledgeDocument(s schemaobjects.Snapshot) *models.KnowledgeBase {
	chat, _ := primitive.ObjectIDFromHex(s.Scope.ChatID)
	owner, _ := primitive.ObjectIDFromHex(s.Scope.OwnerID)
	return &models.KnowledgeBase{ChatID: chat, UserID: owner, TableDescriptions: []models.TableDescription{}}
}

func applyObjectKnowledgeEdits(s schemaobjects.Snapshot, kb *models.KnowledgeBase, expected string, edits []dtos.KnowledgeBaseDescriptionEdit, now time.Time) (*models.KnowledgeBase, error) {
	projection, err := ProjectObjectKnowledge(s, kb)
	if err != nil {
		return nil, err
	}
	if expected == "" || expected != projection.KBRevision {
		return nil, ErrObjectKnowledgeRevision
	}
	if len(edits) == 0 || len(edits) > 256 {
		return nil, ErrObjectKnowledgeInput
	}
	result := newObjectKnowledgeDocument(s)
	if kb != nil {
		*result = *kb
		result.TableDescriptions = cloneKnowledgeDescriptions(kb.TableDescriptions)
	}
	originalLength := len(result.TableDescriptions)
	seenRows := map[int]bool{}
	seenObjects := map[string]bool{}
	for _, edit := range edits {
		if edit.Description == nil && len(edit.FieldDescriptions) == 0 {
			return nil, ErrObjectKnowledgeInput
		}
		index := -1
		var object *schemaobjects.Object
		if edit.DescriptionIndex != nil {
			index = *edit.DescriptionIndex
			if index < 0 || index >= originalLength {
				return nil, ErrObjectKnowledgeInput
			}
			row := result.TableDescriptions[index]
			if edit.ObjectID != "" && edit.ObjectID != row.ObjectID {
				return nil, ErrObjectKnowledgeInput
			}
			for i := range s.Objects {
				if knowledgeAssociationMatches(row, s.Objects[i], s.Scope) {
					object = &s.Objects[i]
					break
				}
			}
		} else {
			if edit.ObjectID == "" || seenObjects[edit.ObjectID] {
				return nil, ErrObjectKnowledgeInput
			}
			seenObjects[edit.ObjectID] = true
			for i := range s.Objects {
				if s.Objects[i].Metadata.ObjectID == edit.ObjectID && s.Objects[i].Metadata.InstanceVersion != "" {
					object = &s.Objects[i]
					break
				}
			}
			if object == nil {
				return nil, ErrObjectKnowledgeMapping
			}
			for i, row := range result.TableDescriptions {
				if knowledgeAssociationMatches(row, *object, s.Scope) {
					index = i
					break
				}
			}
		}
		if index >= 0 && seenRows[index] {
			return nil, ErrObjectKnowledgeInput
		}
		var row models.TableDescription
		existing := index >= 0
		if existing {
			seenRows[index] = true
			row = cloneKnowledgeDescriptions([]models.TableDescription{result.TableDescriptions[index]})[0]
		} else {
			scope := s.Scope
			row = models.TableDescription{TableName: schemaobjects.SQLIdentifier(object.Metadata.Ref), ObjectID: object.Metadata.ObjectID,
				InstanceVersion: object.Metadata.InstanceVersion, ObjectScope: &scope, State: models.KnowledgeMapped, FieldDescriptions: []models.FieldDescription{}}
		}
		if edit.Description != nil {
			row.Description = *edit.Description
			row.Provenance = models.KnowledgeUserEdit
			row.UpdatedAt = now
		}
		columns := map[string]string{}
		if object != nil {
			columns = knowledgeColumnTypes(*object)
		}
		seenFields := map[string]bool{}
		for _, editField := range edit.FieldDescriptions {
			if editField.FieldName == "" || seenFields[editField.FieldName] {
				return nil, ErrObjectKnowledgeInput
			}
			seenFields[editField.FieldName] = true
			fieldIndex := -1
			for i, field := range row.FieldDescriptions {
				if field.FieldName == editField.FieldName {
					fieldIndex = i
					break
				}
			}
			if object != nil && columns[editField.FieldName] == "" {
				return nil, ErrObjectKnowledgeMapping
			}
			if object == nil && fieldIndex < 0 {
				return nil, ErrObjectKnowledgeMapping
			}
			field := models.FieldDescription{FieldName: editField.FieldName, Description: editField.Description, Provenance: models.KnowledgeUserEdit, UpdatedAt: now, State: models.KnowledgePending}
			if fieldIndex >= 0 {
				field.ColumnType = row.FieldDescriptions[fieldIndex].ColumnType
			}
			if object != nil {
				field.ColumnType = columns[editField.FieldName]
				field.State = models.KnowledgeMapped
			}
			if fieldIndex < 0 {
				row.FieldDescriptions = append(row.FieldDescriptions, field)
			} else {
				row.FieldDescriptions[fieldIndex] = field
			}
		}
		if existing {
			archive := cloneKnowledgeDescriptions([]models.TableDescription{result.TableDescriptions[index]})[0]
			archive.State = models.KnowledgePending
			for i := range archive.FieldDescriptions {
				archive.FieldDescriptions[i].State = models.KnowledgePending
			}
			result.TableDescriptions[index] = row
			result.TableDescriptions = append(result.TableDescriptions, archive)
		} else {
			result.TableDescriptions = append(result.TableDescriptions, row)
		}
	}
	projected, err := ProjectObjectKnowledge(s, result)
	if err != nil {
		return nil, err
	}
	result.KBRevision = projected.KBRevision
	return result, nil
}

func applyObjectKnowledgeMappings(s schemaobjects.Snapshot, kb *models.KnowledgeBase, req dtos.ConfirmKnowledgeBaseMappingsRequest) (*models.KnowledgeBase, error) {
	projected, err := ProjectObjectKnowledge(s, kb)
	if err != nil {
		return nil, err
	}
	if req.ExpectedKBRevision == "" || req.ExpectedKBRevision != projected.KBRevision {
		return nil, ErrObjectKnowledgeRevision
	}
	if kb == nil || len(req.Mappings) == 0 || len(req.Mappings) > 256 {
		return nil, ErrObjectKnowledgeInput
	}
	seen := map[int]bool{}
	for _, mapping := range req.Mappings {
		if mapping.DescriptionIndex == nil || *mapping.DescriptionIndex < 0 || *mapping.DescriptionIndex >= len(kb.TableDescriptions) || seen[*mapping.DescriptionIndex] || mapping.ObjectID == "" {
			return nil, ErrObjectKnowledgeInput
		}
		seen[*mapping.DescriptionIndex] = true
	}
	result := kb
	revision := projected.KBRevision
	for _, mapping := range req.Mappings {
		result, err = ConfirmObjectKnowledgeMapping(s, result, revision, *mapping.DescriptionIndex, mapping.ObjectID)
		if err != nil {
			return nil, err
		}
		revision = result.KBRevision
	}
	return result, nil
}

func knowledgeResponse(s schemaobjects.Snapshot, kb *models.KnowledgeBase, indexStatus string, saved bool) (*dtos.KnowledgeBaseResponse, error) {
	projection, err := ProjectObjectKnowledge(s, kb)
	if err != nil {
		return nil, err
	}
	response := &dtos.KnowledgeBaseResponse{ChatID: s.Scope.ChatID, KBRevision: projection.KBRevision,
		TableDescriptions: projection.History, EffectiveDescriptions: projection.Effective, QueryObjectsEnabled: true, IndexStatus: indexStatus, Saved: saved}
	if kb != nil {
		if !kb.CreatedAt.IsZero() {
			response.CreatedAt = kb.CreatedAt.Format(time.RFC3339)
		}
		if !kb.UpdatedAt.IsZero() {
			response.UpdatedAt = kb.UpdatedAt.Format(time.RFC3339)
		}
	}
	return response, nil
}

func (s objectKnowledgeSession) load(ctx context.Context) (schemaobjects.Snapshot, *models.KnowledgeBase, *models.KnowledgeBase, error) {
	if s.Snapshot == nil || s.Store == nil {
		return schemaobjects.Snapshot{}, nil, nil, ErrObjectKnowledgeStore
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return snapshot, nil, nil, err
	}
	chat, err := primitive.ObjectIDFromHex(snapshot.Scope.ChatID)
	if err != nil {
		return snapshot, nil, nil, ErrObjectKnowledgeScope
	}
	stored, err := s.Store.FindByChatID(ctx, chat)
	if err != nil {
		return snapshot, nil, nil, ErrObjectKnowledgeStore
	}
	normalized, err := NormalizeObjectKnowledgeOwner(snapshot, stored)
	return snapshot, stored, normalized, err
}
func (s objectKnowledgeSession) get(ctx context.Context) (*dtos.KnowledgeBaseResponse, error) {
	snapshot, _, kb, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	initial, err := ProjectObjectKnowledge(snapshot, kb)
	if err != nil {
		return nil, err
	}
	current, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	checked, err := ProjectObjectKnowledge(current, kb)
	if err != nil {
		return nil, err
	}
	if checked.KBRevision != initial.KBRevision {
		return nil, ErrObjectKnowledgeRevision
	}
	return knowledgeResponse(current, kb, "not_checked", false)
}
func (s objectKnowledgeSession) save(ctx context.Context, expected string, apply func(schemaobjects.Snapshot, *models.KnowledgeBase, string) (*models.KnowledgeBase, error)) (*dtos.KnowledgeBaseResponse, error) {
	snapshot, stored, kb, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	initial, err := ProjectObjectKnowledge(snapshot, kb)
	if err != nil {
		return nil, err
	}
	if expected == "" || expected != initial.KBRevision {
		return nil, ErrObjectKnowledgeRevision
	}
	next, err := apply(snapshot, kb, expected)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return nil, ErrObjectKnowledgeInput
	}
	// Recollect under the same held source lease before the storage CAS.
	current, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	checked, err := ProjectObjectKnowledge(current, kb)
	if err != nil {
		return nil, err
	}
	if checked.KBRevision != initial.KBRevision {
		return nil, ErrObjectKnowledgeRevision
	}
	previousRevision := int64(0)
	if stored != nil {
		previousRevision = stored.StorageRevision
	}
	if previousRevision < 0 || previousRevision == math.MaxInt64 {
		return nil, ErrObjectKnowledgeRevision
	}
	next.StorageRevision = previousRevision + 1
	next.KnowledgeVersion = 2
	next.UpdatedAt = s.Now
	if stored == nil {
		next.ID = primitive.NewObjectID()
		next.CreatedAt = s.Now
	}
	swapped, err := s.Store.CompareAndSwapKnowledge(ctx, stored, next)
	if err != nil {
		return nil, ErrObjectKnowledgeStore
	}
	if !swapped {
		return nil, ErrObjectKnowledgeRevision
	}
	// A post-save source/selection change cannot be rolled back across documents.
	// Report the durable save without publishing old history or a usable token.
	final, err := s.Snapshot(ctx)
	if err != nil {
		return &dtos.KnowledgeBaseResponse{ChatID: snapshot.Scope.ChatID, QueryObjectsEnabled: true, Saved: true, RefreshRequired: true, IndexStatus: "invalidation_failed"}, nil
	}
	finalCheck, err := ProjectObjectKnowledge(final, kb)
	if err != nil || finalCheck.KBRevision != initial.KBRevision {
		return &dtos.KnowledgeBaseResponse{ChatID: snapshot.Scope.ChatID, QueryObjectsEnabled: true, Saved: true, RefreshRequired: true, IndexStatus: "invalidation_failed"}, nil
	}
	indexStatus := "stale"
	if s.Invalidate == nil || s.Invalidate(ctx, final.Scope) != nil {
		indexStatus = "invalidation_failed"
	}
	// Invalidate performs its own current-binding/selection read. Revalidate
	// after it so a scope failure is not treated as only a manifest CAS failure.
	final, err = s.Snapshot(ctx)
	if err != nil {
		return &dtos.KnowledgeBaseResponse{ChatID: snapshot.Scope.ChatID, QueryObjectsEnabled: true, Saved: true, RefreshRequired: true, IndexStatus: "invalidation_failed"}, nil
	}
	finalCheck, err = ProjectObjectKnowledge(final, kb)
	if err != nil || finalCheck.KBRevision != initial.KBRevision {
		return &dtos.KnowledgeBaseResponse{ChatID: snapshot.Scope.ChatID, QueryObjectsEnabled: true, Saved: true, RefreshRequired: true, IndexStatus: "invalidation_failed"}, nil
	}
	return knowledgeResponse(final, next, indexStatus, true)
}

func invalidateObjectKnowledgeIndex(ctx context.Context, store objectKnowledgeStateStore, scope schemaobjects.Scope) error {
	if store == nil {
		return ErrObjectKnowledgeStore
	}
	state, err := store.Read(ctx, scope)
	if err != nil {
		return err
	}
	if state == nil || state.Revision == math.MaxInt64 {
		return ErrObjectKnowledgeRevision
	}
	if state.Scope.SourceKey != "" && !knowledgeSameSource(state.Scope, scope) {
		return ErrObjectKnowledgeRevision
	}
	next := *state
	next.Scope = scope
	next.Revision = state.Revision + 1
	next.Manifest.Status = "stale"
	next.Manifest.ActiveGeneration = ""
	next.Manifest.BuildingGeneration = ""
	next.Manifest.FailureCode = "KB_CHANGED"
	// Manifest.Scope stays attached to its old generation; registry is unchanged.
	return store.CompareAndSwap(ctx, scope, state.Revision, next)
}
