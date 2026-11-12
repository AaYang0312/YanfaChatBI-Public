package services

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestQueryObjectRecommendationCacheScope(t *testing.T) {
	chat := models.NewChat(primitive.NewObjectID(), models.Connection{Type: "postgresql", Host: "encrypted-host", Database: "encrypted-db"}, models.ChatSettings{})
	chat.SelectedCollections = "orders"
	original := recommendationCacheScope(chat)
	if original == "" || recommendationCacheScope(nil) != "" {
		t.Fatal("invalid cache scope")
	}
	mutations := map[string]func(*models.Chat){
		"owner":              func(c *models.Chat) { c.UserID = primitive.NewObjectID() },
		"chat":               func(c *models.Chat) { c.ID = primitive.NewObjectID() },
		"source":             func(c *models.Chat) { c.SourceID = "other" },
		"binding":            func(c *models.Chat) { c.SourceBindingRevision++ },
		"connection":         func(c *models.Chat) { c.ConnectionRevision++ },
		"selection_revision": func(c *models.Chat) { c.SelectionRevision++ },
		"selection":          func(c *models.Chat) { c.SelectedCollections = "smaller" },
		"schema":             func(c *models.Chat) { text := "new columns"; c.Connection.CurrentSchema = &text },
		"mode":               func(c *models.Chat) { c.Settings.AutoExecuteQuery = true },
		"v2":                 func(c *models.Chat) { c.SelectedObjects = &schemaobjects.Selection{Version: 2, Mode: "explicit"} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			next := *chat
			next.Connection = chat.Connection.Clone()
			mutate(&next)
			if recommendationCacheScope(&next) == original {
				t.Fatal("stale cache survived scope change")
			}
		})
	}
}
