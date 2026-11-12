//go:build object_mongo_integration

package repositories

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/schemaobjects"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

// Run only in the approved isolated stack, selecting ^TestQueryObjectsMongo.
// No application initializer, Redis, SQL, vector store or model provider is used.
// Each case owns one random database; a child only reads its parent's database.
func queryObjectsMongoURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Fatal("object_mongo_integration requires explicit ASSISTANT_TEST_MONGO_URI; Mongo runtime is NOT verified")
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "mongodb" || u.Opaque != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		t.Fatal("Mongo test URI must use mongodb:// with no database path or fragment")
	}
	host := u.Host
	if strings.Contains(host, ":") {
		var port string
		host, port, err = net.SplitHostPort(host)
		p, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || p < 1 || p > 65535 {
			t.Fatal("Mongo test URI requires one valid loopback host and port")
		}
	}
	if host != "127.0.0.1" && host != "localhost" {
		t.Fatal("Mongo test URI permits only a single 127.0.0.1 or localhost host")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		t.Fatal("invalid Mongo test URI options")
	}
	for key, values := range query {
		if len(values) != 1 || (key != "authSource" && key != "directConnection") || (key == "directConnection" && values[0] != "true") {
			t.Fatal("Mongo test URI allows only authSource and directConnection=true options")
		}
	}
	return uri
}

func queryObjectsMongoConnect(t *testing.T) *mongo.Client {
	t.Helper()
	// Direct topology prevents discovery from following any non-loopback member.
	opts := options.Client().ApplyURI(queryObjectsMongoURI(t)).SetDirect(true).
		SetRegistry(mongodb.BSONRegistry()).SetReadPreference(readpref.Primary()).
		SetWriteConcern(writeconcern.Majority()).SetServerSelectionTimeout(5 * time.Second).
		SetConnectTimeout(5 * time.Second).SetTimeout(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		t.Fatalf("connect isolated Mongo: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Disconnect(ctx); err != nil {
			t.Errorf("disconnect isolated Mongo: %v", err)
		}
	})
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("isolated Mongo unavailable (not skipped): %v", err)
	}
	return client
}

var queryObjectsMongoDBName = regexp.MustCompile(`^query_objects_test_[0-9a-f]{32}$`)

func queryObjectsMongoDatabase(t *testing.T) (*mongo.Database, *mongo.Database) {
	t.Helper()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "query_objects_test_" + hex.EncodeToString(token[:])
	a, b := queryObjectsMongoConnect(t), queryObjectsMongoConnect(t)
	if a == b {
		t.Fatal("CAS competitors must use independent Mongo clients")
	}
	db := a.Database(name)
	t.Cleanup(func() {
		// Capture only this case's generated name, never URI/database environment.
		if !queryObjectsMongoDBName.MatchString(name) {
			t.Error("refusing cleanup outside this test's database")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Drop(ctx); err != nil {
			t.Errorf("drop own test database %s: %v", name, err)
		}
	})
	return db, b.Database(name)
}

func queryObjectsMongoChatRepo(db *mongo.Database) *chatRepository {
	return NewChatRepository(&mongodb.MongoDBClient{Client: db.Client(), Config: mongodb.MongoDbConfigModel{DatabaseName: db.Name()}}, nil).(*chatRepository)
}

func queryObjectsMongoKBRepo(db *mongo.Database) *knowledgeBaseRepository {
	return NewKnowledgeBaseRepository(&mongodb.MongoDBClient{Client: db.Client(), Config: mongodb.MongoDbConfigModel{DatabaseName: db.Name()}}).(*knowledgeBaseRepository)
}

func queryObjectsMongoSeed(t *testing.T, db *mongo.Database) (*models.Chat, models.State) {
	t.Helper()
	chat := &models.Chat{
		Base: models.NewBase(), UserID: primitive.NewObjectID(), SourceID: "synthetic-source",
		SourceBindingRevision: 2, ConnectionRevision: 3, SelectionRevision: 4,
		SourceBindingConfirmed: true, ERPAssociated: true, SourceConnectionFingerprint: "synthetic-binding",
		SelectedCollections: "__QUERY_OBJECTS_V2__",
		SelectedObjects:     &schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.View}},
	}
	state := models.State{
		Revision: 1,
		Scope: schemaobjects.Scope{OwnerID: chat.UserID.Hex(), ChatID: chat.ID.Hex(), SourceKey: "synthetic-source",
			BindingRevision: 2, ConnectionRevision: 3, SelectionRevision: 4},
		Registry: schemaobjects.Registry{
			"object-current": {CatalogOID: 41001, Version: "instance-current", Present: true},
			"object-history": {CatalogOID: 41000, Version: "instance-retired", Present: false},
		},
	}
	state.Manifest = models.Manifest{FormatVersion: 2, Status: "building", ActiveGeneration: "generation-old",
		BuildingGeneration: "generation-candidate", Scope: state.Scope, SchemaFingerprint: "schema-digest", KBRevision: "kb-revision", ExpectedPoints: 3}
	if _, err := db.Collection("chats").InsertOne(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	var repo ObjectStateRepository = queryObjectsMongoChatRepo(db)
	if err := repo.CompareAndSwap(context.Background(), state.Scope, 0, state); err != nil {
		t.Fatalf("persist initial state: %v", err)
	}
	return chat, state
}

func queryObjectsMongoEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch:\n got: %#v\nwant: %#v", label, got, want)
	}
}

type queryObjectsMongoChildInput struct {
	Database  string
	Chat      models.Chat
	State     models.State
	ParentPID int
}

func TestQueryObjectsMongoStatePersistsAcrossProcess(t *testing.T) {
	if os.Getenv("QUERY_OBJECTS_MONGO_CHILD") == "read-state" {
		var input queryObjectsMongoChildInput
		payload, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			t.Fatalf("read parent fixture: %v", err)
		}
		if err := bson.Unmarshal(payload, &input); err != nil {
			t.Fatalf("decode parent fixture: %v", err)
		}
		if !queryObjectsMongoDBName.MatchString(input.Database) || input.ParentPID == os.Getpid() || input.ParentPID <= 0 {
			t.Fatal("invalid child database/process fixture")
		}
		repo := queryObjectsMongoChatRepo(queryObjectsMongoConnect(t).Database(input.Database))
		state, err := repo.Read(context.Background(), input.State.Scope)
		if err != nil {
			t.Fatal(err)
		}
		queryObjectsMongoEqual(t, "cross-process state/registry/generations", state, &input.State)
		chat, err := repo.ReadCurrent(context.Background(), input.Chat.UserID, input.Chat.ID)
		if err != nil {
			t.Fatal(err)
		}
		// These are the persisted binding/selection facts used by repository CAS.
		queryObjectsMongoEqual(t, "cross-process binding fields", chatBindingFilter(chat), chatBindingFilter(&input.Chat))
		queryObjectsMongoEqual(t, "cross-process selected objects", chat.SelectedObjects, input.Chat.SelectedObjects)
		fmt.Printf("mongo-child-read-ok pid=%d status=%s\n", os.Getpid(), state.Manifest.Status)
		return
	}
	for _, status := range []string{"building", "failed"} {
		t.Run(status, func(t *testing.T) {
			db, _ := queryObjectsMongoDatabase(t)
			chat, state := queryObjectsMongoSeed(t, db)
			if status == "failed" {
				state.Revision++
				state.Manifest.Status, state.Manifest.FailureCode = "failed", "SYNTHETIC_UPSERT_FAILURE"
				if err := queryObjectsMongoChatRepo(db).CompareAndSwap(context.Background(), state.Scope, 1, state); err != nil {
					t.Fatal(err)
				}
			}
			// BSON preserves registry, manifest scope and Chat binding fields hidden by json:"-".
			payload, err := bson.Marshal(queryObjectsMongoChildInput{db.Name(), *chat, state, os.Getpid()})
			if err != nil {
				t.Fatal(err)
			}
			var decoded queryObjectsMongoChildInput
			if err := bson.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("decode outbound fixture: %v", err)
			}
			queryObjectsMongoEqual(t, "fixture preserves complete state", decoded.State, state)
			queryObjectsMongoEqual(t, "fixture preserves binding fields", chatBindingFilter(&decoded.Chat), chatBindingFilter(chat))
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestQueryObjectsMongoStatePersistsAcrossProcess$", "-test.v", "-test.timeout=25s")
			cmd.Env = append(os.Environ(), "QUERY_OBJECTS_MONGO_CHILD=read-state")
			cmd.Stdin = bytes.NewReader(payload)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fresh process failed: %v\n%s", err, output)
			}
			marker := fmt.Sprintf("mongo-child-read-ok pid=%d status=%s", cmd.ProcessState.Pid(), status)
			if !strings.Contains(string(output), marker) {
				t.Fatalf("missing independent process evidence: %s", output)
			}
			t.Logf("%s", output)
		})
	}
}

