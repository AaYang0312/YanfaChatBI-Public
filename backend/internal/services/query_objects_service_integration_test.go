//go:build object_service_integration

package services

// Run only against the manager-provisioned synthetic stack. No fixture SQL is
// installed here, no production grants are changed, and no external model is used.
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/mongodb"
	redisrepo "yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"

	"github.com/lib/pq"
	goredis "github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Observe, then delegate to the genuine PostgreSQL driver. These counters are
// business-execution entry counts, NOT catalog SQL statement counts. Catalog
// collection uses the real DBExecutor/session directly and is not mocked.
type objectServiceRuntimeDriver struct {
	dbmanager.DatabaseDriver
	execute, begin, samples atomic.Int64
}

func (d *objectServiceRuntimeDriver) ExecuteQuery(ctx context.Context, c *dbmanager.Connection, q, kind string, count bool) *dbmanager.QueryExecutionResult {
	d.execute.Add(1)
	return d.DatabaseDriver.ExecuteQuery(ctx, c, q, kind, count)
}
func (d *objectServiceRuntimeDriver) BeginTx(ctx context.Context, c *dbmanager.Connection) dbmanager.Transaction {
	d.begin.Add(1)
	return d.DatabaseDriver.BeginTx(ctx, c)
}
func (d *objectServiceRuntimeDriver) FetchExampleRecords(ctx context.Context, db dbmanager.DBExecutor, table string, limit int) ([]map[string]interface{}, error) {
	d.samples.Add(1)
	return d.DatabaseDriver.FetchExampleRecords(ctx, db, table, limit)
}

type objectServiceRuntimeRequest struct {
	Messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}
type objectServiceRuntimeModel struct {
	mu       sync.Mutex
	requests []objectServiceRuntimeRequest
	batches  [][]llm.ToolCall
	before   func(int) error
	faults   []string
}

func (m *objectServiceRuntimeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		m.faults = append(m.faults, "unexpected model endpoint: "+r.Method+" "+r.URL.Path)
		http.Error(w, "unexpected model endpoint", 400)
		return
	}
	var request objectServiceRuntimeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
		m.faults = append(m.faults, err.Error())
		http.Error(w, "bad model request", 400)
		return
	}
	index := len(m.requests)
	m.requests = append(m.requests, request)
	if index >= len(m.batches) {
		m.faults = append(m.faults, "unexpected extra SDK request")
		http.Error(w, "script exhausted", 400)
		return
	}
	if m.before != nil {
		if err := m.before(index); err != nil {
			m.faults = append(m.faults, err.Error())
			http.Error(w, "fixture operation failed", 400)
			return
		}
	}
	calls := []interface{}{}
	for _, call := range m.batches[index] {
		arguments, err := json.Marshal(call.Arguments)
		if err != nil {
			m.faults = append(m.faults, err.Error())
			http.Error(w, "bad script", 400)
			return
		}
		calls = append(calls, map[string]interface{}{"id": call.ID, "type": "function", "function": map[string]interface{}{"name": call.Name, "arguments": string(arguments)}})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": fmt.Sprintf("object-runtime-%d", index), "object": "chat.completion", "choices": []interface{}{map[string]interface{}{"index": 0, "message": map[string]interface{}{"role": "assistant", "tool_calls": calls}, "finish_reason": "tool_calls"}}})
}
func (m *objectServiceRuntimeModel) snapshot() ([]objectServiceRuntimeRequest, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]objectServiceRuntimeRequest(nil), m.requests...), append([]string(nil), m.faults...)
}

type objectServiceRuntimeStream struct{ events chan dtos.StreamResponse }

func (s *objectServiceRuntimeStream) HasStream(string, string, string) bool { return true }
func (s *objectServiceRuntimeStream) HandleStreamEvent(_, _, _ string, event dtos.StreamResponse) {
	if event.Event == "ai-response" || event.Event == "ai-response-error" || event.Event == "system-message" {
		select {
		case s.events <- event:
		default:
		}
	}
}

