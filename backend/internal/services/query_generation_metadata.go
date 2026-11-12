package services

import (
	"context"
	"fmt"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/schemaobjects"
	"strings"
)

// PostgreSQL's legacy table-only mode can still collect current metadata when
// the view rollout flag is off. It never enables v2 selections or reuses KB text.
func (s *chatService) legacyPostgresGenerationSnapshot(ctx context.Context, chat *models.Chat) (schemaobjects.Snapshot, error) {
	if chat.SelectedObjects != nil {
		return schemaobjects.Snapshot{}, ErrQueryObjectsDisabled
	}
	current, read, err := acquireChatSource(ctx, s.chatRepo, s.sourceAccess, chat.UserID.Hex(), chat.ID.Hex())
	if err != nil {
		return schemaobjects.Snapshot{}, err
	}
	defer read.Close()
	if current.SelectedObjects != nil || !sameChatBinding(current, chat) || current.SelectionRevision != chat.SelectionRevision {
		return schemaobjects.Snapshot{}, ErrSelectionConflict
	}
	db, err := s.dbManager.GetConnectionContext(read.Context, current.ID.Hex())
	if err != nil {
		if _, err = s.ConnectDB(read.Context, current.UserID.Hex(), current.ID.Hex(), ""); err != nil {
			return schemaobjects.Snapshot{}, err
		}
		db, err = s.dbManager.GetConnectionContext(read.Context, current.ID.Hex())
		if err != nil {
			return schemaobjects.Snapshot{}, err
		}
	}
	catalog, err := (&dbmanager.PostgresDriver{}).GetQueryObjects(read.Context, db)
	if err != nil {
		return schemaobjects.Snapshot{}, err
	}
	selection, err := current.QuerySelection()
	if err != nil {
		return schemaobjects.Snapshot{}, err
	}
	objects, err := schemaobjects.Select(catalog, selection)
	if err != nil {
		return schemaobjects.Snapshot{}, err
	}
	connection := current.Connection.Clone()
	utils.DecryptConnection(&connection)
	return schemaobjects.Reconcile(objectScope(current, connection), nil, objects)
}
func isPostgresGenerationType(kind string) bool {
	return kind == constants.DatabaseTypePostgreSQL || kind == constants.DatabaseTypeTimescaleDB || kind == constants.DatabaseTypeYugabyteDB
}
func (s *chatService) generationMetadata(ctx context.Context, chat *models.Chat, args map[string]interface{}) (string, error) {
	if queryObjectsEnabled(chat) {
		if args == nil {
			return buildQueryObjectContext(ctx, s.queryObjectReader(chat), nil, "")
		}
		return s.queryObjectTableInfo(ctx, chat, args)
	}
	if chat.SelectedObjects != nil {
		return "", ErrQueryObjectsDisabled
	}
	if isPostgresGenerationType(chat.Connection.Type) {
		snapshot, err := s.legacyPostgresGenerationSnapshot(ctx, chat)
		if err != nil {
			return "", err
		}
		objects := snapshot.Objects
		if args != nil {
			objects, err = resolveQueryObjectArguments(snapshot, args)
			if err != nil {
				return "", err
			}
		}
		return schemaobjects.Format(objects), nil
	}
	// Some older drivers infer schema by sampling or counting business records.
	// Strict requests only use their identity-scoped stored table metadata; they
	// never invoke those collectors, and never use CurrentSchema/example text.
	stored, err := s.dbManager.GetSchemaManager().GetStoredSchemaInfo(ctx, chat.ID.Hex())
	if err != nil || stored == nil {
		return "", fmt.Errorf("metadata-only schema unavailable for this connection")
	}
	safe := *stored
	safe.Tables = map[string]dbmanager.TableSchema{}
	safe.Views = nil
	allowed := map[string]bool{}
	all := chat.SelectedCollections == "" || chat.SelectedCollections == "ALL"
	for _, name := range strings.Split(chat.SelectedCollections, ",") {
		allowed[strings.TrimSpace(name)] = true
	}
	for name, table := range stored.Tables {
		if all || allowed[name] {
			safe.Tables[name] = table
		}
	}
	if args != nil {
		if len(args) != 1 {
			return "", schemaobjects.ErrUnavailable
		}
		raw, ok := args["table_names"]
		if !ok {
			return "", schemaobjects.ErrUnavailable
		}
		names := []string{}
		switch list := raw.(type) {
		case []string:
			names = list
		case []interface{}:
			for _, value := range list {
				name, ok := value.(string)
				if !ok {
					return "", schemaobjects.ErrUnavailable
				}
				names = append(names, name)
			}
		default:
			return "", schemaobjects.ErrUnavailable
		}
		if len(names) == 0 || len(names) > 256 {
			return "", schemaobjects.ErrUnavailable
		}
		selected := map[string]dbmanager.TableSchema{}
		for _, name := range names {
			table, ok := safe.Tables[name]
			if !ok {
				return "", schemaobjects.ErrUnavailable
			}
			selected[name] = table
		}
		safe.Tables = selected
	}
	for name, table := range safe.Tables {
		fks := map[string]dbmanager.ForeignKey{}
		for key, fk := range table.ForeignKeys {
			if _, ok := safe.Tables[fk.RefTable]; ok {
				fks[key] = fk
			}
		}
		table.ForeignKeys = fks
		safe.Tables[name] = table
	}
	return s.dbManager.GetSchemaManager().FormatSchemaForLLM(&safe), nil
}
