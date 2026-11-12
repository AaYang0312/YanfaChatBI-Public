//go:build object_index_integration

package services

// These tests use the production index, Mongo repositories/CAS and Qdrant
// ObjectStore. Only catalog snapshots and deterministic embeddings are synthetic.
// Run on the manager's isolated Linux stack; missing configuration is fatal.
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const objectRuntimeChildEnv = "OBJECT_INDEX_RUNTIME_CHILD"
const objectRuntimeCrashExit = 86
const objectRuntimeManual = "人工原文：退款不得重复扣除。\n保留标点与换行。"
const objectRuntimeField = "金额原文（含税），不得改写。"

var objectRuntimeDBName = regexp.MustCompile("^object_index_test_[0-9a-f]{32}$")

type objectRuntimeWire struct {
	Database, Collection, TestName, Mode string
	ParentPID                            int
	Snapshot                             schemaobjects.Snapshot
	Knowledge                            models.KnowledgeBase
	Expected                             models.State
}

type objectRuntimeFixture struct {
	ctx         context.Context
	db          *mongo.Database
	chat        repositories.ChatRepository
	state       repositories.ObjectStateRepository
	kb          repositories.KnowledgeBaseRepository
	store       *vectordb.QdrantObjectStore
	collections pb.CollectionsClient
	wire        objectRuntimeWire
}

// This fake has no clients, credentials, I/O or failure/control hooks.
type objectRuntimeEmbedding struct{}

func (objectRuntimeEmbedding) GetDimension() int { return 3 }
func (objectRuntimeEmbedding) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for n := range vectors {
		vectors[n] = []float32{1, 0, 0}
	}
	return vectors, ctx.Err()
}
func (objectRuntimeEmbedding) EmbedQuery(ctx context.Context, _ string) ([]float32, error) {
	return []float32{1, 0, 0}, ctx.Err()
}

func objectRuntimeEndpoints(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "mongodb" || u.Host != "127.0.0.1:18546" ||
		(u.Path != "" && u.Path != "/") || u.Fragment != "" || u.Opaque != "" {
		t.Fatal("requires explicit ASSISTANT_TEST_MONGO_URI=mongodb://[credentials@]127.0.0.1:18546/ with no database path")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		t.Fatal("invalid isolated Mongo URI options")
	}
	for key, values := range query {
		if len(values) != 1 || (key != "authSource" && key != "directConnection") ||
			(key == "directConnection" && values[0] != "true") ||
			(key == "authSource" && values[0] == "") {
			t.Fatal("isolated Mongo URI permits only authSource and directConnection=true")
		}
	}
	if os.Getenv("SOURCE_ACCESS_TEST_QDRANT_ADDR") != "127.0.0.1:6334" {
		t.Fatal("requires explicit SOURCE_ACCESS_TEST_QDRANT_ADDR=127.0.0.1:6334")
	}
	return uri
}

func objectRuntimeConnectMongo(t *testing.T) *mongo.Client {
	t.Helper()
	opts := options.Client().ApplyURI(objectRuntimeEndpoints(t)).SetDirect(true).
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
			t.Errorf("disconnect own Mongo client: %v", err)
		}
	})
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("isolated Mongo unavailable (not skipped): %v", err)
	}
	return client
}