type objectServiceRuntimeFixture struct {
	service    *chatService
	chat       *models.Chat
	chats      repositories.ChatRepository
	kb         repositories.KnowledgeBaseRepository
	database   *mongo.Database
	driver     *objectServiceRuntimeDriver
	model      *objectServiceRuntimeModel
	stream     *objectServiceRuntimeStream
	reader     *sql.DB
	readerName string
	adminURL   string
}

func objectServiceRuntimeEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("object_service_integration requires explicit isolated %s (not a skip)", name)
	}
	return value
}
func objectServiceRuntimePGURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.User == nil || parsed.User.Username() == "" || parsed.Hostname() == "" || (parsed.Scheme != "postgresql" && parsed.Scheme != "postgres") || parsed.Path != "/query_objects_test" {
		t.Fatal("PG URL must explicitly identify a reader/owner in synthetic query_objects_test")
	}
	// The dedicated synthetic PG fixture has no TLS. Honor an explicit mode;
	// match ChatService's default (UseSSL=false) when the supplied URL omits it.
	query := parsed.Query()
	if query.Get("sslmode") == "" {
		query.Set("sslmode", "disable")
	}
	parsed.RawQuery = query.Encode()
	return parsed
}
func newObjectServiceRuntime(t *testing.T, ref schemaobjects.Ref) *objectServiceRuntimeFixture {
	t.Helper()
	readerURL := objectServiceRuntimeEnv(t, "QUERY_OBJECTS_TEST_PG_READER_URL")
	adminURL := objectServiceRuntimeEnv(t, "QUERY_OBJECTS_TEST_PG_ADMIN_URL")
	mongoURI := objectServiceRuntimeEnv(t, "ASSISTANT_TEST_MONGO_URI")
	redisAddress := objectServiceRuntimeEnv(t, "SOURCE_ACCESS_TEST_REDIS_ADDR")
	qdrantAddress := objectServiceRuntimeEnv(t, "SOURCE_ACCESS_TEST_QDRANT_ADDR")
	parsed, admin := objectServiceRuntimePGURL(t, readerURL), objectServiceRuntimePGURL(t, adminURL)
	readerURL, adminURL = parsed.String(), admin.String()
	if parsed.User.Username() == admin.User.Username() || parsed.Host != admin.Host {
		t.Fatal("reader and owner must be distinct roles on the same synthetic PG endpoint")
	}
	previousFlag, previousKey, previousDataKey := config.Env.QueryObjectsEnabled, config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey
	config.Env.QueryObjectsEnabled, config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey = true, "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"
	t.Cleanup(func() {
		config.Env.QueryObjectsEnabled, config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey = previousFlag, previousKey, previousDataKey
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := &objectServiceRuntimeFixture{model: &objectServiceRuntimeModel{}, stream: &objectServiceRuntimeStream{events: make(chan dtos.StreamResponse, 8)}, adminURL: adminURL}
	var err error
	f.reader, err = sql.Open("postgres", readerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.reader.Close() })
	var super, bypass, owner, baseUsage, baseSelect, baseColumns, viewSelect bool
	err = f.reader.QueryRowContext(ctx, `SELECT current_user, r.rolsuper, r.rolbypassrls,
 v.relowner=r.oid,
 has_schema_privilege(current_user,n.oid,'USAGE'),
 has_table_privilege(current_user,b.oid,'SELECT'),
 has_any_column_privilege(current_user,b.oid,'SELECT'),
 has_table_privilege(current_user,v.oid,'SELECT')
 FROM pg_catalog.pg_roles r
 JOIN pg_catalog.pg_namespace n ON n.nspname='private_base'
 JOIN pg_catalog.pg_class b ON b.relnamespace=n.oid AND b.relname='sales'
 JOIN pg_catalog.pg_namespace vn ON vn.nspname='reporting'
 JOIN pg_catalog.pg_class v ON v.relnamespace=vn.oid AND v.relname='sales'
 WHERE r.rolname=current_user`).Scan(&f.readerName, &super, &bypass, &owner, &baseUsage, &baseSelect, &baseColumns, &viewSelect)
	if err != nil {
		t.Fatal(err)
	}
	if f.readerName != parsed.User.Username() || super || bypass || owner || baseUsage || baseSelect || baseColumns || !viewSelect {
		t.Fatalf("unsafe reader fixture: super=%t bypass=%t viewOwner=%t baseUsage=%t baseSelect=%t baseColumns=%t viewSelect=%t", super, bypass, owner, baseUsage, baseSelect, baseColumns, viewSelect)
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI).SetServerSelectionTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Disconnect(ctx)
	})
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	f.database = client.Database("query_objects_service_test_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := f.database.Drop(ctx); err != nil {
			t.Errorf("own Mongo cleanup: %v", err)
		}
	})
	redisClient := goredis.NewClient(&goredis.Options{Addr: redisAddress})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	redis := redisrepo.NewRedisRepositories(redisClient)
	// Connectivity only: this test covers authoritative Mongo KB/catalog fallback,
	// not vector search. Do not create global collections or call real embeddings.
	host, port, err := net.SplitHostPort(qdrantAddress)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := vectordb.NewQdrantClient(vectordb.QdrantConfig{Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vectors.Close() })
	if !vectors.IsHealthy(ctx) {
		t.Fatal("explicit isolated Qdrant endpoint is unhealthy")
	}
	wrapper := &mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}
	f.chats, f.kb = repositories.NewChatRepository(wrapper, redis), repositories.NewKnowledgeBaseRepository(wrapper)
	password, _ := parsed.User.Password()
	pgPort := parsed.Port()
	if pgPort == "" {
		pgPort = "5432"
	}
	username := parsed.User.Username()
	connection := models.Connection{Type: constants.DatabaseTypePostgreSQL, Host: parsed.Hostname(), Port: &pgPort, Username: &username, Password: &password, Database: "query_objects_test"}
	f.chat = models.NewChat(primitive.NewObjectID(), connection, models.ChatSettings{AutoExecuteQuery: false, ShareDataWithAI: false})
	f.chat.SelectedObjects = &schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{ref}}
	f.chat.SelectedCollections, f.chat.SelectionRevision = "__QUERY_OBJECTS_V2__", 1
	stale := "STALE_SCHEMA_SECRET private_base.sales secret old_removed_column"
	f.chat.Connection.CurrentSchema = &stale
	if err := utils.EncryptConnection(&f.chat.Connection); err != nil {
		t.Fatal(err)
	}
	if err := f.chats.Create(f.chat); err != nil {
		t.Fatal(err)
	}
	manager, err := dbmanager.NewManager(redis, config.Env.SchemaEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	f.driver = &objectServiceRuntimeDriver{DatabaseDriver: dbmanager.NewPostgresDriver()}
	manager.RegisterDriver(constants.DatabaseTypePostgreSQL, f.driver)
	manager.RegisterFetcher(constants.DatabaseTypePostgreSQL, func(dbmanager.DBExecutor) dbmanager.SchemaFetcher { return dbmanager.NewPostgresDriver() })
	server := httptest.NewServer(f.model)
	t.Cleanup(server.Close)
	modelConfig := llm.Config{Provider: constants.OpenAI, APIKey: "synthetic-local-only", BaseURL: server.URL + "/v1", Model: "gpt-4o"}
	modelManager := llm.NewManager()
	if err := modelManager.RegisterClient(constants.OpenAI, modelConfig); err != nil {
		t.Fatal(err)
	}
	modelClient, err := modelManager.GetClient(constants.OpenAI)
	if err != nil {
		t.Fatal(err)
	}
	policy := NewUnconfiguredSourceAccessService(f.chats)
	// This is an independent synthetic source; no ERP registration or extra grants.
	f.service = NewChatService(f.chats, manager, modelClient, modelManager, redis, repositories.NewVisualizationRepository(wrapper, redis), nil, f.kb, repositories.NewDashboardRepository(wrapper, redis), nil, policy).(*chatService)
	f.service.SetStreamHandler(f.stream)
	manager.SetStreamHandler(f.service)
	t.Cleanup(func() {
		f.service.CancelProcessing(f.chat.UserID.Hex(), f.chat.ID.Hex(), "object-runtime")
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			f.service.processesMu.Lock()
			active := len(f.service.activeProcesses)
			f.service.processesMu.Unlock()
			if active == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("generation goroutine did not stop before fixture cleanup")
	})
	return f
}
func (f *objectServiceRuntimeFixture) saveKB(t *testing.T, ref schemaobjects.Ref) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	directory, status, err := f.service.GetAllTables(ctx, f.chat.UserID.Hex(), f.chat.ID.Hex())
	if err != nil || status != 200 || directory == nil || !directory.QueryObjectsEnabled {
		t.Fatalf("real directory: status=%d error=%v", status, err)
	}
	selectedID := ""
	selectedCount := 0
	for _, item := range directory.Tables {
		if item.IsSelected {
			selectedCount++
			if item.SchemaName != ref.Schema || item.ObjectName != ref.Name || item.ObjectType != schemaobjects.View {
				t.Fatalf("unexpected selected object: %+v", item)
			}
			if len(item.Columns) != 2 || item.Columns[0].Name != "id" || item.Columns[1].Name != "amount" {
				t.Fatalf("view columns must be current id,amount: %+v", item.Columns)
			}
			selectedID = item.ObjectID
		}
	}
	if selectedCount != 1 || selectedID == "" {
		t.Fatalf("view-only directory selected=%d id=%q", selectedCount, selectedID)
	}
	kb, status, err := f.service.GetKnowledgeBaseView(ctx, f.chat.UserID.Hex(), f.chat.ID.Hex())
	if err != nil || status != 200 || kb == nil || kb.KBRevision == "" {
		t.Fatalf("real KB revision: status=%d error=%v", status, err)
	}
	description := "synthetic sales view KB marker"
	saved, status, err := f.service.UpdateKnowledgeBaseWithRevision(ctx, f.chat.UserID.Hex(), f.chat.ID.Hex(), dtos.UpdateKnowledgeBaseRequest{
		ExpectedKBRevision: kb.KBRevision, TableDescriptions: []dtos.KnowledgeBaseDescriptionEdit{{ObjectID: selectedID, Description: &description, FieldDescriptions: []dtos.KnowledgeBaseFieldEdit{{FieldName: "amount", Description: "synthetic amount KB marker"}}}},
	})
	if err != nil || status != 200 || saved == nil || !saved.Saved || len(saved.EffectiveDescriptions) != 1 {
		t.Fatalf("real KB save: status=%d response=%+v error=%v", status, saved, err)
	}
	persisted, err := f.kb.FindByChatID(ctx, f.chat.ID)
	if err != nil || persisted == nil || persisted.UserID != f.chat.UserID || persisted.KnowledgeVersion != 2 || len(persisted.TableDescriptions) != 1 || persisted.TableDescriptions[0].ObjectID != selectedID || persisted.TableDescriptions[0].Description != description {
		t.Fatalf("Mongo v2 KB did not preserve scoped edit: %+v error=%v", persisted, err)
	}
	var stored models.Chat
	if err := f.database.Collection("chats").FindOne(ctx, bson.M{"_id": f.chat.ID}).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.SelectedObjects == nil || stored.SelectedObjects.Version != 2 || len(stored.SelectedObjects.Objects) != 1 || stored.SelectedObjects.Objects[0] != ref {
		t.Fatalf("Mongo view-only selection lost: %+v", stored.SelectedObjects)
	}
}
func objectServiceRuntimeFinal(sqlText string) llm.ToolCall {
	return llm.ToolCall{ID: "final", Name: llm.FinalResponseToolName, Arguments: map[string]interface{}{
		"assistantMessage": "Synthetic SQL generated; execution not verified.", "queries": []interface{}{map[string]interface{}{"query": sqlText, "queryType": "SELECT", "isCritical": false, "explanation": "SQL for explicit user execution", "isExecuted": true, "executionResult": "MODEL_FORGED_EXECUTION_RESULT", "exampleResult": []interface{}{map[string]interface{}{"marker": "MODEL_FORGED_EXAMPLE_RESULT"}}}}, "actionButtons": []interface{}{},
	}}
}
func (f *objectServiceRuntimeFixture) run(t *testing.T, sqlText string) {
	t.Helper()
	// Join the real async generation before any per-test schema/database cleanup.
	defer func() {
		f.service.CancelProcessing(f.chat.UserID.Hex(), f.chat.ID.Hex(), "object-runtime")
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			f.service.processesMu.Lock()
			active := len(f.service.activeProcesses)
			f.service.processesMu.Unlock()
			if active == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("generation did not finish before returning to schema cleanup")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	accepted, status, err := f.service.CreateMessage(ctx, f.chat.UserID.Hex(), f.chat.ID.Hex(), "object-runtime", "Describe the selected sales view and generate SQL only.", "gpt-4o")
	if err != nil || status != 200 || accepted == nil {
		t.Fatalf("CreateMessage: status=%d error=%v", status, err)
	}
	var event dtos.StreamResponse
	select {
	case event = <-f.stream.events:
	case <-ctx.Done():
		t.Fatal("no terminal from real CreateMessage/SDK pipeline")
	}
	if event.Event != "ai-response" {
		t.Fatalf("generation terminal=%s data=%+v", event.Event, event.Data)
	}
	response, ok := event.Data.(*dtos.MessageResponse)
	if !ok || response.UserMessageID == nil || *response.UserMessageID != accepted.ID || response.ChatID != f.chat.ID.Hex() || response.Queries == nil || len(*response.Queries) != 1 {
		t.Fatalf("real stream response lost message/SQL: %+v", event.Data)
	}
	result := (*response.Queries)[0]
	if result.Query != sqlText || result.IsExecuted || result.ExecutionResult != nil {
		t.Fatalf("stream SQL was executed/fabricated: %+v", result)
	}
	if len(result.ExampleResult) != 0 {
		t.Errorf("strict stream exposed model-forged example rows: %+v", result.ExampleResult)
	}
	id, err := primitive.ObjectIDFromHex(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.chats.FindMessageByID(id)
	if err != nil || stored == nil || stored.UserID != f.chat.UserID || stored.ChatID != f.chat.ID || stored.UserMessageId == nil || stored.UserMessageId.Hex() != accepted.ID || stored.Queries == nil || len(*stored.Queries) != 1 {
		t.Fatalf("real Mongo assistant message: %+v error=%v", stored, err)
	}
	query := (*stored.Queries)[0]
	if query.Query != sqlText || query.IsExecuted || query.ExecutionResult != nil || query.Error != nil {
		t.Fatalf("persisted SQL was executed/fabricated: %+v", query)
	}
	if query.ExampleResult != nil {
		t.Error("strict Mongo query persisted model-forged ExampleResult; want nil")
	}
	if f.driver.execute.Load() != 0 || f.driver.begin.Load() != 0 || f.driver.samples.Load() != 0 {
		t.Fatalf("strict generation reached real SQL driver: execute=%d begin=%d samples=%d", f.driver.execute.Load(), f.driver.begin.Load(), f.driver.samples.Load())
	}
	requests, faults := f.model.snapshot()
	if len(faults) != 0 {
		t.Fatalf("local model faults: %v", faults)
	}
	if len(requests) != len(f.model.batches) {
		t.Fatalf("SDK requests=%d want=%d", len(requests), len(f.model.batches))
	}
	toolResults := map[string]string{}
	for i, request := range requests {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("SDK request[%d]: %s", i, encoded)
		names := map[string]bool{}
		for _, tool := range request.Tools {
			names[tool.Function.Name] = true
		}
		if len(names) != 2 || !names[llm.GetTableInfoToolName] || !names[llm.FinalResponseToolName] {
			t.Fatalf("request %d strict tool allowlist=%v", i, names)
		}
		for _, message := range request.Messages {
			if message.Role == "system" {
				for _, forbidden := range []string{"STALE_SCHEMA_SECRET", "private_base", "old_removed_column", "12.50"} {
					if strings.Contains(string(message.Content), forbidden) {
						t.Fatalf("system schema leaked %q", forbidden)
					}
				}
			}
			if message.Role == "tool" {
				var content string
				if err := json.Unmarshal(message.Content, &content); err != nil {
					t.Fatal(err)
				}
				toolResults[message.ToolCallID] = content
			}
		}
	}
	t.Logf("SDK_HTTP_requests=%d distinct_tool_results=%d driver_execute=%d driver_begin=%d driver_samples=%d; catalog SQL is real and excluded from business counters", len(requests), len(toolResults), f.driver.execute.Load(), f.driver.begin.Load(), f.driver.samples.Load())
	for id, content := range toolResults {
		t.Logf("SDK tool result %s: %s", id, content)
	}
}
func objectServiceRuntimeResults(t *testing.T, f *objectServiceRuntimeFixture) map[string]string {
	t.Helper()
	requests, _ := f.model.snapshot()
	results := map[string]string{}
	for _, request := range requests {
		for _, message := range request.Messages {
			if message.Role == "tool" {
				var content string
				if err := json.Unmarshal(message.Content, &content); err != nil {
					t.Fatal(err)
				}
				results[message.ToolCallID] = content
			}
		}
	}
	return results
}
func objectServiceRuntimeView(t *testing.T, text, identifier string) {
	t.Helper()
	for _, want := range []string{"View " + identifier, `"id" integer`, `"amount" numeric(18,2)`, "synthetic sales view KB marker", "synthetic amount KB marker", "Execution is not verified"} {
		if !strings.Contains(text, want) {
			t.Errorf("tool result missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"private_base", "secret", "old_removed_column", "12.50", "CREATE VIEW", "SELECT id", "empty_view"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("tool metadata leaked %q: %s", forbidden, text)
		}
	}
}

// Catches regressions in admission, Mongo selection/KB projection, actual tool
// routing, legacy arguments, tool allowlisting, and final SQL persistence.
func TestQueryObjectsServiceIntegrationViewOnlyToolsAndGenerateOnly(t *testing.T) {
	ref := schemaobjects.Ref{Schema: "reporting", Name: "sales", Kind: schemaobjects.View}
	f := newObjectServiceRuntime(t, ref)
	f.saveKB(t, ref)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	outsider := primitive.NewObjectID().Hex()
	for _, pair := range [][2]string{{outsider, f.chat.ID.Hex()}, {f.chat.UserID.Hex(), primitive.NewObjectID().Hex()}} {
		result, status, err := f.service.CreateMessage(ctx, pair[0], pair[1], "denied", "unauthorized", "gpt-4o")
		if err == nil || result != nil || (status != 403 && status != 404) {
			t.Fatalf("owner/chat admission not denied: %d %v", status, err)
		}
	}
	if text, err := f.service.QueryObjectStructure(ctx, outsider, f.chat.ID.Hex(), map[string]interface{}{"object_refs": []schemaobjects.Ref{ref}}); err == nil || text != "" {
		t.Fatalf("wrong owner got structure: %q error=%v", text, err)
	}
	if kb, status, err := f.service.GetKnowledgeBaseView(ctx, outsider, f.chat.ID.Hex()); err == nil || kb != nil || (status != 403 && status != 404) {
		t.Fatalf("wrong owner got KB: %d %v", status, err)
	}
	if count, err := f.database.Collection("messages").CountDocuments(ctx, bson.M{}); err != nil || count != 0 {
		t.Fatalf("denied request persisted message: count=%d err=%v", count, err)
	}
	if requests, _ := f.model.snapshot(); len(requests) != 0 {
		t.Fatal("denied request reached SDK")
	}
	sqlText := `SELECT id, amount FROM "reporting"."sales" ORDER BY id`
	f.model.batches = [][]llm.ToolCall{{
		{ID: "selected", Name: llm.GetTableInfoToolName, Arguments: map[string]interface{}{"object_refs": []schemaobjects.Ref{ref}}},
		{ID: "legacy", Name: llm.GetTableInfoToolName, Arguments: map[string]interface{}{"table_names": []string{`"reporting"."sales"`}}},
		{ID: "unselected", Name: llm.GetTableInfoToolName, Arguments: map[string]interface{}{"object_refs": []schemaobjects.Ref{{Schema: "reporting", Name: "empty_view", Kind: schemaobjects.View}}}},
		{ID: "malicious-real-name", Name: llm.ExecuteQueryToolName, Arguments: map[string]interface{}{"query": sqlText}},
		{ID: "malicious-execute-sql", Name: "execute_sql", Arguments: map[string]interface{}{"query": sqlText}},
	}, {objectServiceRuntimeFinal(sqlText)}}
	f.run(t, sqlText)
	results := objectServiceRuntimeResults(t, f)
	if len(results) != 5 {
		t.Fatalf("actual SDK tool results=%d want=5", len(results))
	}
	objectServiceRuntimeView(t, results["selected"], `"reporting"."sales"`)
	objectServiceRuntimeView(t, results["legacy"], `"reporting"."sales"`)
	denied := results["unselected"]
	if !strings.Contains(strings.ToLower(denied), "unavailable") || strings.Contains(denied, `"id"`) || strings.Contains(denied, "empty_view") {
		t.Fatalf("unselected object was disclosed: %s", denied)
	}
	for _, id := range []string{"malicious-real-name", "malicious-execute-sql"} {
		if !strings.Contains(results[id], "GENERATION_ONLY") || strings.Contains(results[id], "results") {
			t.Fatalf("malicious execution was not refused: %s", results[id])
		}
	}
}

// Catches stale-column/KB reuse after a real REVOKE between two SDK rounds.
// Only a uniquely owned schema/view is created and removed; no base-table grants.
func TestQueryObjectsServiceIntegrationViewRevokeDuringToolLoop(t *testing.T) {
	schema := "object_service_" + primitive.NewObjectID().Hex()
	ref := schemaobjects.Ref{Schema: schema, Name: "sales", Kind: schemaobjects.View}
	f := newObjectServiceRuntime(t, ref)
	admin, err := sql.Open("postgres", f.adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var current, database string
	if err := admin.QueryRowContext(ctx, "SELECT current_user,current_database()").Scan(&current, &database); err != nil {
		t.Fatal(err)
	}
	if current == f.readerName || database != "query_objects_test" {
		t.Fatal("invalid synthetic owner connection")
	}
	quotedSchema, reader := pq.QuoteIdentifier(schema), pq.QuoteIdentifier(f.readerName)
	identifier := quotedSchema + `."sales"`
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("own schema cleanup: %v", err)
		}
	})
	for _, statement := range []string{
		"CREATE VIEW " + identifier + ` AS SELECT id, amount FROM reporting.sales`,
		"GRANT USAGE ON SCHEMA " + quotedSchema + " TO " + reader,
		"GRANT SELECT ON " + identifier + " TO " + reader,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	f.saveKB(t, ref)
	sqlText := "SELECT id, amount FROM " + identifier + " ORDER BY id"
	f.model.batches = [][]llm.ToolCall{
		{{ID: "before-revoke", Name: llm.GetTableInfoToolName, Arguments: map[string]interface{}{"object_refs": []schemaobjects.Ref{ref}}}},
		{{ID: "after-revoke", Name: llm.GetTableInfoToolName, Arguments: map[string]interface{}{"object_refs": []schemaobjects.Ref{ref}}}},
		{objectServiceRuntimeFinal(sqlText)},
	}
	f.model.before = func(round int) error {
		if round != 1 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "REVOKE SELECT ON "+identifier+" FROM "+reader); err != nil {
			return err
		}
		var readable bool
		if err := f.reader.QueryRowContext(ctx, "SELECT has_table_privilege(current_user,$1,'SELECT')", identifier).Scan(&readable); err != nil {
			return err
		}
		if readable {
			return fmt.Errorf("synthetic view is still readable after revoke")
		}
		return nil
	}
	f.run(t, sqlText)
	results := objectServiceRuntimeResults(t, f)
	if len(results) != 2 {
		t.Fatalf("actual SDK tool results=%d want=2", len(results))
	}
	objectServiceRuntimeView(t, results["before-revoke"], identifier)
	denied := results["after-revoke"]
	if !strings.Contains(strings.ToLower(denied), "unavailable") {
		t.Fatalf("revoked tool did not fail closed: %s", denied)
	}
	for _, forbidden := range []string{`"id"`, `"amount"`, "KB marker", identifier, "private_base"} {
		if strings.Contains(denied, forbidden) {
			t.Fatalf("post-revoke tool leaked %q: %s", forbidden, denied)
		}
	}
}

// The strict-mode fix must not remove the existing preview contract from a
// server-authorized execution-enabled generation. Invoke response generation
// directly so this assertion does not request the separate auto-run phase.
func TestQueryObjectsServiceIntegrationExecutionEnabledPreview(t *testing.T) {
	f := newObjectServiceRuntime(t, schemaobjects.Ref{Schema: "reporting", Name: "sales", Kind: schemaobjects.View})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	enabled := true
	if err := f.chats.PatchMetadata(ctx, f.chat.UserID, f.chat.ID, repositories.ChatMetadataPatch{AutoExecuteQuery: &enabled}); err != nil {
		t.Fatal(err)
	}
	message := models.NewMessage(f.chat.UserID, f.chat.ID, "user", "Generate a sales query with an illustrative preview.", nil, nil)
	model := "gpt-4o"
	message.LLMModel = &model
	if err := f.chats.CreateMessage(message); err != nil {
		t.Fatal(err)
	}
	sqlText := `SELECT id, amount FROM "reporting"."sales" ORDER BY id`
	f.model.batches = [][]llm.ToolCall{{objectServiceRuntimeFinal(sqlText)}}
	response, err := f.service.processLLMResponse(ctx, f.chat.UserID.Hex(), f.chat.ID.Hex(), message.ID.Hex(), "object-runtime", true, false)
	if err != nil || response == nil || response.Queries == nil || len(*response.Queries) != 1 {
		t.Fatalf("execution-enabled response: %+v error=%v", response, err)
	}
	query := (*response.Queries)[0]
	if query.Query != sqlText || query.IsExecuted || query.ExecutionResult != nil || len(query.ExampleResult) != 1 || fmt.Sprint(query.ExampleResult[0]) != "map[marker:MODEL_FORGED_EXAMPLE_RESULT]" {
		t.Fatalf("execution-enabled preview contract changed: %+v", query)
	}
	id, err := primitive.ObjectIDFromHex(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.chats.FindMessageByID(id)
	if err != nil || stored == nil || stored.Queries == nil || len(*stored.Queries) != 1 {
		t.Fatalf("stored response=%+v error=%v", stored, err)
	}
	saved := (*stored.Queries)[0]
	if saved.ExampleResult == nil || strings.Contains(*saved.ExampleResult, "MODEL_FORGED_EXAMPLE_RESULT") || saved.IsExecuted || saved.ExecutionResult != nil {
		t.Fatal("normal preview was lost, stored unencrypted, or promoted to actual results")
	}
	requests, faults := f.model.snapshot()
	if len(requests) != 1 || len(faults) != 0 {
		t.Fatalf("SDK requests=%d faults=%v", len(requests), faults)
	}
	executionTool := false
	for _, tool := range requests[0].Tools {
		if tool.Function.Name == llm.ExecuteQueryToolName {
			executionTool = true
		}
	}
	if !executionTool {
		t.Fatal("authorized execution tool was removed")
	}
	if f.driver.execute.Load() != 0 || f.driver.begin.Load() != 0 || f.driver.samples.Load() != 0 {
		t.Fatal("response-only call unexpectedly executed data SQL")
	}
}
