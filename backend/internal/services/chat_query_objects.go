package services

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/schemaobjects"
	"net/http"
)

func queryObjectsEnabled(chat *models.Chat) bool {
	return chat != nil && config.Env.QueryObjectsEnabled && chat.Connection.Type == constants.DatabaseTypePostgreSQL
}
func objectScope(chat *models.Chat, connection models.Connection) schemaobjects.Scope {
	return schemaobjects.Scope{OwnerID: chat.UserID.Hex(), ChatID: chat.ID.Hex(),
		SourceKey:       chat.SourceID + ":" + ConnectionFingerprint(connection),
		BindingRevision: chat.SourceBindingRevision, ConnectionRevision: chat.ConnectionRevision, SelectionRevision: chat.SelectionRevision}
}

type queryObjectCatalog struct {
	Objects  []schemaobjects.Object
	Observed *models.State
}

// The returned lease remains held until the caller finishes consuming or saving
// the catalog. Every collection goes through the actual reader and source gate.
func (s *chatService) loadQueryObjectCatalog(ctx context.Context, chat *models.Chat) (*models.Chat, *SourceRead, *queryObjectCatalog, schemaobjects.Scope, error) {
	if !queryObjectsEnabled(chat) {
		return nil, nil, nil, schemaobjects.Scope{}, ErrQueryObjectsDisabled
	}
	current, err := s.chatRepo.ReadCurrent(ctx, chat.UserID, chat.ID)
	if err != nil || current == nil {
		return nil, nil, nil, schemaobjects.Scope{}, ErrSourceForbidden
	}
	if !queryObjectsEnabled(current) || !sameChatBinding(chat, current) || chat.SelectionRevision != current.SelectionRevision {
		return nil, nil, nil, schemaobjects.Scope{}, ErrSelectionConflict
	}
	connection := current.Connection.Clone()
	utils.DecryptConnection(&connection)
	if s.sourcePolicy() == nil || s.dbManager == nil {
		return nil, nil, nil, schemaobjects.Scope{}, ErrSourceStateUnavailable
	}
	read, err := s.sourcePolicy().AcquireRead(ctx, current.UserID, current, connection)
	if err != nil {
		return nil, nil, nil, schemaobjects.Scope{}, err
	}
	fail := func(err error) (*models.Chat, *SourceRead, *queryObjectCatalog, schemaobjects.Scope, error) {
		read.Close()
		return nil, nil, nil, schemaobjects.Scope{}, err
	}
	scope := objectScope(current, connection)
	if s.objectState == nil {
		return fail(fmt.Errorf("query object state repository unavailable"))
	}
	// Fix the publication revision BEFORE any database catalog observation.
	observed, err := s.objectState.Read(read.Context, scope)
	if err != nil {
		return fail(err)
	}
	if observed == nil {
		return fail(fmt.Errorf("query object state unavailable"))
	}
	db, err := s.dbManager.GetConnectionContext(read.Context, current.ID.Hex())
	if err != nil {
		cfg := connectionConfig(current, connection)
		cfg.InitialSchemaDeferred = true
		if current.ERPAssociated {
			cfg.ApplicationName = s.sourcePolicy().applicationName(current)
		}
		if err = s.dbManager.Connect(read.Context, current.ID.Hex(), current.UserID.Hex(), "", cfg); err != nil {
			return fail(err)
		}
		db, err = s.dbManager.GetConnectionContext(read.Context, current.ID.Hex())
		if err != nil {
			return fail(err)
		}
	}
	catalog, err := (&dbmanager.PostgresDriver{}).GetQueryObjects(read.Context, db)
	if err != nil {
		return fail(err)
	}
	return current, read, &queryObjectCatalog{Objects: catalog, Observed: observed}, scope, nil
}
func (s *chatService) snapshotFromQueryCatalog(ctx context.Context, chat *models.Chat, scope schemaobjects.Scope, catalog *queryObjectCatalog) (*schemaobjects.Snapshot, error) {
	selection, err := chat.QuerySelection()
	if err != nil {
		return nil, err
	}
	snapshot, err := publishQueryObjectSnapshot(ctx, s.objectState, scope, selection, catalog.Observed, catalog.Objects)
	if err != nil {
		return nil, err
	}
	current, err := s.chatRepo.ReadCurrent(ctx, chat.UserID, chat.ID)
	if err != nil || current == nil || !sameChatBinding(current, chat) || current.SelectionRevision != scope.SelectionRevision {
		return nil, ErrSelectionConflict
	}
	return snapshot, nil
}
func (s *chatService) loadQueryObjectSnapshot(ctx context.Context, chat *models.Chat) (*schemaobjects.Snapshot, error) {
	current, read, catalog, scope, err := s.loadQueryObjectCatalog(ctx, chat)
	if err != nil {
		return nil, err
	}
	defer read.Close()
	return s.snapshotFromQueryCatalog(read.Context, current, scope, catalog)
}
func (s *chatService) getQueryObjectDirectory(ctx context.Context, chat *models.Chat) (*dtos.TablesResponse, uint32, error) {
	current, read, catalog, scope, err := s.loadQueryObjectCatalog(ctx, chat)
	if err != nil {
		return nil, uint32(sourceHTTPStatus(err)), err
	}
	defer read.Close()
	snapshot, err := s.snapshotFromQueryCatalog(read.Context, current, scope, catalog)
	if err != nil {
		return nil, http.StatusConflict, err
	}
	full, err := schemaobjects.Reconcile(scope, snapshot.Registry, catalog.Objects)
	if err != nil {
		return nil, http.StatusConflict, err
	}
	selected := map[string]bool{}
	for _, o := range snapshot.Objects {
		selected[o.Metadata.ObjectID] = true
	}
	response := &dtos.TablesResponse{Tables: []dtos.TableInfo{}, MetadataVersion: 2, SelectionRevision: current.SelectionRevision, SelectedObjects: current.SelectedObjects, QueryObjectsEnabled: true}
	for _, o := range full.Objects {
		info := dtos.TableInfo{Name: schemaobjects.SQLIdentifier(o.Metadata.Ref), ObjectID: o.Metadata.ObjectID, InstanceVersion: o.Metadata.InstanceVersion,
			SchemaName: o.Metadata.Ref.Schema, ObjectName: o.Metadata.Ref.Name, ObjectType: o.Metadata.Ref.Kind, SQLIdentifier: schemaobjects.SQLIdentifier(o.Metadata.Ref),
			RowCountStatus: o.RowCountStatus, PartialColumns: o.Metadata.PartialColumns, IsSelected: selected[o.Metadata.ObjectID], Columns: []dtos.ColumnInfo{}}
		if o.RowCount != nil {
			info.RowCount = *o.RowCount
		}
		for _, c := range o.Columns {
			column := dtos.ColumnInfo{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Ordinal: c.Ordinal, Comment: c.Comment}
			if c.Nullable != nil {
				column.IsNullable = *c.Nullable
			}
			info.Columns = append(info.Columns, column)
		}
		response.Tables = append(response.Tables, info)
	}
	return response, http.StatusOK, nil
}
func (s *chatService) saveQueryObjectSelection(ctx context.Context, chat *models.Chat, selection *schemaobjects.Selection, legacy string) error {
	store, ok := s.chatRepo.(repositories.ObjectSelectionRepository)
	if !ok {
		return fmt.Errorf("query object selection repository unavailable")
	}
	current, read, catalog, _, err := s.loadQueryObjectCatalog(ctx, chat)
	if err != nil {
		return err
	}
	defer read.Close()
	if selection.Mode == "explicit" {
		if _, err = schemaobjects.Resolve(catalog.Objects, nil, selection.Objects); err != nil {
			return schemaobjects.ErrUnavailable
		}
	}
	return store.UpdateObjectSelectionCAS(read.Context, current, selection, legacy)
}
func (s *chatService) updateQueryObjectChat(ctx context.Context, owner primitive.ObjectID, previous *models.Chat, connection models.Connection, bindingChanged bool, req *dtos.UpdateChatRequest) (*dtos.ChatResponse, uint32, error) {
	objects, legacy, changed, err := prepareObjectSelection(previous, req.SelectedObjects, req.SelectedCollections, req.SelectionRevision)
	if err != nil {
		return nil, http.StatusConflict, err
	}
	if bindingChanged && req.SelectedObjects == nil {
		return nil, http.StatusConflict, ErrSelectionConflict
	}
	if (changed || bindingChanged) && (!config.Env.QueryObjectsEnabled || connection.Type != constants.DatabaseTypePostgreSQL) {
		return nil, http.StatusConflict, ErrQueryObjectsDisabled
	}
	next := *previous
	if changed {
		next.SelectedObjects = objects
		next.SelectedCollections = legacy
		next.SelectionRevision = previous.SelectionRevision + 1
	}
	patch := repositories.ChatMetadataPatch{
		DisplayName:             req.DisplayName,
		PreferredLLMModel:       req.PreferredLLMModel,
		PreferredReasoningLevel: req.PreferredReasoningLevel,
	}
	if req.Settings != nil {
		patch.AutoExecuteQuery = req.Settings.AutoExecuteQuery
		patch.NonTechMode = req.Settings.NonTechMode
		patch.AutoGenerateVisualization = req.Settings.AutoGenerateVisualization
		patch.AutoAnalyzeResults = req.Settings.AutoAnalyzeResults
	}
	if bindingChanged {
		next.Connection = connection.Clone()
		if err = utils.EncryptConnection(&next.Connection); err != nil {
			return nil, http.StatusInternalServerError, err
		}
		classification, _ := s.sourcePolicy().Classify(previous, connection)
		if classification == models.SourceIndependent {
			if err = s.probeConnection(ctx, previous, connection); err != nil {
				return nil, http.StatusBadRequest, err
			}
			store, ok := s.chatRepo.(repositories.ObjectConnectionRepository)
			if !ok {
				return nil, http.StatusServiceUnavailable, fmt.Errorf("atomic object connection repository unavailable")
			}
			if err = store.UpdateIndependentObjectConnectionCAS(ctx, previous, &next); err != nil {
				return nil, http.StatusConflict, err
			}
			if s.dbManager != nil {
				if _, exists := s.dbManager.GetConnectionInfo(previous.ID.Hex()); exists {
					if err = s.dbManager.Disconnect(previous.ID.Hex(), owner.Hex(), false); err != nil {
						return nil, http.StatusConflict, err
					}
				}
			}
			if err = s.chatRepo.PatchMetadata(ctx, owner, previous.ID, patch); err != nil {
				return nil, http.StatusConflict, err
			}
		} else {
			if _, err = s.sourcePolicy().ChangeBinding(ctx, owner, previous, &next, connection, s.chatRepo, &chatBindingEffects{service: s, owner: owner, chatID: previous.ID, probe: true, refresh: true, metadata: &patch}); err != nil {
				return nil, http.StatusConflict, err
			}
		}
	} else {
		if changed {
			if err = s.saveQueryObjectSelection(ctx, previous, objects, legacy); err != nil {
				return nil, http.StatusConflict, err
			}
		}
		if err = s.chatRepo.PatchMetadata(ctx, owner, previous.ID, patch); err != nil {
			return nil, http.StatusConflict, err
		}
	}
	current, err := s.chatRepo.ReadCurrent(ctx, owner, previous.ID)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return s.buildChatResponse(current), http.StatusOK, nil
}