func objectRuntimeOpen(t *testing.T, wire objectRuntimeWire) *objectRuntimeFixture {
	t.Helper()
	objectRuntimeEndpoints(t) // Validate both addresses before making any connection.
	if !objectRuntimeDBName.MatchString(wire.Database) || wire.Collection != wire.Database+"_v2" {
		t.Fatal("invalid test-owned database/collection fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	client := objectRuntimeConnectMongo(t)
	db := client.Database(wire.Database)
	mc := &mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: wire.Database}}
	chat := repositories.NewChatRepository(mc, nil)
	state, ok := chat.(repositories.ObjectStateRepository)
	if !ok {
		t.Fatal("production chat repository lacks ObjectStateRepository")
	}
	q, err := vectordb.NewQdrantClient(vectordb.QdrantConfig{
		Host: "127.0.0.1", Port: "6334",
		SchemaCollection: wire.Database + "_schema_unused", MessageCollection: wire.Database + "_messages_unused",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := q.Close(); err != nil {
			t.Errorf("close own Qdrant client: %v", err)
		}
	})
	store, err := vectordb.NewQdrantObjectStore(q, wire.Collection)
	if err != nil {
		t.Fatal(err)
	}
	// This separate connection only checks/deletes this case's physical v2
	// collection. It never creates, lists, reads or drops legacy collections.
	conn, err := grpc.NewClient("127.0.0.1:6334", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &objectRuntimeFixture{ctx: ctx, db: db, chat: chat, state: state,
		kb: repositories.NewKnowledgeBaseRepository(mc), store: store,
		collections: pb.NewCollectionsClient(conn), wire: wire}
}

func objectRuntimeNew(t *testing.T) *objectRuntimeFixture {
	t.Helper()
	objectRuntimeEndpoints(t)
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "object_index_test_" + hex.EncodeToString(token[:])
	f := objectRuntimeOpen(t, objectRuntimeWire{Database: name, Collection: name + "_v2"})
	collections, err := f.db.ListCollectionNames(f.ctx, bson.D{})
	if err != nil || len(collections) != 0 {
		t.Fatalf("refusing nonempty/unverified random Mongo database: collections=%v err=%v", collections, err)
	}
	_, err = f.collections.Get(f.ctx, &pb.GetCollectionInfoRequest{CollectionName: f.wire.Collection})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("refusing existing/unverified random v2 collection: %v", err)
	}
	ownedDB, ownedCollection := name, name+"_v2"
	t.Cleanup(func() {
		if !objectRuntimeDBName.MatchString(ownedDB) || ownedCollection != ownedDB+"_v2" {
			t.Error("refusing cleanup outside this case's captured resource names")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Parent alone owns cleanup; children only disconnect their own clients.
		if _, err := f.collections.Delete(ctx, &pb.DeleteCollection{CollectionName: ownedCollection}); err != nil && status.Code(err) != codes.NotFound {
			t.Errorf("drop own v2 collection %s: %v", ownedCollection, err)
		}
		if err := f.db.Drop(ctx); err != nil {
			t.Errorf("drop own Mongo database %s: %v", ownedDB, err)
		}
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	chat := models.Chat{
		Base:   models.Base{ID: primitive.NewObjectID(), CreatedAt: now, UpdatedAt: now},
		UserID: primitive.NewObjectID(), SourceID: "synthetic-index-source",
		SourceBindingRevision: 2, ConnectionRevision: 3, SelectionRevision: 4,
		SourceBindingConfirmed: true, ERPAssociated: true, SourceConnectionFingerprint: "synthetic-index-binding",
		SelectedCollections: "__QUERY_OBJECTS_V2__",
		SelectedObjects:     &schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.View}},
	}
	scope := schemaobjects.Scope{OwnerID: chat.UserID.Hex(), ChatID: chat.ID.Hex(), SourceKey: chat.SourceID,
		BindingRevision: 2, ConnectionRevision: 3, SelectionRevision: 4}
	objects := []schemaobjects.Object{
		{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "reports", Name: "sales_view", Kind: schemaobjects.View}, CatalogOID: 41001, DefinitionStatus: "unavailable"}, RowCountStatus: "unknown"},
		{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "reports", Name: "restricted_view", Kind: schemaobjects.View}, CatalogOID: 41002, DefinitionStatus: "unavailable"}, RowCountStatus: "unknown",
			Columns: []schemaobjects.Column{{Name: "restricted_amount", Type: "numeric", Ordinal: 1}}},
	}
	for n := 0; n < 55; n++ {
		objects[0].Columns = append(objects[0].Columns, schemaobjects.Column{Name: fmt.Sprintf("column_%02d", n), Type: "numeric", Ordinal: n + 1})
	}
	snapshot, err := schemaobjects.Reconcile(scope, nil, objects)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Collection("chats").InsertOne(f.ctx, &chat); err != nil {
		t.Fatal(err)
	}
	initial := models.State{Revision: 1, Scope: scope, Registry: snapshot.Registry}
	if err := f.state.CompareAndSwap(f.ctx, scope, 0, initial); err != nil {
		t.Fatalf("seed real Mongo state CAS: %v", err)
	}
	var sales schemaobjects.Object
	for _, object := range snapshot.Objects {
		if object.Metadata.Ref.Name == "sales_view" {
			sales = object
		}
	}
	if sales.Metadata.ObjectID == "" {
		t.Fatal("missing synthetic sales view")
	}
	kb := models.KnowledgeBase{
		Base: models.Base{ID: primitive.NewObjectID(), CreatedAt: now, UpdatedAt: now}, ChatID: chat.ID, UserID: chat.UserID,
		KnowledgeVersion: 2, StorageRevision: 1, KBRevision: "synthetic-manual-kb-1",
		TableDescriptions: []models.TableDescription{{
			TableName: "reports.sales_view", ObjectID: sales.Metadata.ObjectID, InstanceVersion: sales.Metadata.InstanceVersion, ObjectScope: &scope,
			Description: objectRuntimeManual, Provenance: models.KnowledgeUserEdit, State: models.KnowledgeMapped, UpdatedAt: now,
			FieldDescriptions: []models.FieldDescription{{FieldName: "column_00", ColumnType: "numeric", Description: objectRuntimeField,
				Provenance: models.KnowledgeUserEdit, State: models.KnowledgeMapped, UpdatedAt: now}},
		}},
	}
	kbCAS, ok := f.kb.(repositories.ObjectKnowledgeRepository)
	if !ok {
		t.Fatal("production KB repository lacks CAS")
	}
	if won, err := kbCAS.CompareAndSwapKnowledge(f.ctx, nil, &kb); err != nil || !won {
		t.Fatalf("seed real manual KB CAS: won=%v err=%v", won, err)
	}
	f.wire.Snapshot, f.wire.Knowledge, f.wire.Expected = snapshot, kb, initial
	objectRuntimeEqual(t, "three fixture chunks", len(f.chunks(t)), 3)
	return f
}

func objectRuntimeEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch:\n got: %#v\nwant: %#v", label, got, want)
	}
}

func (f *objectRuntimeFixture) knowledge(t *testing.T) *models.KnowledgeBase {
	t.Helper()
	got, err := f.kb.FindByChatID(f.ctx, f.wire.Knowledge.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	objectRuntimeEqual(t, "real KB text/provenance/field/scope/storage revision", got, &f.wire.Knowledge)
	return got
}

func (f *objectRuntimeFixture) chunks(t *testing.T) []SchemaChunk {
	t.Helper()
	kb := f.knowledge(t) // Enrichment comes from real persisted KB, not the wire copy.
	enrichment := map[string]ObjectIndexEnrichment{}
	for _, object := range f.wire.Snapshot.Objects {
		for _, table := range kb.TableDescriptions {
			if table.ObjectID == object.Metadata.ObjectID && table.InstanceVersion == object.Metadata.InstanceVersion {
				fields := map[string]string{}
				for _, field := range table.FieldDescriptions {
					fields[field.FieldName] = field.Description
				}
				enrichment[table.ObjectID] = ObjectIndexEnrichment{Description: table.Description, Fields: fields}
			}
		}
	}
	chunks, err := BuildObjectSchemaChunks(f.wire.Snapshot, kb.KBRevision, enrichment)
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

func (f *objectRuntimeFixture) index(t *testing.T, store vectordb.ObjectStore) ObjectIndex {
	t.Helper()
	index, err := NewObjectIndex(f.state, store, objectRuntimeEmbedding{}, func(ctx context.Context) (schemaobjects.Snapshot, string, error) {
		kb, err := f.kb.FindByChatID(ctx, f.wire.Knowledge.ChatID)
		if err != nil || kb == nil {
			return schemaobjects.Snapshot{}, "", fmt.Errorf("read real KB freshness: %v", err)
		}
		// The synthetic selected catalog deliberately stays fixed for this task.
		// Mongo, not a fake state/freshness hook, must fence a late selection.
		return f.wire.Snapshot, kb.KBRevision, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func (f *objectRuntimeFixture) read(t *testing.T) models.State {
	t.Helper()
	state, err := f.state.Read(f.ctx, f.wire.Snapshot.Scope)
	if err != nil || state == nil {
		t.Fatalf("read real index state: state=%+v err=%v", state, err)
	}
	return *state
}

func (f *objectRuntimeFixture) ready(t *testing.T, index ObjectIndex, revision int64) models.State {
	t.Helper()
	state := f.read(t)
	objectRuntimeEqual(t, "state revision", state.Revision, revision)
	objectRuntimeEqual(t, "state scope", state.Scope, f.wire.Snapshot.Scope)
	objectRuntimeEqual(t, "state registry", state.Registry, f.wire.Snapshot.Registry)
	if _, err := uuid.Parse(state.Manifest.ActiveGeneration); err != nil {
		t.Fatalf("active generation is not UUID: %+v", state.Manifest)
	}
	want := models.Manifest{FormatVersion: 2, Status: "ready", Scope: f.wire.Snapshot.Scope,
		SchemaFingerprint: f.wire.Snapshot.Fingerprint, KBRevision: f.wire.Knowledge.KBRevision,
		ActiveGeneration: state.Manifest.ActiveGeneration, ExpectedPoints: len(f.chunks(t))}
	objectRuntimeEqual(t, "durable ready manifest", state.Manifest, want)
	manifest, err := index.Status(f.ctx, f.wire.Snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	objectRuntimeEqual(t, "production Status", manifest, want)
	f.points(t, state.Manifest.ActiveGeneration, f.chunks(t))
	f.search(t, index, state.Manifest.ActiveGeneration)
	f.knowledge(t)
	return state
}

// Independently spell out the public payload fence instead of reusing the
// production index's filter builder (which these tests are meant to check).
func objectRuntimeFilter(scope schemaobjects.Scope, generation string) map[string]string {
	return map[string]string{
		"owner_id": scope.OwnerID, "chat_id": scope.ChatID, "source_key": scope.SourceKey,
		"source_binding_revision": strconv.FormatInt(scope.BindingRevision, 10),
		"connection_revision":     strconv.FormatInt(scope.ConnectionRevision, 10),
		"selection_revision":      strconv.FormatInt(scope.SelectionRevision, 10),
		"index_format_version":    "2", "generation_id": generation,
	}
}
func objectRuntimeConditions(scope schemaobjects.Scope, generation string) []vectordb.FilterCondition {
	conditions := []vectordb.FilterCondition{}
	for key, value := range objectRuntimeFilter(scope, generation) {
		conditions = append(conditions, vectordb.FilterCondition{Key: key, Value: value})
	}
	return conditions
}

func (f *objectRuntimeFixture) expectedPoint(t *testing.T, generation string, chunk SchemaChunk) (string, map[string]interface{}) {
	t.Helper()
	var object schemaobjects.Object
	for _, candidate := range f.wire.Snapshot.Objects {
		if candidate.Metadata.ObjectID == chunk.Metadata["object_id"] {
			object = candidate
		}
	}
	if object.Metadata.ObjectID == "" {
		t.Fatal("chunk does not belong to fixture snapshot")
	}
	scope := f.wire.Snapshot.Scope
	key, err := json.Marshal([]any{"query-object-v2", scope, generation, object.Metadata.ObjectID, object.Metadata.InstanceVersion, chunk.Metadata["part_index"]})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, key).String()
	payload := map[string]interface{}{}
	for key, value := range objectRuntimeFilter(scope, generation) {
		payload[key] = value
	}
	identifier := "\"reports\".\"" + object.Metadata.Ref.Name + "\""
	columns, _ := json.Marshal(chunk.ColumnNames)
	digest := sha256.Sum256([]byte(chunk.Content))
	for key, value := range map[string]string{
		"object_id": object.Metadata.ObjectID, "object_instance_version": object.Metadata.InstanceVersion,
		"object_type": "view", "schema_name": "reports", "object_name": object.Metadata.Ref.Name,
		"table_name": identifier, "sql_identifier": identifier,
		"schema_fingerprint": object.Metadata.Fingerprint, "snapshot_fingerprint": f.wire.Snapshot.Fingerprint,
		"kb_revision": f.wire.Knowledge.KBRevision, "part_index": chunk.Metadata["part_index"],
		"total_parts": chunk.Metadata["total_parts"], "type": "schema", "content": chunk.Content,
		"content_digest": hex.EncodeToString(digest[:]), "column_names": string(columns), "row_count_status": "unknown",
	} {
		payload[key] = value
	}
	return id, payload
}

func (f *objectRuntimeFixture) assertPayloads(t *testing.T, generation string, hits []vectordb.SearchResult, chunks []SchemaChunk) {
	t.Helper()
	want := map[string]map[string]interface{}{}
	for _, chunk := range chunks {
		id, payload := f.expectedPoint(t, generation, chunk)
		want[id] = payload
	}
	for _, hit := range hits {
		payload, exists := want[hit.ID]
		if !exists {
			t.Fatalf("unknown/duplicate/fallback/foreign-generation point: id=%s payload=%+v", hit.ID, hit.Payload)
		}
		objectRuntimeEqual(t, "current complete point payload "+hit.ID, hit.Payload, payload)
		delete(want, hit.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing exact pointset: %+v", want)
	}
}

func (f *objectRuntimeFixture) points(t *testing.T, generation string, chunks []SchemaChunk) {
	t.Helper()
	points, err := f.store.Scroll(f.ctx, objectRuntimeConditions(f.wire.Snapshot.Scope, generation))
	if err != nil {
		t.Fatal(err)
	}
	hits := make([]vectordb.SearchResult, len(points))
	for n, point := range points {
		objectRuntimeEqual(t, "real normalized deterministic vector", point.Vector, []float32{1, 0, 0})
		hits[n] = vectordb.SearchResult{ID: point.ID, Payload: point.Payload}
	}
	f.assertPayloads(t, generation, hits, chunks)
}

func (f *objectRuntimeFixture) search(t *testing.T, index ObjectIndex, generation string) {
	t.Helper()
	hits, err := index.Search(f.ctx, f.wire.Snapshot, f.wire.Knowledge.KBRevision, "view", 50)
	if err != nil {
		t.Fatal(err)
	}
	// Both views and both parts of the wide view fit the production per-object
	// budget. Comparing UUIDs and every payload field also rejects fallback text.
	f.assertPayloads(t, generation, hits, f.chunks(t))
}

// All fault/scheduling hooks wrap a REAL store; no state or vector operation is
// replaced by in-memory storage. Upsert below commits only the first real point.
type objectRuntimeCrashStore struct {
	vectordb.ObjectStore
	t *testing.T
}

func (s *objectRuntimeCrashStore) Upsert(ctx context.Context, points []vectordb.VectorPoint) error {
	if len(points) < 2 {
		s.t.Fatal("crash fixture must have at least two candidate points")
	}
	if err := s.ObjectStore.Upsert(ctx, points[:1]); err != nil {
		return err // Parent demands exit 86, so a dependency failure cannot pass.
	}
	fmt.Printf("object-index-partial-upsert pid=%d point=%s generation=%s\n",
		os.Getpid(), points[0].ID, points[0].Payload["generation_id"])
	os.Exit(objectRuntimeCrashExit) // Bypass Build's deferred failure transition.
	return errors.New("unreachable after controlled child exit")
}

type objectRuntimeAfterScrollStore struct {
	vectordb.ObjectStore
	after func()
}

func (s *objectRuntimeAfterScrollStore) Scroll(ctx context.Context, filters []vectordb.FilterCondition) ([]vectordb.VectorPoint, error) {
	points, err := s.ObjectStore.Scroll(ctx, filters)
	if err == nil && s.after != nil {
		after := s.after
		s.after = nil
		after()
	}
	return points, err
}

func (f *objectRuntimeFixture) runChild(t *testing.T, mode string, expected models.State, exitCode int) {
	t.Helper()
	wire := f.wire
	wire.TestName, wire.Mode, wire.ParentPID, wire.Expected = t.Name(), mode, os.Getpid(), expected
	// BSON (not JSON) retains State.Registry, Manifest.Scope and KB revisions.
	payload, err := bson.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip objectRuntimeWire
	if err := bson.Unmarshal(payload, &roundtrip); err != nil {
		t.Fatal(err)
	}
	objectRuntimeEqual(t, "BSON state fixture", roundtrip.Expected, wire.Expected)
	objectRuntimeEqual(t, "BSON KB fixture", roundtrip.Knowledge, wire.Knowledge)
	objectRuntimeEqual(t, "BSON snapshot fixture", roundtrip.Snapshot, wire.Snapshot)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v", "-test.timeout=60s")
	// Avoid duplicate inherited mode variables; only this exact child test runs.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, objectRuntimeChildEnv+"=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, objectRuntimeChildEnv+"="+mode)
	cmd.Stdin = bytes.NewReader(payload)
	output, runErr := cmd.CombinedOutput()
	if cmd.ProcessState == nil {
		t.Fatalf("child failed to start: %v\n%s", runErr, output)
	}
	gotExit := cmd.ProcessState.ExitCode()
	if ctx.Err() != nil || gotExit != exitCode || (exitCode == 0 && runErr != nil) {
		t.Fatalf("child mode=%s pid=%d exit=%d want=%d err=%v timeout=%v\n%s",
			mode, cmd.ProcessState.Pid(), gotExit, exitCode, runErr, ctx.Err(), output)
	}
	if exitCode != 0 {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != exitCode {
			t.Fatalf("missing controlled subprocess exit evidence: %v\n%s", runErr, output)
		}
	}
	marker := fmt.Sprintf("object-index-child-ok mode=%s pid=%d", mode, cmd.ProcessState.Pid())
	if mode == "crash" {
		marker = fmt.Sprintf("object-index-partial-upsert pid=%d ", cmd.ProcessState.Pid())
	}
	if !strings.Contains(string(output), marker) || cmd.ProcessState.Pid() == os.Getpid() {
		t.Fatalf("missing independent child evidence %q:\n%s", marker, output)
	}
	t.Logf("child mode=%s pid=%d exit=%d\n%s", mode, cmd.ProcessState.Pid(), gotExit, output)
}

func objectRuntimeChild(t *testing.T) bool {
	t.Helper()
	mode := os.Getenv(objectRuntimeChildEnv)
	if mode == "" {
		return false
	}
	objectRuntimeEndpoints(t)
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	var wire objectRuntimeWire
	if err := bson.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("decode BSON subprocess fixture: %v", err)
	}
	if wire.Mode != mode || wire.TestName != t.Name() || wire.ParentPID <= 0 || wire.ParentPID == os.Getpid() {
		t.Fatal("invalid isolated subprocess identity")
	}
	f := objectRuntimeOpen(t, wire)
	objectRuntimeEqual(t, "fresh process full Mongo state", f.read(t), wire.Expected)
	f.knowledge(t)
	index := f.index(t, f.store)
	switch mode {
	case "build", "recover":
		if err := index.Build(f.ctx, wire.Snapshot, wire.Knowledge.KBRevision, f.chunks(t)); err != nil {
			t.Fatalf("real production Build: %v", err)
		}
		state := f.ready(t, index, wire.Expected.Revision+2)
		if state.Manifest.ActiveGeneration == wire.Expected.Manifest.ActiveGeneration ||
			state.Manifest.ActiveGeneration == wire.Expected.Manifest.BuildingGeneration {
			t.Fatal("build reused an old or incomplete generation")
		}
	case "read":
		objectRuntimeEqual(t, "read-only restart ready state", f.ready(t, index, wire.Expected.Revision), wire.Expected)
	case "read-building":
		manifest, err := index.Status(f.ctx, wire.Snapshot.Scope)
		if err != nil {
			t.Fatal(err)
		}
		objectRuntimeEqual(t, "restart building manifest", manifest, wire.Expected.Manifest)
		f.points(t, manifest.ActiveGeneration, f.chunks(t))
		f.points(t, manifest.BuildingGeneration, f.chunks(t)[:1])
		f.search(t, index, manifest.ActiveGeneration)
		objectRuntimeEqual(t, "read-only restart preserved building state", f.read(t), wire.Expected)
	case "crash":
		crashing := f.index(t, &objectRuntimeCrashStore{ObjectStore: f.store, t: t})
		err := crashing.Build(f.ctx, wire.Snapshot, wire.Knowledge.KBRevision, f.chunks(t))
		t.Fatalf("controlled crash hook did not exit: %v", err)
	default:
		t.Fatalf("unknown child mode %q", mode)
	}
	f.knowledge(t)
	fmt.Printf("object-index-child-ok mode=%s pid=%d\n", mode, os.Getpid())
	return true
}

// Catches publication without durable ready state, lost generation on restart,
// stale payloads, fake structure fallbacks and incomplete Qdrant pointsets.
func TestObjectIndexRuntimeReadySurvivesProcessRestart(t *testing.T) {
	if objectRuntimeChild(t) {
		return
	}
	f := objectRuntimeNew(t)
	f.runChild(t, "build", f.wire.Expected, 0)
	index := f.index(t, f.store)
	ready := f.ready(t, index, 3)
	// Builder has exited; this second child cannot inherit its heap/clients.
	f.runChild(t, "read", ready, 0)
	objectRuntimeEqual(t, "restart did not rebuild", f.read(t), ready)
	f.knowledge(t)
}

// Catches early publication, loss of the previous active generation, orphan
// visibility, inability to recover after process death, and any manual KB loss.
func TestObjectIndexRuntimePartialUpsertCrashRecovery(t *testing.T) {
	if objectRuntimeChild(t) {
		return
	}
	f := objectRuntimeNew(t)
	f.runChild(t, "build", f.wire.Expected, 0)
	index := f.index(t, f.store)
	old := f.ready(t, index, 3)
	f.runChild(t, "crash", old, objectRuntimeCrashExit)
	building := f.read(t)
	if _, err := uuid.Parse(building.Manifest.BuildingGeneration); err != nil ||
		building.Manifest.BuildingGeneration == old.Manifest.ActiveGeneration {
		t.Fatalf("missing distinct durable candidate generation: %+v", building)
	}
	want := old
	want.Revision++
	want.Manifest.Status = "building"
	want.Manifest.BuildingGeneration = building.Manifest.BuildingGeneration
	objectRuntimeEqual(t, "abrupt death retains building + old active", building, want)
	manifest, err := index.Status(f.ctx, f.wire.Snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	objectRuntimeEqual(t, "post-crash status", manifest, want.Manifest)
	f.points(t, old.Manifest.ActiveGeneration, f.chunks(t))
	f.points(t, building.Manifest.BuildingGeneration, f.chunks(t)[:1])
	f.search(t, index, old.Manifest.ActiveGeneration)
	f.knowledge(t)
	f.runChild(t, "read-building", building, 0)
	f.runChild(t, "recover", building, 0)
	recovered := f.ready(t, index, building.Revision+2)
	if recovered.Manifest.ActiveGeneration == old.Manifest.ActiveGeneration ||
		recovered.Manifest.ActiveGeneration == building.Manifest.BuildingGeneration {
		t.Fatal("recovery activated an old/incomplete generation")
	}
	f.points(t, old.Manifest.ActiveGeneration, nil)
	// Production deliberately does not sweep unknown generations. Prove that
	// the persisted orphan still exists but cannot leak through Search.
	f.points(t, building.Manifest.BuildingGeneration, f.chunks(t)[:1])
	f.search(t, index, recovered.Manifest.ActiveGeneration)
	f.runChild(t, "read", recovered, 0)
	f.knowledge(t)
}

// Catches a stale builder/failure cleanup overwriting a newer selection's
// publication, or new-scope Search returning the removed view or old points.
func TestObjectIndexRuntimeSelectionCASBlocksLatePublish(t *testing.T) {
	if objectRuntimeChild(t) {
		return
	}
	f := objectRuntimeNew(t)
	f.runChild(t, "build", f.wire.Expected, 0)
	old := f.ready(t, f.index(t, f.store), 3)
	// A second real Mongo client performs selection and state CAS independently.
	mc := objectRuntimeConnectMongo(t)
	otherChat := repositories.NewChatRepository(&mongodb.MongoDBClient{
		Client: mc, Config: mongodb.MongoDbConfigModel{DatabaseName: f.wire.Database}}, nil)
	otherState := otherChat.(repositories.ObjectStateRepository)
	selectionRepo := otherChat.(repositories.ObjectSelectionRepository)
	var pending, winner models.State
	var narrowed *objectRuntimeFixture
	hookCalled := false
	wrapped := &objectRuntimeAfterScrollStore{ObjectStore: f.store}
	wrapped.after = func() {
		hookCalled = true
		pending = f.read(t)
		if pending.Manifest.Status != "building" || pending.Manifest.ActiveGeneration != old.Manifest.ActiveGeneration ||
			pending.Manifest.BuildingGeneration == "" || pending.Manifest.BuildingGeneration == old.Manifest.ActiveGeneration {
			t.Fatalf("selection hook did not interrupt a candidate before publish: %+v", pending)
		}
		// All candidate points really exist before the competing selection wins.
		f.points(t, pending.Manifest.BuildingGeneration, f.chunks(t))
		previous, err := otherChat.ReadCurrent(f.ctx, f.wire.Knowledge.UserID, f.wire.Knowledge.ChatID)
		if err != nil {
			t.Fatal(err)
		}
		selection := &schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{
			{Schema: "reports", Name: "sales_view", Kind: schemaobjects.View},
		}}
		if err := selectionRepo.UpdateObjectSelectionCAS(f.ctx, previous, selection, "__QUERY_OBJECTS_V2__"); err != nil {
			t.Fatal(err)
		}
		current, err := otherChat.ReadCurrent(f.ctx, previous.UserID, previous.ID)
		if err != nil {
			t.Fatal(err)
		}
		objectRuntimeEqual(t, "selection revision advanced", current.SelectionRevision, previous.SelectionRevision+1)
		objectRuntimeEqual(t, "narrow selected view", current.SelectedObjects, selection)
		objectRuntimeEqual(t, "selection CAS preserves interrupted state", current.ObjectState, &pending)

		// Isolate the selection fence: state revision is STILL pending.Revision,
		// so an ordinary revision mismatch cannot explain this CAS rejection.
		stalePublish := pending
		stalePublish.Revision++
		stalePublish.Manifest.Status = "ready"
		stalePublish.Manifest.ActiveGeneration = pending.Manifest.BuildingGeneration
		stalePublish.Manifest.BuildingGeneration = ""
		if err := f.state.CompareAndSwap(f.ctx, f.wire.Snapshot.Scope, pending.Revision, stalePublish); !errors.Is(err, repositories.ErrObjectConflict) {
			t.Fatalf("selection-only fence accepted old publisher at unchanged state revision: %v", err)
		}
		afterRejectedCAS, err := otherChat.ReadCurrent(f.ctx, previous.UserID, previous.ID)
		if err != nil {
			t.Fatal(err)
		}
		objectRuntimeEqual(t, "selection-only rejection preserves pending state", afterRejectedCAS.ObjectState, &pending)
		objectRuntimeEqual(t, "selection-only rejection preserves narrow selection", afterRejectedCAS.SelectedObjects, selection)
		scope := f.wire.Snapshot.Scope
		scope.SelectionRevision = current.SelectionRevision
		var selected []schemaobjects.Object
		for _, object := range f.wire.Snapshot.Objects {
			if object.Metadata.Ref.Name == "sales_view" {
				selected = append(selected, object)
			}
		}
		snapshot, err := schemaobjects.Reconcile(scope, f.wire.Snapshot.Registry, selected)
		if err != nil {
			t.Fatal(err)
		}
		copyFixture := *f
		copyFixture.wire.Snapshot = snapshot
		copyFixture.chat, copyFixture.state = otherChat, otherState
		narrowed = &copyFixture
		narrowIndex := narrowed.index(t, narrowed.store)
		stale, err := narrowIndex.Status(f.ctx, scope)
		if err != nil || stale.Status != "stale" || stale.ActiveGeneration != "" {
			t.Fatalf("new selection exposed old active manifest: %+v %v", stale, err)
		}
		// Before a new generation exists, only the newly selected structure may
		// appear. No stored manual/old-generation payload is authorized.
		hits, err := narrowIndex.Search(f.ctx, snapshot, f.wire.Knowledge.KBRevision, "restricted_view", 50)
		if err != nil || len(hits) != 1 || hits[0].Payload["object_name"] != "sales_view" ||
			hits[0].Payload["needs_rebuild"] != true || hits[0].Payload["generation_id"] != "" ||
			hits[0].Payload["selection_revision"] != strconv.FormatInt(scope.SelectionRevision, 10) {
			t.Fatalf("new-scope fallback expanded selection: %+v %v", hits, err)
		}
		if content, _ := hits[0].Payload["content"].(string); strings.Contains(content, "restricted_") || strings.Contains(content, "KB description") {
			t.Fatalf("fallback leaked removed/stored prose: %q", content)
		}
		registry := pending
		registry.Revision++
		registry.Scope, registry.Registry = scope, snapshot.Registry
		if err := otherState.CompareAndSwap(f.ctx, scope, pending.Revision, registry); err != nil {
			t.Fatalf("new selection registry CAS: %v", err)
		}
		if err := narrowIndex.Build(f.ctx, snapshot, f.wire.Knowledge.KBRevision, narrowed.chunks(t)); err != nil {
			t.Fatalf("new selection real Build: %v", err)
		}
		winner = narrowed.ready(t, narrowIndex, registry.Revision+2)
	}
	late := f.index(t, wrapped)
	if err := late.Build(f.ctx, f.wire.Snapshot, f.wire.Knowledge.KBRevision, f.chunks(t)); !errors.Is(err, ErrObjectIndexConflict) {
		t.Fatalf("old task must lose publication: %v", err)
	}
	if !hookCalled || narrowed == nil {
		t.Fatal("publication race hook never ran")
	}
	objectRuntimeEqual(t, "late Build/failBuild did not clobber winner", narrowed.read(t), winner)
	// Exercise the actual old-scope Mongo CAS predicate too, even though the
	// production builder may reject at its preceding authoritative re-read.
	forbidden := pending
	forbidden.Revision++
	forbidden.Manifest.Status = "ready"
	forbidden.Manifest.ActiveGeneration = pending.Manifest.BuildingGeneration
	forbidden.Manifest.BuildingGeneration = ""
	if err := f.state.CompareAndSwap(f.ctx, f.wire.Snapshot.Scope, pending.Revision, forbidden); !errors.Is(err, repositories.ErrObjectConflict) {
		t.Fatalf("old selection CAS accepted: %v", err)
	}
	if _, err := f.state.Read(f.ctx, f.wire.Snapshot.Scope); !errors.Is(err, repositories.ErrObjectConflict) {
		t.Fatalf("old selection state read accepted: %v", err)
	}
	objectRuntimeEqual(t, "rejected old CAS leaves new state unchanged", narrowed.read(t), winner)
	// Old active is retired, while the superseded candidate remains an invisible
	// real orphan; it cannot contribute its removed view to new-scope Search.
	f.points(t, old.Manifest.ActiveGeneration, nil)
	f.points(t, pending.Manifest.BuildingGeneration, f.chunks(t))
	narrowed.search(t, narrowed.index(t, narrowed.store), winner.Manifest.ActiveGeneration)

	currentChat, err := otherChat.ReadCurrent(f.ctx, f.wire.Knowledge.UserID, f.wire.Knowledge.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	objectRuntimeEqual(t, "late task preserves winner selection revision", currentChat.SelectionRevision, narrowed.wire.Snapshot.Scope.SelectionRevision)
	objectRuntimeEqual(t, "late task preserves selected object list", currentChat.SelectedObjects,
		&schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{
			{Schema: "reports", Name: "sales_view", Kind: schemaobjects.View},
		}})
	objectRuntimeEqual(t, "late task preserves current chat state", currentChat.ObjectState, &winner)
	narrowed.runChild(t, "read", winner, 0)
	narrowed.knowledge(t)
}