// Both goroutines reach the barrier before either CAS is released. Return all
// errors to the parent testing goroutine; no Fatal calls inside competitors.
func queryObjectsMongoRace(t *testing.T, attempt func(int) (bool, error)) int {
	t.Helper()
	type result struct {
		index int
		won   bool
		err   error
	}
	ready, start, done := make(chan struct{}, 2), make(chan struct{}), make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func(i int) { ready <- struct{}{}; <-start; won, err := attempt(i); done <- result{i, won, err} }(i)
	}
	<-ready
	<-ready
	close(start)
	winner, count := -1, 0
	for i := 0; i < 2; i++ {
		r := <-done
		if r.err != nil {
			t.Errorf("CAS competitor %d: %v", r.index, r.err)
		}
		if r.won {
			winner = r.index
			count++
		}
	}
	if t.Failed() || count != 1 {
		t.Fatalf("concurrent CAS winners=%d, want exactly one", count)
	}
	return winner
}

func TestQueryObjectsMongoStateCASAndSelectionFence(t *testing.T) {
	a, b := queryObjectsMongoDatabase(t)
	chat, initial := queryObjectsMongoSeed(t, a)
	repos := []*chatRepository{queryObjectsMongoChatRepo(a), queryObjectsMongoChatRepo(b)}
	var next [2]models.State
	for i, repo := range repos {
		previous, err := repo.Read(context.Background(), initial.Scope)
		if err != nil {
			t.Fatal(err)
		}
		queryObjectsMongoEqual(t, "independent state read", previous, &initial)
		next[i] = *previous
		next[i].Revision++
		next[i].Manifest.Status = "ready"
		next[i].Manifest.ActiveGeneration = fmt.Sprintf("winner-%d", i)
		next[i].Manifest.BuildingGeneration = ""
	}
	winner := queryObjectsMongoRace(t, func(i int) (bool, error) {
		err := repos[i].CompareAndSwap(context.Background(), initial.Scope, initial.Revision, next[i])
		if errors.Is(err, ErrObjectConflict) {
			return false, nil
		}
		return err == nil, err
	})
	stored, err := repos[1].Read(context.Background(), initial.Scope)
	if err != nil {
		t.Fatal(err)
	}
	queryObjectsMongoEqual(t, "state CAS winner", stored, &next[winner])

	// Exercise actual Mongo predicates, including the stored source-key fence.
	badScopes := []schemaobjects.Scope{initial.Scope, initial.Scope, initial.Scope, initial.Scope, initial.Scope, initial.Scope}
	badScopes[0].OwnerID = primitive.NewObjectID().Hex()
	badScopes[1].ChatID = primitive.NewObjectID().Hex()
	badScopes[2].SourceKey = "another-source"
	badScopes[3].BindingRevision++
	badScopes[4].ConnectionRevision++
	badScopes[5].SelectionRevision++
	for _, scope := range badScopes {
		if _, err := repos[0].Read(context.Background(), scope); !errors.Is(err, ErrObjectConflict) {
			t.Fatalf("foreign/stale scope read accepted: %+v, %v", scope, err)
		}
		invalid := next[winner]
		invalid.Revision++
		invalid.Scope = scope
		invalid.Manifest.Scope = scope
		if err := repos[0].CompareAndSwap(context.Background(), scope, next[winner].Revision, invalid); !errors.Is(err, ErrObjectConflict) {
			t.Fatalf("foreign/stale scope CAS accepted: %v", err)
		}
	}

	var previous [2]*models.Chat
	selections := []*schemaobjects.Selection{
		{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "reporting", Name: "v_sales", Kind: schemaobjects.View}}},
		{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "reporting", Name: "v_refunds", Kind: schemaobjects.View}}},
	}
	for i, repo := range repos {
		if err := schemaobjects.ValidateSelection(*selections[i]); err != nil {
			t.Fatalf("invalid competing selection fixture: %v", err)
		}
		previous[i], err = repo.ReadCurrent(context.Background(), chat.UserID, chat.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	selectionWinner := queryObjectsMongoRace(t, func(i int) (bool, error) {
		var repo ObjectSelectionRepository = repos[i]
		err := repo.UpdateObjectSelectionCAS(context.Background(), previous[i], selections[i], "__QUERY_OBJECTS_V2__")
		if errors.Is(err, ErrObjectConflict) {
			return false, nil
		}
		return err == nil, err
	})
	current, err := repos[1].ReadCurrent(context.Background(), chat.UserID, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.SelectionRevision != chat.SelectionRevision+1 {
		t.Fatalf("selection revision=%d", current.SelectionRevision)
	}
	queryObjectsMongoEqual(t, "selection CAS winner", current.SelectedObjects, selections[selectionWinner])
	queryObjectsMongoEqual(t, "selection update preserves state/registry", current.ObjectState, &next[winner])

	late := next[winner]
	late.Revision++
	late.Manifest.ActiveGeneration = "forbidden-late-generation"
	if err := repos[0].CompareAndSwap(context.Background(), initial.Scope, next[winner].Revision, late); !errors.Is(err, ErrObjectConflict) {
		t.Fatalf("old selection published: %v", err)
	}
	if _, err := repos[0].Read(context.Background(), initial.Scope); !errors.Is(err, ErrObjectConflict) {
		t.Fatalf("old selection read accepted: %v", err)
	}
	current, err = repos[1].ReadCurrent(context.Background(), chat.UserID, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	queryObjectsMongoEqual(t, "late publish did not overwrite state", current.ObjectState, &next[winner])
	queryObjectsMongoEqual(t, "late publish did not overwrite selection", current.SelectedObjects, selections[selectionWinner])
}

func TestQueryObjectsMongoKnowledgeRoundTripCASAndLegacyFence(t *testing.T) {
	a, b := queryObjectsMongoDatabase(t)
	chat, state := queryObjectsMongoSeed(t, a)
	repos := []*knowledgeBaseRepository{queryObjectsMongoKBRepo(a), queryObjectsMongoKBRepo(b)}
	now := time.Now().UTC().Truncate(time.Millisecond)
	kb := models.KnowledgeBase{
		Base: models.Base{ID: primitive.NewObjectID(), CreatedAt: now, UpdatedAt: now}, ChatID: chat.ID, UserID: chat.UserID,
		KnowledgeVersion: 2, StorageRevision: 1, KBRevision: "kb-original",
		TableDescriptions: []models.TableDescription{
			{TableName: "reporting.v_sales", ObjectID: "object-current", InstanceVersion: "instance-current", ObjectScope: &state.Scope,
				Description: "人工原文：退款不得重复扣除。\n保留标点与换行。", Provenance: models.KnowledgeUserEdit, State: models.KnowledgeMapped, UpdatedAt: now,
				FieldDescriptions: []models.FieldDescription{{FieldName: "amount", ColumnType: "numeric", Description: "金额原文（含税）", Provenance: models.KnowledgeUserEdit, State: models.KnowledgeMapped, UpdatedAt: now}}},
			{TableName: "reporting.retired", ObjectID: "object-history", InstanceVersion: "instance-retired", ObjectScope: &state.Scope,
				Description: "旧来源原文，保留待映射", Provenance: models.KnowledgeLegacyUnknown, State: models.KnowledgePending, UpdatedAt: now,
				FieldDescriptions: []models.FieldDescription{{FieldName: "removed", Description: "删除字段的历史原文", Provenance: models.KnowledgeLegacyUnknown, State: models.KnowledgePending}}},
		},
	}
	creates := [2]models.KnowledgeBase{kb, kb}
	creates[1].ID = primitive.NewObjectID() // Same chat, different _id: requires unique chat_id.
	creator := queryObjectsMongoRace(t, func(i int) (bool, error) {
		var repo ObjectKnowledgeRepository = repos[i]
		return repo.CompareAndSwapKnowledge(context.Background(), nil, &creates[i])
	})
	stored, err := repos[1].FindByChatID(context.Background(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	queryObjectsMongoEqual(t, "manual text/provenance/binding roundtrip", stored, &creates[creator])

	var previous [2]*models.KnowledgeBase
	var next [2]models.KnowledgeBase
	for i, repo := range repos {
		previous[i], err = repo.FindByChatID(context.Background(), chat.ID)
		if err != nil || previous[i] == nil {
			t.Fatalf("read KB: %v", err)
		}
		next[i] = *previous[i]
		next[i].StorageRevision++
		next[i].KBRevision = fmt.Sprintf("kb-winner-%d", i)
	}
	winner := queryObjectsMongoRace(t, func(i int) (bool, error) {
		return repos[i].CompareAndSwapKnowledge(context.Background(), previous[i], &next[i])
	})
	stored, err = repos[0].FindByChatID(context.Background(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	queryObjectsMongoEqual(t, "KB CAS winner preserves manual/history evidence", stored, &next[winner])
	foreignPrevious, foreignNext := *stored, *stored
	foreignPrevious.UserID = primitive.NewObjectID()
	foreignNext.UserID = foreignPrevious.UserID
	foreignNext.StorageRevision++
	if ok, err := repos[1].CompareAndSwapKnowledge(context.Background(), &foreignPrevious, &foreignNext); err != nil || ok {
		t.Fatalf("foreign owner KB CAS: ok=%v err=%v", ok, err)
	}

	legacy := kb
	legacy.ID = primitive.NewObjectID()
	legacy.KnowledgeVersion = 0
	legacy.StorageRevision = 0
	legacy.TableDescriptions = []models.TableDescription{{TableName: "reporting.v_sales", Description: "must never overwrite v2"}}
	if err := repos[1].Upsert(context.Background(), &legacy); !errors.Is(err, ErrKnowledgeStorageConflict) {
		t.Fatalf("legacy Upsert not fenced: %v", err)
	}
	duplicate := next[winner]
	duplicate.ID = primitive.NewObjectID()
	if _, err := b.Collection("knowledge_bases").InsertOne(context.Background(), duplicate); !mongo.IsDuplicateKeyError(err) {
		t.Fatalf("unique chat_id index not enforced: %v", err)
	}
	count, err := a.Collection("knowledge_bases").CountDocuments(context.Background(), bson.M{"chat_id": chat.ID})
	if err != nil || count != 1 {
		t.Fatalf("duplicate chat KB count=%d err=%v", count, err)
	}
	stored, err = repos[0].FindByChatID(context.Background(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	queryObjectsMongoEqual(t, "rejected writes leave v2 and manual originals intact", stored, &next[winner])
}

// Legacy compatibility uses real Mongo writes and independent-client readback.
// Keep these cases separate: a failing ownerless create must not prevent the
// existing owner/v2 fences or historical-document promotion from being tested.
func queryObjectsMongoLegacyFixture() models.KnowledgeBase {
	kb := models.NewKnowledgeBase(primitive.NewObjectID())
	kb.CreatedAt = time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	kb.UpdatedAt = kb.CreatedAt
	kb.TableDescriptions = []models.TableDescription{{
		TableName: "reporting.legacy", Description: "人工原文：保留历史口径。\n不覆盖。",
		Provenance: models.KnowledgeLegacyUnknown, State: models.KnowledgePending,
		FieldDescriptions: []models.FieldDescription{{FieldName: "amount", Description: "历史金额原文", Provenance: models.KnowledgeLegacyUnknown, State: models.KnowledgePending}},
	}}
	return *kb
}

func queryObjectsMongoAssertKnowledge(t *testing.T, db *mongo.Database, want models.KnowledgeBase) {
	t.Helper()
	got, err := queryObjectsMongoKBRepo(db).FindByChatID(context.Background(), want.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	queryObjectsMongoEqual(t, "persisted KB including original text/revision/owner", got, &want)
	count, err := db.Collection("knowledge_bases").CountDocuments(context.Background(), bson.M{"chat_id": want.ChatID})
	if err != nil || count != 1 {
		t.Fatalf("KB count=%d, want 1; err=%v", count, err)
	}
}

func TestQueryObjectsMongoLegacyOwnerlessCreate(t *testing.T) {
	a, b := queryObjectsMongoDatabase(t)
	kb := queryObjectsMongoLegacyFixture()
	if err := queryObjectsMongoKBRepo(a).Upsert(context.Background(), &kb); err != nil {
		t.Fatalf("ownerless legacy create must remain compatible: %v", err)
	}
	want := kb
	want.StorageRevision = 1
	queryObjectsMongoAssertKnowledge(t, b, want)
}

func TestQueryObjectsMongoLegacyOwnerlessUpdateAndPromotion(t *testing.T) {
	for _, shape := range []string{"missing_owner_and_revision", "zero_owner_revision_7"} {
		for _, operation := range []string{"ownerless_update", "legacy_owner_claim", "v2_promotion"} {
			t.Run(shape+"/"+operation, func(t *testing.T) {
				a, b := queryObjectsMongoDatabase(t)
				previous := queryObjectsMongoLegacyFixture()
				if shape == "zero_owner_revision_7" {
					previous.StorageRevision = 7
				}
				encoded, err := bson.Marshal(previous)
				if err != nil {
					t.Fatal(err)
				}
				var historical bson.M
				if err := bson.Unmarshal(encoded, &historical); err != nil {
					t.Fatal(err)
				}
				if shape == "missing_owner_and_revision" {
					delete(historical, "user_id")
					delete(historical, "storage_revision")
				}
				// Seed the historical BSON shape directly so update/promotion are
				// exercised even while the separate ownerless create case is RED.
				if _, err := a.Collection("knowledge_bases").InsertOne(context.Background(), historical); err != nil {
					t.Fatal(err)
				}
				queryObjectsMongoAssertKnowledge(t, b, previous)
				next := previous
				repo := queryObjectsMongoKBRepo(b)
				switch operation {
				case "ownerless_update":
					next.TableDescriptions = append([]models.TableDescription(nil), previous.TableDescriptions...)
					next.TableDescriptions[0].Description = "更新对象说明；字段原文仍保留。"
					if err := repo.Upsert(context.Background(), &next); err != nil {
						t.Fatalf("ownerless legacy update must remain compatible: %v", err)
					}
					next.StorageRevision = previous.StorageRevision + 1
				case "legacy_owner_claim":
					next.UserID = primitive.NewObjectID()
					if err := repo.Upsert(context.Background(), &next); err != nil {
						t.Fatalf("owned legacy write must accept ownerless history: %v", err)
					}
					next.StorageRevision = previous.StorageRevision + 1
				case "v2_promotion":
					next.UserID = primitive.NewObjectID()
					next.KnowledgeVersion = 2
					next.StorageRevision = previous.StorageRevision + 1
					next.KBRevision = "promoted-with-original-history"
					next.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
					// Promotion still requires a real owner; compatibility must not
					// relax v2's input contract or write anything on rejection.
					ownerlessV2 := next
					ownerlessV2.UserID = primitive.NilObjectID
					if ok, err := repo.CompareAndSwapKnowledge(context.Background(), &previous, &ownerlessV2); ok || !errors.Is(err, ErrKnowledgeStorageConflict) {
						t.Fatalf("ownerless v2 promotion accepted: ok=%v err=%v", ok, err)
					}
					queryObjectsMongoAssertKnowledge(t, a, previous)
					if ok, err := repo.CompareAndSwapKnowledge(context.Background(), &previous, &next); err != nil || !ok {
						t.Fatalf("ownerless history promotion failed: ok=%v err=%v", ok, err)
					}
					if ok, err := repo.CompareAndSwapKnowledge(context.Background(), &previous, &next); err != nil || ok {
						t.Fatalf("stale promotion accepted: ok=%v err=%v", ok, err)
					}
				}
				queryObjectsMongoAssertKnowledge(t, a, next)
			})
		}
	}
}

func TestQueryObjectsMongoLegacyOwnerlessWriteFences(t *testing.T) {
	for _, storedKind := range []string{"owned_legacy", "v2"} {
		for _, writer := range []string{"ownerless", "foreign_owner"} {
			t.Run(storedKind+"/"+writer, func(t *testing.T) {
				a, b := queryObjectsMongoDatabase(t)
				kb := queryObjectsMongoLegacyFixture()
				kb.UserID = primitive.NewObjectID()
				repo := queryObjectsMongoKBRepo(a)
				if storedKind == "v2" {
					kb.KnowledgeVersion, kb.StorageRevision, kb.KBRevision = 2, 1, "protected-v2"
					if ok, err := repo.CompareAndSwapKnowledge(context.Background(), nil, &kb); err != nil || !ok {
						t.Fatalf("seed owned v2: ok=%v err=%v", ok, err)
					}
				} else {
					if err := repo.Upsert(context.Background(), &kb); err != nil {
						t.Fatalf("seed owned legacy: %v", err)
					}
					kb.StorageRevision = 1
				}
				queryObjectsMongoAssertKnowledge(t, b, kb)
				var original bson.M
				if err := b.Collection("knowledge_bases").FindOne(context.Background(), bson.M{"chat_id": kb.ChatID}).Decode(&original); err != nil {
					t.Fatal(err)
				}
				// Different _id ensures a failed predicate must be fenced by the
				// unique chat_id index, not merely an accidental _id collision.
				attack := queryObjectsMongoLegacyFixture()
				attack.ChatID = kb.ChatID
				if writer == "foreign_owner" {
					attack.UserID = primitive.NewObjectID()
				}
				attack.TableDescriptions = []models.TableDescription{{TableName: "reporting.legacy", Description: "must not replace original text or fields"}}
				if err := queryObjectsMongoKBRepo(b).Upsert(context.Background(), &attack); !errors.Is(err, ErrKnowledgeStorageConflict) {
					t.Fatalf("%s legacy write overwrote %s: %v", writer, storedKind, err)
				}
				queryObjectsMongoAssertKnowledge(t, a, kb)
				var after bson.M
				if err := a.Collection("knowledge_bases").FindOne(context.Background(), bson.M{"chat_id": kb.ChatID}).Decode(&after); err != nil {
					t.Fatal(err)
				}
				queryObjectsMongoEqual(t, "rejected legacy write leaves every BSON field unchanged", after, original)
			})
		}
	}
}
