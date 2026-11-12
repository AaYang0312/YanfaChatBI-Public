package services

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestObjectSelectionCreateV2BeforePersistence(t *testing.T) {
	s, repo := scopedChatService(t)
	previous := config.Env.QueryObjectsEnabled
	config.Env.QueryObjectsEnabled = true
	t.Cleanup(func() { config.Env.QueryObjectsEnabled = previous })
	selection := &schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "reporting", Name: "view_only", Kind: schemaobjects.View}}}
	req := &dtos.CreateChatRequest{SelectedObjects: selection, Connection: dtos.CreateConnectionRequest{Type: "postgresql", Host: "fixture", Database: "fixture"}}
	response, status, err := s.CreateWithoutConnectionPing(primitive.NewObjectID().Hex(), req)
	if err != nil || status != 201 {
		t.Fatalf("%d %v", status, err)
	}
	if repo.created.SelectedObjects == nil || repo.created.SelectedObjects.Objects[0].Kind != schemaobjects.View || repo.created.SelectedCollections == "ALL" || response.SelectedObjects == nil {
		t.Fatal("v2 scope absent at initial persistence/response")
	}
}
func TestObjectSelectionCreateDisabled(t *testing.T) {
	s, repo := scopedChatService(t)
	previous := config.Env.QueryObjectsEnabled
	config.Env.QueryObjectsEnabled = false
	t.Cleanup(func() { config.Env.QueryObjectsEnabled = previous })
	req := &dtos.CreateChatRequest{SelectedObjects: &schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.View}}, Connection: dtos.CreateConnectionRequest{Type: "postgresql"}}
	_, status, err := s.CreateWithoutConnectionPing(primitive.NewObjectID().Hex(), req)
	if err == nil || status != 409 || repo.created != nil {
		t.Fatalf("disabled v2 silently accepted: %d %v", status, err)
	}
}

func TestObjectSelectionDuplicateKeepsViewOnlyScope(t *testing.T) {
	s, repo := scopedChatService(t)
	previous := config.Env.QueryObjectsEnabled
	config.Env.QueryObjectsEnabled = true
	t.Cleanup(func() { config.Env.QueryObjectsEnabled = previous })
	owner := primitive.NewObjectID()
	repo.original = models.NewChat(owner, models.Connection{Type: "postgresql", Host: "fixture", Database: "fixture"}, models.DefaultChatSettings())
	repo.original.SelectedObjects = &schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "reporting", Name: "view_only", Kind: schemaobjects.View}}}
	repo.original.SelectedCollections = v2LegacySelection
	repo.original.ObjectState = &models.State{Manifest: models.Manifest{ActiveGeneration: "old-generation"}}
	stale := "old structure"
	repo.original.Connection.CurrentSchema = &stale
	response, status, err := s.Duplicate(owner.Hex(), repo.original.ID.Hex(), false, false)
	if err != nil || status != 200 {
		t.Fatalf("%d %v", status, err)
	}
	if response.SelectedObjects == nil || repo.created.SelectedObjects == nil || repo.created.SelectedObjects.Objects[0].Name != "view_only" {
		t.Fatal("duplicated view scope lost")
	}
	if repo.created.Connection.CurrentSchema != nil || repo.created.ObjectState != nil {
		t.Fatal("duplicate inherited schema/index state")
	}
	legacy := "ALL"
	repo.created = nil
	if _, status, err := s.Duplicate(owner.Hex(), repo.original.ID.Hex(), false, false, &legacy); err == nil || status != 409 || repo.created != nil {
		t.Fatal("legacy duplicate overrode v2 scope")
	}
}
