package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/schemaobjects"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Uses the existing isolated Mongo fixture, never a business PostgreSQL source.
func metadataPatchMongoFixture(t *testing.T) (*chatService, *mongo.Collection, models.Chat) {
	t.Helper()
	service, collection := displayNameMongoService(t)
	owner := primitive.NewObjectID().Hex()
	created, status, err := service.CreateWithoutConnectionPing(owner,
		displayNameCreateRequest(t, "postgresql", "metadata_patch_fixture", `,"display_name":"before"`))
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create fixture: %d %v", status, err)
	}
	id, err := primitive.ObjectIDFromHex(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = collection.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"selected_collections": "orders", "selection_revision": int64(7),
		"connection.current_schema": "retained schema", "connection.schema_updated_at": primitive.NewDateTimeFromTime(time.Unix(1700000000, 0)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return service, collection, displayNameStored(t, collection, created.ID)
}

func TestChatMetadataPatchSelectionPersistence(t *testing.T) {
	for _, selected := range []string{"orders", "items"} {
		t.Run(selected, func(t *testing.T) {
			service, collection, before := metadataPatchMongoFixture(t)
			name := "after"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := service.chatRepo.PatchMetadata(ctx, before.UserID, before.ID, repositories.ChatMetadataPatch{DisplayName: &name, SelectedCollections: &selected}); err != nil {
				t.Fatal(err)
			}
			after := displayNameStored(t, collection, before.ID.Hex())
			wantRevision := int64(7)
			wantConnection := before.Connection.Clone()
			if selected == "items" {
				wantRevision = 8
				wantConnection.CurrentSchema, wantConnection.SchemaUpdatedAt = nil, nil
			}
			if after.SelectedCollections != selected || after.SelectionRevision != wantRevision || after.DisplayName != "after" || !reflect.DeepEqual(after.Connection, wantConnection) {
				t.Fatalf("selection metadata persistence: selection=%q revision=%d name=%q connectionEqual=%t", after.SelectedCollections, after.SelectionRevision, after.DisplayName, reflect.DeepEqual(after.Connection, wantConnection))
			}
			// Replaying the committed value must not advance the selection again.
			if err := service.chatRepo.PatchMetadata(ctx, before.UserID, before.ID, repositories.ChatMetadataPatch{SelectedCollections: &selected}); err != nil {
				t.Fatal(err)
			}
			replay := displayNameStored(t, collection, before.ID.Hex())
			if replay.SelectionRevision != wantRevision || replay.SelectedCollections != selected || !reflect.DeepEqual(replay.Connection, wantConnection) {
				t.Fatal("same-selection replay changed revision or connection/schema")
			}
		})
	}
}

func TestChatMetadataPatchRejectsLegacySelectionForV2(t *testing.T) {
	for _, selected := range []string{"orders", "items"} {
		t.Run(selected, func(t *testing.T) {
			service, collection, before := metadataPatchMongoFixture(t)
			selection := schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "public", Name: "orders", Kind: schemaobjects.Table}}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := collection.UpdateOne(ctx, bson.M{"_id": before.ID}, bson.M{"$set": bson.M{"selected_objects": selection}}); err != nil {
				t.Fatal(err)
			}
			before = displayNameStored(t, collection, before.ID.Hex())
			name := "must not commit"
			err := service.chatRepo.PatchMetadata(ctx, before.UserID, before.ID, repositories.ChatMetadataPatch{DisplayName: &name, SelectedCollections: &selected})
			if !errors.Is(err, repositories.ErrObjectConflict) {
				t.Fatalf("legacy patch into v2: %v", err)
			}
			after := displayNameStored(t, collection, before.ID.Hex())
			if !reflect.DeepEqual(after, before) {
				t.Fatal("rejected legacy patch changed v2 document")
			}
		})
	}
}

// A synchronous driver command observer commits a competing real Mongo write
// after ReadCurrent has built the CAS, but before its UpdateOne reaches Mongo.
// It never replaces a repository result or edits the outgoing CAS command.
func TestChatMetadataPatchSelectionCASRejectsConcurrentChange(t *testing.T) {
	for _, selected := range []string{"orders", "items"} {
		for _, race := range []string{"selection", "binding"} {
			t.Run(selected+"/"+race, func(t *testing.T) {
				_, collection, before := metadataPatchMongoFixture(t)
				var fired atomic.Bool
				outcome := make(chan error, 1)
				monitor := &event.CommandMonitor{Started: func(_ context.Context, command *event.CommandStartedEvent) {
					if command.CommandName != "update" || command.DatabaseName != collection.Database().Name() || !fired.CompareAndSwap(false, true) {
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					update := bson.M{"$set": bson.M{"connection.current_schema": "winner schema"}}
					if race == "selection" {
						update["$inc"] = bson.M{"selection_revision": int64(1)}
						update["$set"].(bson.M)["selected_collections"] = "winner"
					} else {
						update["$inc"] = bson.M{"connection_revision": int64(1)}
					}
					result, err := collection.UpdateOne(ctx, bson.M{"_id": before.ID}, update)
					if err == nil && result.MatchedCount != 1 {
						err = fmt.Errorf("concurrent fixture write matched %d documents", result.MatchedCount)
					}
					outcome <- err
				}}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				client, err := mongo.Connect(ctx, options.Client().ApplyURI(os.Getenv("ASSISTANT_TEST_MONGO_URI")).SetRegistry(mongodb.BSONRegistry()).SetServerSelectionTimeout(5*time.Second).SetMonitor(monitor))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = client.Disconnect(ctx)
				})
				repo := repositories.NewChatRepository(&mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: collection.Database().Name()}}, nil)
				name := "must not commit"
				patch := repositories.ChatMetadataPatch{SelectedCollections: &selected}
				if selected == "items" {
					patch.DisplayName = &name
				}
				// The same-value case has no other metadata: even after removing
				// its selection write, the request must not skip the CAS.
				err = repo.PatchMetadata(ctx, before.UserID, before.ID, patch)
				if !fired.Load() {
					t.Fatal("test did not reach real Mongo CAS write")
				}
				if raceErr := <-outcome; raceErr != nil {
					t.Fatalf("real concurrent write: %v", raceErr)
				}
				if !errors.Is(err, repositories.ErrChatBindingConflict) {
					t.Fatalf("stale %s CAS accepted for %q: %v", race, selected, err)
				}
				after := displayNameStored(t, collection, before.ID.Hex())
				wantSelection, wantRevision, wantConnectionRevision := "orders", int64(7), before.ConnectionRevision
				if race == "selection" {
					wantSelection, wantRevision = "winner", 8
				} else {
					wantConnectionRevision++
				}
				wantConnection := before.Connection.Clone()
				winner := "winner schema"
				wantConnection.CurrentSchema = &winner
				if after.DisplayName != before.DisplayName || after.SelectedCollections != wantSelection || after.SelectionRevision != wantRevision || after.ConnectionRevision != wantConnectionRevision || !reflect.DeepEqual(after.Connection, wantConnection) {
					t.Fatal("stale metadata patch overwrote the concurrent winner")
				}
			})
		}
	}
}
