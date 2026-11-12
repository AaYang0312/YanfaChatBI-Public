package services_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
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
	"yanfachatbi/internal/apis/handlers"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/embedding"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/mongodb"
	redisrepo "yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/sourcegate"
	"yanfachatbi/pkg/vectordb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// These are real service/repository/handler/PG tests. The loopback HTTP endpoint
// supplies deterministic model responses only; no customer ERP or T19 mapping is
// asserted. JWT validation uses the real JWTService with a fixture-only key.
// The application's DI/auth middleware is exercised separately by manager smoke.
type sourceGateAPI struct {
	chats             repositories.ChatRepository
	registry          repositories.ERPSourceRepository
	policy            *services.SourceAccessService
	manager           *dbmanager.Manager
	service           services.ChatService
	assistant         services.AssistantService
	dashboards        repositories.DashboardRepository
	mongo             *mongo.Client
	database          *mongo.Database
	redis             redisrepo.IRedisRepositories
	gate              *sourcegate.Gate
	source            *models.ERPSource
	chat              *models.Chat
	connection        models.Connection
	owner             primitive.ObjectID
	message           *models.Message
	userMessage       *models.Message
	query             models.Query
	dashboard         *models.Dashboard
	widget            *models.Widget
	control           *sql.DB
	table             string
	server            *httptest.Server
	streamHandler     *handlers.ChatHandler
	model             *httptest.Server
	modelCalls        atomic.Int64
	modelUseSQL       atomic.Bool
	modelFinalQueries atomic.Bool
	modelSchemaReply  atomic.Bool
	modelEntered      chan struct{}
	modelRelease      chan struct{}
	releaseOnce       sync.Once
	wire              atomic.Int64
	jwt               utils.JWTService
	modelClient       llm.Client
	modelManager      *llm.Manager
	mongoURI          string
}

type sourceGateWireReader struct {
	net.Conn
	bytes *atomic.Int64
}

func (r sourceGateWireReader) Read(data []byte) (int, error) {
	n, err := r.Conn.Read(data)
	r.bytes.Add(int64(n))
	return n, err
}

func newSourceGateAPI(t *testing.T, holdModel bool) *sourceGateAPI {
	return newSourceGateAPIWithPilot(t, holdModel, false)
}

func newSourceGateAPIWithPilot(t *testing.T, holdModel, pilot bool) *sourceGateAPI {
	t.Helper()
	mongoURI, pgURL, redisAddress := os.Getenv("ASSISTANT_TEST_MONGO_URI"), os.Getenv("SOURCE_ACCESS_TEST_PG_URL"), os.Getenv("SOURCE_ACCESS_TEST_REDIS_ADDR")
	if mongoURI == "" || pgURL == "" || redisAddress == "" {
		t.Skip("UNVERIFIED: SourceGateIntegration requires explicit isolated Mongo, PG bi_agent and Redis inputs")
	}
	parsed, err := url.Parse(pgURL)
	if err != nil || parsed.User == nil || strings.TrimPrefix(parsed.Path, "/") != "bi_agent" {
		t.Fatal("SOURCE_ACCESS_TEST_PG_URL must name the isolated bi_agent fixture")
	}
	f := &sourceGateAPI{owner: primitive.NewObjectID(), modelEntered: make(chan struct{}, 8), modelRelease: make(chan struct{}), mongoURI: mongoURI}
	if !holdModel {
		f.releaseOnce.Do(func() { close(f.modelRelease) })
	}
	t.Cleanup(func() { f.releaseOnce.Do(func() { close(f.modelRelease) }) })
	oldSchema, oldData, oldMax := config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey, config.Env.MaxChatsPerUser
	config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey, config.Env.MaxChatsPerUser = "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef", 100
	t.Cleanup(func() {
		config.Env.SchemaEncryptionKey, config.Env.SpreadsheetDataEncryptionKey, config.Env.MaxChatsPerUser = oldSchema, oldData, oldMax
	})
	ctx := context.Background()
	f.mongo, err = mongo.Connect(ctx, options.Client().ApplyURI(mongoURI).SetServerSelectionTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.mongo.Disconnect(context.Background()) })
	if err := f.mongo.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	f.database = f.mongo.Database("source_gate_test_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() { f.database.Drop(context.Background()) })
	redisClient := goredis.NewClient(&goredis.Options{Addr: redisAddress})
	t.Cleanup(func() { redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	f.redis = redisrepo.NewRedisRepositories(redisClient)
	mongoWrapper := &mongodb.MongoDBClient{Client: f.mongo, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}
	f.chats = repositories.NewChatRepository(mongoWrapper, f.redis)
	f.dashboards = repositories.NewDashboardRepository(mongoWrapper, f.redis)
	f.control, err = sql.Open("postgres", pgURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.control.Close() })
	f.table = "source_gate_" + primitive.NewObjectID().Hex()
	if _, err := f.control.ExecContext(ctx, `CREATE TABLE public."`+f.table+`" (id integer PRIMARY KEY, label text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.control.ExecContext(context.Background(), `DROP TABLE public."`+f.table+`"`) })
	if _, err := f.control.ExecContext(ctx, `INSERT INTO public."`+f.table+`" VALUES (1, 'persisted source row')`); err != nil {
		t.Fatal(err)
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	destination := net.JoinHostPort(parsed.Hostname(), port)
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				upstream, err := net.Dial("tcp", destination)
				if err != nil {
					return
				}
				defer upstream.Close()
				done := make(chan struct{})
				go func() {
					io.Copy(upstream, sourceGateWireReader{Conn: client, bytes: &f.wire})
					upstream.Close()
					close(done)
				}()
				io.Copy(client, upstream)
				client.Close()
				<-done
			}()
		}
	}()
	host, proxyPort, _ := net.SplitHostPort(listener.Addr().String())
	user, password := parsed.User.Username(), ""
	if value, ok := parsed.User.Password(); ok {
		password = value
	}
	f.connection = models.Connection{Type: "postgresql", Host: host, Port: &proxyPort, Username: &user, Password: &password, Database: "bi_agent"}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	f.gate, err = sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.gate.Close() })
	f.registry = repositories.NewERPSourceRepository(f.database)
	if err := f.registry.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.ERPSyncConfig{InstanceID: "t38-isolated-api", DeploymentUID: uint32(os.Getuid()), LockDirectory: directory}
	initial, err := services.NewSourceAccessService(f.registry, f.gate, cfg, f.chats)
	if err != nil {
		t.Fatal(err)
	}
	source := models.ERPSource{EnvironmentRef: "isolated-only", PhysicalDatabase: "bi_agent",
		ApprovedEndpointAliases: []string{services.EndpointFingerprint(f.connection)}, ApprovedConnectionFingerprints: []string{services.ConnectionFingerprint(f.connection)},
		ReaderRoleRefs: []string{"isolated-reader"}, WriterRoleRef: "isolated-writer", ComponentManifestRef: "isolated-only",
		ApprovalRefs: map[string]string{"Q15-02": "isolated-only"}, ViewAccountIDs: []primitive.ObjectID{f.owner}}
	if pilot {
		source.ShopScope = []string{"S1", "S2", "S3", "S4", "S5", "S6", "S7"}
		source.OrderSourceByShop = make(map[string]string)
		for _, shop := range source.ShopScope {
			source.OrderSourceByShop[shop] = "isolated-orders"
		}
	}
	f.source, err = initial.RegisterApproved(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.registry.UpdateStateCAS(ctx, f.source.SourceID, 0, "", repositories.ERPSourceState{Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: cfg.InstanceID}); err != nil {
		t.Fatal(err)
	}
	f.source, err = f.registry.Get(ctx, f.source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Associations = []config.ERPAssociation{{SourceID: f.source.SourceID, EndpointFingerprints: f.source.ApprovedEndpointAliases, ApprovalRef: "isolated-only"}}
	f.policy, err = services.NewSourceAccessService(f.registry, f.gate, cfg, f.chats)
	if err != nil {
		t.Fatal(err)
	}
	f.chat = models.NewChat(f.owner, f.connection.Clone(), models.ChatSettings{AutoExecuteQuery: false, ShareDataWithAI: false})
	f.chat.ERPAssociated, f.chat.SourceBindingConfirmed, f.chat.SourceID, f.chat.SourceBindingRevision = true, true, f.source.SourceID, 1
	f.chat.SourceConnectionFingerprint = services.ConnectionFingerprint(f.connection)
	f.chat.SelectedCollections = f.table
	binding := models.ERPChatBinding{ChatID: f.chat.ID, OwnerID: f.owner, ConnectionFingerprint: f.chat.SourceConnectionFingerprint, BindingRevision: 1, OperationID: uuid.NewString()}
	if err := f.registry.PutBindingCAS(ctx, f.source.SourceID, f.source.ConfigRevision, nil, binding); err != nil {
		t.Fatal(err)
	}
	if err := f.registry.ConfirmBindingCAS(ctx, f.source.SourceID, f.source.ConfigRevision, binding); err != nil {
		t.Fatal(err)
	}
	if err := utils.EncryptConnection(&f.chat.Connection); err != nil {
		t.Fatal(err)
	}
	if err := f.chats.Create(f.chat); err != nil {
		t.Fatal(err)
	}
	f.manager, err = dbmanager.NewManager(f.redis, config.Env.SchemaEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.manager.Close)
	f.manager.RegisterDriver(constants.DatabaseTypePostgreSQL, dbmanager.NewPostgresDriver())
	f.manager.RegisterFetcher(constants.DatabaseTypePostgreSQL, func(dbmanager.DBExecutor) dbmanager.SchemaFetcher { return dbmanager.NewPostgresDriver() })
	f.model = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.modelCalls.Add(1)
		select {
		case f.modelEntered <- struct{}{}:
		default:
		}
		<-f.modelRelease
		w.Header().Set("Content-Type", "application/json")
		if f.modelSchemaReply.Load() {
			content, _ := json.Marshal(map[string]interface{}{"tables": []interface{}{map[string]interface{}{"table_name": f.table, "description": "actual admitted schema refresh", "field_descriptions": []interface{}{map[string]string{"field_name": "refresh_marker", "description": "new isolated column"}}}}})
			json.NewEncoder(w).Encode(map[string]interface{}{"id": "isolated-schema-model", "object": "chat.completion", "choices": []interface{}{map[string]interface{}{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}}})
			return
		}
		arguments, _ := json.Marshal(map[string]interface{}{"assistantMessage": "source gate model completed", "queries": []interface{}{}, "actionButtons": []interface{}{}})
		name := llm.FinalResponseToolName
		if f.modelFinalQueries.Load() {
			arguments, _ = json.Marshal(map[string]interface{}{
				"assistantMessage": sourceGateFinalQueryContent,
				"queries": []interface{}{
					map[string]interface{}{"query": `SELECT id, label FROM public."` + f.table + `" ORDER BY id`, "queryType": "SELECT", "isCritical": false, "explanation": "first actual final query"},
					map[string]interface{}{"query": `SELECT id FROM public."` + f.table + `" WHERE id = 1`, "queryType": "SELECT", "isCritical": false, "explanation": "must not start after refusal"},
				},
				"actionButtons": []interface{}{},
			})
		}
		if f.modelUseSQL.Swap(false) {
			name = llm.ExecuteQueryToolName
			arguments, _ = json.Marshal(map[string]string{"query": "SELECT id FROM public.\"" + f.table + "\"", "explanation": "isolated source probe"})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "isolated-source-gate", "object": "chat.completion", "choices": []interface{}{map[string]interface{}{"index": 0, "message": map[string]interface{}{"role": "assistant", "tool_calls": []interface{}{map[string]interface{}{"id": "gate-final", "type": "function", "function": map[string]interface{}{"name": name, "arguments": string(arguments)}}}}, "finish_reason": "tool_calls"}}})
	}))
	t.Cleanup(f.model.Close)
	// Release before httptest.Server.Close even when an assertion fails.
	t.Cleanup(func() { f.releaseOnce.Do(func() { close(f.modelRelease) }) })
	modelConfig := llm.Config{Provider: constants.OpenAI, APIKey: "isolated-only", BaseURL: f.model.URL + "/v1", Model: "gpt-4o"}
	f.modelClient, err = llm.NewOpenAIClient(modelConfig)
	if err != nil {
		t.Fatal(err)
	}
	f.modelManager = llm.NewManager()
	if err := f.modelManager.RegisterClient(constants.OpenAI, modelConfig); err != nil {
		t.Fatal(err)
	}
	crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := repositories.NewAESReportCipher(crypto)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.EnsureAssistantRunIndexes(ctx, f.database.Collection("assistant_runs")); err != nil {
		t.Fatal(err)
	}
	assistantOptions := []services.AssistantServiceOption{services.WithAssistantClock(services.AssistantFixtureClock), services.WithAssistantInstanceID("t38-isolated-api")}
	if pilot {
		scope, err := json.Marshal(source.OrderSourceByShop)
		if err != nil {
			t.Fatal(err)
		}
		assistantOptions = append(assistantOptions, services.WithAssistantERPQuantityPilot(f.source.SourceID, fmt.Sprintf("%x", sha256.Sum256(scope))))
	}
	f.assistant = services.NewAssistantService(services.NewAssistantChatRepository(f.chats), services.NewAssistantMessageRepository(f.chats),
		repositories.NewAssistantRunRepository(f.database.Collection("assistant_runs"), cipher), cipher,
		services.NewAssistantFixtureCatalog(), services.NewAssistantFixtureSource(), nil, f.policy,
		assistantOptions...)
	kb := repositories.NewKnowledgeBaseRepository(mongoWrapper)
	f.service = services.NewChatService(f.chats, f.manager, f.modelClient, f.modelManager, f.redis,
		repositories.NewVisualizationRepository(mongoWrapper, f.redis), nil, kb, f.dashboards, f.assistant, f.policy)
	f.manager.SetStreamHandler(f.service)
	read, err := f.policy.AcquireRead(ctx, f.owner, f.chat, f.connection)
	if err != nil {
		t.Fatal(err)
	}
	cfgConnection := sourceGateConnectionConfig(f.chat, f.connection)
	if err := f.manager.Connect(read.Context, f.chat.ID.Hex(), f.owner.Hex(), "", cfgConnection); err != nil {
		t.Fatal(err)
	}
	storage, err := f.manager.GetSchemaWithExamples(read.Context, f.chat.ID.Hex(), []string{f.table})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.chats.UpdateConnectionSchema(read.Context, f.chat, f.manager.GetSchemaManager().FormatSchemaForLLM(storage.FullSchema)); err != nil {
		t.Fatal(err)
	}
	read.Close()
	f.userMessage = models.NewMessage(f.owner, f.chat.ID, "user", "original user question", nil, nil)
	if err := f.chats.CreateMessage(f.userMessage); err != nil {
		t.Fatal(err)
	}
	selectType, saved, count := "SELECT", `{"results":[{"id":1,"label":"historical snapshot"}]}`, `SELECT COUNT(*) FROM public."`+f.table+`"`
	selectSQL := `SELECT id, label FROM public."` + f.table + `"`
	paginated := selectSQL + " LIMIT 50 OFFSET offset_size"
	f.query = models.Query{ID: primitive.NewObjectID(), Query: selectSQL, QueryType: &selectType, IsExecuted: true, CanRollback: true,
		RollbackQuery: &selectSQL, ExecutionResult: &saved, Pagination: &models.Pagination{PaginatedQuery: &paginated, CountQuery: &count}}
	queries := []models.Query{f.query}
	f.message = models.NewMessage(f.owner, f.chat.ID, "assistant", "historical answer", &queries, &f.userMessage.ID)
	if err := f.chats.CreateMessage(f.message); err != nil {
		t.Fatal(err)
	}
	f.dashboard = models.NewDashboard(f.owner, f.chat.ID, "historical dashboard")
	if err := f.dashboards.CreateDashboard(ctx, f.dashboard); err != nil {
		t.Fatal(err)
	}
	f.widget = models.NewWidget(f.dashboard.ID, f.chat.ID, f.owner, "historical widget", "table", selectSQL)
	if err := f.dashboards.CreateWidget(ctx, f.widget); err != nil {
		t.Fatal(err)
	}
	f.jwt = utils.NewJWTService("isolated-source-gate-jwt", time.Hour, time.Hour)
	f.startAPI(t, f.service, f.policy)
	return f
}

func TestSourceGateQuantityPilotAutomaticSchemaSkipsModel(t *testing.T) {
	f := newSourceGateAPIWithPilot(t, false, true)
	f.modelSchemaReply.Store(true)
	if _, err := f.control.Exec(`ALTER TABLE public."` + f.table + `" ADD COLUMN refresh_marker integer`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	read, err := f.policy.AcquireRead(ctx, f.owner, f.chat, f.connection)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if err := f.manager.GetSchemaManager().InvalidateSchema(read.Context, f.chat.ID.Hex()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.GetSchemaManager().GetStoredSchemaInfo(read.Context, f.chat.ID.Hex()); err == nil {
		t.Fatal("test did not remove the binding-scoped stored schema")
	}
	if err := f.manager.Disconnect(f.chat.ID.Hex(), f.owner.Hex(), false); err != nil {
		t.Fatal(err)
	}
	if status, err := f.service.ConnectDB(read.Context, f.owner.Hex(), f.chat.ID.Hex(), ""); status != http.StatusOK || err != nil {
		t.Fatalf("pilot connect did not open the read-only connection: %d %v", status, err)
	}
	if _, err := f.manager.GetSchemaManager().GetStoredSchemaInfo(read.Context, f.chat.ID.Hex()); err == nil {
		t.Fatal("pilot first connection unexpectedly scanned the general-purpose schema")
	}
	if err := f.service.HandleSchemaChange(read.Context, f.owner.Hex(), f.chat.ID.Hex(), "", nil); err != nil {
		t.Fatal(err)
	}
	if calls := f.modelCalls.Load(); calls != 0 {
		t.Fatalf("quantity pilot automatic schema made %d external model calls", calls)
	}
	stored, err := f.chats.ReadCurrent(ctx, f.owner, f.chat.ID)
	if err != nil || stored.Connection.CurrentSchema == nil || !strings.Contains(*stored.Connection.CurrentSchema, "refresh_marker") {
		t.Fatalf("quantity pilot did not persist the refreshed schema: %v", err)
	}
}

func sourceGateConnectionConfig(chat *models.Chat, c models.Connection) dbmanager.ConnectionConfig {
	return dbmanager.ConnectionConfig{SourceChat: chat, ChatID: chat.ID.Hex(), Type: c.Type, Host: c.Host, Port: c.Port,
		Database: c.Database, Username: c.Username, Password: c.Password, InitialSchemaDeferred: true,
		ApplicationName: "yanfa:" + chat.SourceID + ":t38-isolated-api"}
}

func (f *sourceGateAPI) startAPI(t *testing.T, service services.ChatService, policy *services.SourceAccessService) {
	t.Helper()
	chat := handlers.NewChatHandler(service)
	f.streamHandler = chat
	service.SetStreamHandler(chat)
	dashboard := services.NewDashboardService(f.dashboards, f.chats, f.manager, f.modelManager, f.redis,
		repositories.NewKnowledgeBaseRepository(&mongodb.MongoDBClient{Client: f.mongo, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}), policy)
	dashboard.SetStreamHandler(chat)
	dash := handlers.NewDashboardHandler(dashboard, services.NewDashboardImportExportService(f.dashboards, f.chats, f.manager))
	assistant := handlers.NewAssistantHandler(f.assistant)
	visual := handlers.NewVisualizationHandler(service)
	upload := handlers.NewUploadHandler(service)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		bearer := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		owner, err := f.jwt.ValidateToken(bearer)
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("userID", *owner)
		c.Next()
	})
	base := router.Group("/api/chats/:id")
	base.POST("/messages", chat.CreateMessage)
	base.PATCH("/messages/:messageId", chat.UpdateMessage)
	base.GET("/messages", chat.ListMessages)
	base.GET("/stream", chat.StreamChat)
	base.POST("/stream/cancel", chat.CancelStream)
	base.POST("/connect", chat.ConnectDB)
	base.POST("/disconnect", chat.DisconnectDB)
	base.GET("/connection-status", chat.GetDBConnectionStatus)
	base.POST("/refresh-schema", chat.RefreshSchema)
	base.GET("/tables", chat.GetTables)
	base.GET("/recommendations", chat.GetQueryRecommendations)
	base.POST("/queries/execute", chat.ExecuteQuery)
	base.POST("/queries/rollback", chat.RollbackQuery)
	base.POST("/queries/results", chat.GetQueryResults)
	base.POST("/queries/cancel", chat.CancelQueryExecution)
	base.PATCH("/queries/edit", chat.EditQuery)
	base.POST("/assistant/resolve", assistant.Resolve)
	base.POST("/assistant/runs", assistant.CreateRun)
	base.GET("/assistant/runs/:runID", assistant.GetRun)
	base.POST("/assistant/runs/:runID/cancel", assistant.CancelRun)
	base.POST("/dashboards/suggest-templates", dash.GenerateBlueprints)
	base.POST("/dashboards/create-from-blueprints", dash.CreateFromBlueprints)
	base.POST("/dashboards/:dashboardId/regenerate", dash.RegenerateDashboard)
	base.POST("/dashboards/:dashboardId/widgets", dash.AddWidget)
	base.POST("/dashboards/:dashboardId/widgets/:widgetId/edit", dash.EditWidget)
	base.POST("/dashboards/:dashboardId/refresh", dash.RefreshDashboard)
	base.POST("/dashboards/:dashboardId/widgets/:widgetId/refresh", dash.RefreshWidget)
	base.GET("/dashboards/:dashboardId", dash.GetDashboard)
	base.GET("/dashboards/:dashboardId/export", dash.ExportDashboard)
	base.POST("/messages/:messageId/visualizations", visual.GenerateVisualization)
	base.POST("/execute-chart", visual.ExecuteChartQuery)
	base.POST("/visualization-data", visual.GetVisualizationData)
	router.GET("/api/upload/:chatID/tables/:tableName", upload.GetTableData)
	router.GET("/api/upload/:chatID/tables/:tableName/download", upload.DownloadTableData)
	router.DELETE("/api/upload/:chatID/tables/:tableName", upload.DeleteTable)
	router.DELETE("/api/upload/:chatID/tables/:tableName/rows/:rowID", upload.DeleteRow)
	router.POST("/api/upload/:chatID/file", upload.UploadFile)
	f.server = httptest.NewServer(router)
	t.Cleanup(f.server.Close)
}

func (f *sourceGateAPI) request(t *testing.T, method, path string, body interface{}, owner primitive.ObjectID) (int, []byte) {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if !owner.IsZero() {
		token, err := f.jwt.GenerateToken(owner.Hex())
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	result, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, result
}

func (f *sourceGateAPI) writer(t *testing.T, busy bool) {
	t.Helper()
	lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive,
		sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
	if lease != nil {
		lease.Close()
	}
	if busy && !errors.Is(err, sourcegate.ErrBusy) {
		t.Fatalf("writer crossed live source work: %v", err)
	}
	if !busy && err != nil {
		t.Fatalf("writer stayed blocked after actual completion: %v", err)
	}
	t.Logf("actual source=%s EX busy=%t observed_error=%v", f.source.SourceID, busy, err)
}

func sourceGateEventually(t *testing.T, check func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("source gate terminal condition not observed")
		case <-ticker.C:
		}
	}
}

func TestSourceGateIntegrationDeniedAPIRootsAndHistory(t *testing.T) {
	f := newSourceGateAPI(t, false)
	root := "/api/chats/" + f.chat.ID.Hex()
	queryBody := map[string]interface{}{"message_id": f.message.ID.Hex(), "query_id": f.query.ID.Hex(), "stream_id": "gate-api"}
	dash := root + "/dashboards/" + f.dashboard.ID.Hex()
	widget := dash + "/widgets/" + f.widget.ID.Hex()
	cases := []struct {
		name, method, path string
		body               interface{}
		status             int
	}{
		{"E02", "POST", root + "/messages", map[string]string{"content": "rejected question", "stream_id": "gate-api"}, 423},
		{"E03", "PATCH", root + "/messages/" + f.userMessage.ID.Hex(), map[string]string{"content": "rejected edit", "stream_id": "gate-api"}, 423},
		{"E05", "POST", root + "/queries/execute", queryBody, 423},
		{"E06", "POST", root + "/queries/rollback", queryBody, 423},
		{"E07", "POST", root + "/queries/results", queryBody, 423},
		{"E08-connect", "POST", root + "/connect", map[string]string{"stream_id": "gate-api"}, 423},
		{"E08-status", "GET", root + "/connection-status", nil, 423},
		{"E09", "POST", root + "/refresh-schema", nil, 423},
		{"E10", "GET", root + "/tables", nil, 423},
		{"E12-miss", "GET", root + "/recommendations?stream_id=gate-api", nil, 423},
		{"E13", "POST", root + "/assistant/resolve", map[string]interface{}{"schema_version": 1, "request_id": "gate-resolve", "workflow": "product_analysis", "product_query": "fixture", "metrics": []string{"quantity", "sales"}}, 423},
		{"E14", "POST", root + "/assistant/runs", map[string]interface{}{"schema_version": 1, "request_id": "gate-run", "workflow": "product_analysis", "basis": "payment", "period": map[string]string{"start_date": "2026-09-01", "end_date_exclusive": "2026-09-08", "timezone": "Asia/Shanghai"}, "scope": map[string]string{"mode": "all"}, "product": map[string]string{"product_id": "fixture"}, "group_by": "shop", "metrics": []string{"quantity", "sales"}}, 423},
		{"E15-blueprints", "POST", root + "/dashboards/suggest-templates?stream_id=gate-api", map[string]string{"prompt": "rejected"}, 423},
		{"E15-create", "POST", root + "/dashboards/create-from-blueprints?stream_id=gate-api", map[string]interface{}{"blueprint_indices": []int{0}}, 423},
		{"E15-regenerate", "POST", dash + "/regenerate?stream_id=gate-api", map[string]string{"reason": "schema_changed"}, 423},
		{"E15-add", "POST", dash + "/widgets", map[string]string{"prompt": "rejected"}, 423},
		{"E15-edit", "POST", widget + "/edit", map[string]string{"prompt": "rejected"}, 423},
		{"E17-dashboard", "POST", dash + "/refresh?stream_id=gate-api", nil, 423},
		{"E17-widget", "POST", widget + "/refresh?stream_id=gate-api", nil, 423},
		{"E18", "POST", root + "/messages/" + f.message.ID.Hex() + "/visualizations", map[string]string{"query_id": f.query.ID.Hex()}, 423},
		{"E19-chart", "POST", root + "/execute-chart", map[string]interface{}{"chart_configuration": map[string]interface{}{"data_fetch": map[string]string{"optimized_query": f.query.Query}}}, 423},
		{"E19-lazy", "POST", root + "/visualization-data", queryBody, 423},
		{"E22-read", "GET", "/api/upload/" + f.chat.ID.Hex() + "/tables/" + f.table, nil, 403},
		{"E22-download", "GET", "/api/upload/" + f.chat.ID.Hex() + "/tables/" + f.table + "/download", nil, 403},
		{"E23-delete", "DELETE", "/api/upload/" + f.chat.ID.Hex() + "/tables/" + f.table, nil, 403},
		{"E23-row", "DELETE", "/api/upload/" + f.chat.ID.Hex() + "/tables/" + f.table + "/rows/1", nil, 403},
	}
	writer, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive, sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	beforeWire, beforeModel := f.wire.Load(), f.modelCalls.Load()
	counts := map[string]int64{}
	for _, name := range []string{"messages", "assistant_runs", "dashboards", "widgets"} {
		count, err := f.database.Collection(name).CountDocuments(context.Background(), bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		counts[name] = count
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			status, body := f.request(t, test.method, test.path, test.body, f.owner)
			if status != test.status {
				t.Fatalf("status=%d want=%d body=%s", status, test.status, body)
			}
		})
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	part, err := form.CreateFormFile("file", "isolated.csv")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(part, "id,label\n1,rejected\n")
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", f.server.URL+"/api/upload/"+f.chat.ID.Hex()+"/file", &upload)
	token, _ := f.jwt.GenerateToken(f.owner.Hex())
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("E23-upload status=%d", resp.StatusCode)
	}
	for name, before := range counts {
		after, err := f.database.Collection(name).CountDocuments(context.Background(), bson.M{})
		if err != nil || after != before {
			t.Fatalf("denied roots changed %s: before=%d after=%d err=%v", name, before, after, err)
		}
	}
	original, err := f.chats.FindMessageByID(f.userMessage.ID)
	if err != nil || original.Content != "original user question" || original.IsEdited {
		t.Fatalf("rejected edit changed old message: %v", err)
	}
	answer, err := f.chats.FindMessageByID(f.message.ID)
	if err != nil || (*answer.Queries)[0].ExecutionResult == nil || *(*answer.Queries)[0].ExecutionResult != *f.query.ExecutionResult {
		t.Fatalf("rejection cleared persisted results: %v", err)
	}
	status, body := f.request(t, "GET", root+"/messages", nil, f.owner)
	if status != 200 || !bytes.Contains(body, []byte("historical answer")) {
		t.Fatalf("history unavailable under EX: %d %s", status, body)
	}
	status, body = f.request(t, "GET", dash+"/export", nil, f.owner)
	if status != 200 || !bytes.Contains(body, []byte("historical dashboard")) {
		t.Fatalf("metadata export unavailable under EX: %d %s", status, body)
	}
	status, _ = f.request(t, "PATCH", root+"/queries/edit", map[string]string{"message_id": f.message.ID.Hex(), "query_id": f.query.ID.Hex(), "query": f.query.Query}, f.owner)
	// Executed SQL may reject a local edit by its original edit-state rule;
	// either original outcome is source-free, never a source lock refusal.
	if status == 423 {
		t.Fatal("local SQL edit was gated as live execution")
	}
	currentCacheChat, err := f.chats.ReadCurrent(context.Background(), f.owner, f.chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	cached := services.NewCachedQueryRecommendations(currentCacheChat, []dtos.CachedQueryRecommendation{{Text: "cached one"}, {Text: "cached two"}, {Text: "cached three"}, {Text: "cached four"}})
	cache, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.redis.SetCompressed("recommendations:"+f.chat.ID.Hex(), cache, time.Hour, context.Background()); err != nil {
		t.Fatal(err)
	}
	status, body = f.request(t, "GET", root+"/recommendations?stream_id=gate-api", nil, f.owner)
	if status != 200 || !bytes.Contains(body, []byte("cached one")) || !bytes.Contains(body, []byte("cached four")) {
		t.Fatalf("cached recommendations acquired source during EX: %d %s", status, body)
	}
	if f.wire.Load() != beforeWire || f.modelCalls.Load() != beforeModel {
		t.Fatalf("denial/history accessed PG/model: wire %d->%d model %d->%d", beforeWire, f.wire.Load(), beforeModel, f.modelCalls.Load())
	}
	for _, staleScope := range []string{"", "different-selection-revision"} {
		cached.Scope = staleScope
		data, _ := json.Marshal(cached)
		if err := f.redis.SetCompressed("recommendations:"+f.chat.ID.Hex(), data, time.Hour, context.Background()); err != nil {
			t.Fatal(err)
		}
		status, body = f.request(t, "GET", root+"/recommendations?stream_id=gate-api", nil, f.owner)
		if status != 423 || bytes.Contains(body, []byte("cached one")) || f.wire.Load() != beforeWire || f.modelCalls.Load() != beforeModel {
			t.Fatalf("unscoped/stale recommendations bypassed source: %d %s", status, body)
		}
	}

	status, _ = f.request(t, "POST", root+"/messages", map[string]string{"content": "foreign", "stream_id": "gate-api"}, primitive.NewObjectID())
	if status != 403 && status != 404 {
		t.Fatalf("foreign owner status=%d", status)
	}
	status, _ = f.request(t, "POST", root+"/messages", map[string]string{"content": "anonymous", "stream_id": "gate-api"}, primitive.NilObjectID)
	if status != 401 {
		t.Fatalf("anonymous status=%d", status)
	}
}

func TestSourceGateIntegrationQuestionSurvivesHTTPAndSSE(t *testing.T) {
	f := newSourceGateAPI(t, true)
	root := "/api/chats/" + f.chat.ID.Hex()
	status, body := f.request(t, "POST", root+"/messages", map[string]string{"content": "question behind real model barrier", "stream_id": "detached-gate"}, f.owner)
	if status != 200 {
		t.Fatalf("question not accepted: %d %s", status, body)
	}
	select {
	case <-f.modelEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("real model HTTP request was not reached")
	}
	f.writer(t, true)
	// No live SSE transport is required for this root to remain active.
	status, history := f.request(t, "GET", root+"/messages", nil, f.owner)
	if status != 200 || !bytes.Contains(history, []byte("question behind real model barrier")) {
		t.Fatal("accepted question was not persisted while its real model request remained active")
	}
	f.writer(t, true)
	f.releaseOnce.Do(func() { close(f.modelRelease) })
	sourceGateEventually(t, func() bool {
		rows, _, err := f.chats.FindMessagesByChat(f.chat.ID, 1, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Content == "source gate model completed" {
				return true
			}
		}
		return false
	})
	sourceGateEventually(t, func() bool {
		lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive, sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
		if lease != nil {
			lease.Close()
			return true
		}
		if !errors.Is(err, sourcegate.ErrBusy) {
			t.Fatal(err)
		}
		return false
	})
}

func TestSourceGateIntegrationSQLCancellationAndPermits(t *testing.T) {
	f := newSourceGateAPI(t, false)
	ctx := context.Background()
	if _, err := f.manager.GetConnectionContext(ctx, f.chat.ID.Hex()); err == nil {
		t.Fatal("ERP executor accepted a missing permit")
	}
	if _, err := f.manager.GetConnection(f.chat.ID.Hex()); err == nil {
		t.Fatal("ERP exposed a context-free executor")
	}
	if info, ok := f.manager.GetConnectionInfo(f.chat.ID.Hex()); !ok || info.DB != nil {
		t.Fatal("ERP status metadata exposed the pool")
	}
	if _, ok := f.manager.GetConnections()[f.chat.ID.Hex()]; ok {
		t.Fatal("ERP appeared in raw pool enumeration")
	}
	beforeWire := f.wire.Load()
	if _, err := f.policy.AcquireRead(ctx, primitive.NewObjectID(), f.chat, f.connection); err == nil {
		t.Fatal("foreign owner acquired source read")
	}
	if _, err := f.manager.GetSchemaManager().GetStoredSchemaInfo(ctx, primitive.NewObjectID().Hex()); err == nil {
		t.Fatal("missing stored schema silently fetched a live source")
	}
	if f.wire.Load() != beforeWire {
		t.Fatal("missing permission/schema metadata touched PG")
	}
	read, err := f.policy.AcquireRead(ctx, f.owner, f.chat, f.connection)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := f.gate.Acquire(ctx, sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Shared,
		sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString(), OwnerID: primitive.NewObjectID().Hex(), ChatID: f.chat.ID.Hex(), BindingRevision: f.chat.SourceBindingRevision, ConnectionFingerprint: f.chat.SourceConnectionFingerprint, ConfigRevision: f.source.ConfigRevision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.GetConnectionContext(sourcegate.WithLease(ctx, wrong), f.chat.ID.Hex()); err == nil {
		t.Fatal("ERP executor accepted a forged owner")
	}
	wrong.Close()
	for _, identity := range []sourcegate.Identity{
		{SourceID: f.source.SourceID, OperationID: uuid.NewString(), OwnerID: f.owner.Hex(), ChatID: primitive.NewObjectID().Hex(), BindingRevision: f.chat.SourceBindingRevision, ConnectionFingerprint: f.chat.SourceConnectionFingerprint, ConfigRevision: f.source.ConfigRevision},
		{SourceID: f.source.SourceID, OperationID: uuid.NewString(), OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex(), BindingRevision: f.chat.SourceBindingRevision + 1, ConnectionFingerprint: f.chat.SourceConnectionFingerprint, ConfigRevision: f.source.ConfigRevision},
		{SourceID: f.source.SourceID, OperationID: uuid.NewString(), OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex(), BindingRevision: f.chat.SourceBindingRevision, ConnectionFingerprint: "different reader", ConfigRevision: f.source.ConfigRevision},
	} {
		forged, err := f.gate.Acquire(ctx, sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Shared, identity)
		if err != nil {
			t.Fatal(err)
		}
		_, accessErr := f.manager.GetConnectionContext(sourcegate.WithLease(ctx, forged), f.chat.ID.Hex())
		forged.Close()
		if accessErr == nil {
			t.Fatalf("accepted forged Chat/binding/reader: %+v", identity)
		}
	}
	if _, err := f.manager.GetConnectionContext(sourcegate.WithLease(ctx, &sourcegate.Lease{}), f.chat.ID.Hex()); err == nil {
		t.Fatal("accepted an unissued permit")
	}
	otherID := uuid.NewString()
	otherTarget, err := f.gate.Provision(otherID)
	if err != nil {
		t.Fatal(err)
	}
	otherLease, err := f.gate.Acquire(ctx, otherTarget, sourcegate.Shared, sourcegate.Identity{SourceID: otherID, OperationID: uuid.NewString(), OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex(), BindingRevision: f.chat.SourceBindingRevision, ConnectionFingerprint: f.chat.SourceConnectionFingerprint, ConfigRevision: f.source.ConfigRevision})
	if err != nil {
		t.Fatal(err)
	}
	_, crossErr := f.manager.GetConnectionContext(sourcegate.WithLease(ctx, otherLease), f.chat.ID.Hex())
	otherLease.Close()
	if crossErr == nil {
		t.Fatal("ERP executor accepted another source's actual lease")
	}
	executor, err := f.manager.GetConnectionContext(read.Context, f.chat.ID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if executor.GetDB() != nil {
		t.Fatal("ERP executor exposed the raw pool")
	}
	if err := executor.Exec("DELETE FROM public.\"" + f.table + "\""); err == nil {
		t.Fatal("ERP executor exposed raw mutation")
	}
	read.Close()
	var rows []map[string]interface{}
	if err := executor.QueryRows("SELECT 1", &rows); err == nil {
		t.Fatal("ERP executor used a closed permit")
	}
	control, err := f.control.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Rollback()
	if _, err := control.ExecContext(ctx, `LOCK TABLE public."`+f.table+`" IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := f.service.ExecuteQuery(requestCtx, f.owner.Hex(), f.chat.ID.Hex(), &dtos.ExecuteQueryRequest{MessageID: f.message.ID.Hex(), QueryID: f.query.ID.Hex(), StreamID: "cancel-gate"})
		done <- err
	}()
	sourceGateEventually(t, func() bool {
		var blocked bool
		err := f.control.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event_type='Lock' AND query LIKE $1)`, "%yanfa_operation:%"+"FROM public.\""+f.table+"\"%").Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	f.writer(t, true)
	// The owned cancellation only signals; ExecuteQuery returns after SQL worker,
	// rollback and dedicated connection cleanup have actually completed.
	f.service.CancelQueryExecution(primitive.NewObjectID().Hex(), f.chat.ID.Hex(), f.message.ID.Hex(), f.query.ID.Hex(), "cancel-gate")
	f.writer(t, true)
	f.service.CancelQueryExecution(f.owner.Hex(), f.chat.ID.Hex(), f.message.ID.Hex(), f.query.ID.Hex(), "cancel-gate")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled SQL reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("actual SQL/rollback cleanup did not complete")
	}
	var active bool
	if err := f.control.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND query LIKE $1)`, "%yanfa_operation:%"+"FROM public.\""+f.table+"\"%").Scan(&active); err != nil || active {
		t.Fatalf("SQL still active after cancellation terminal: %v", err)
	}
	f.writer(t, false)
}

func TestSourceGateIntegrationNestedRunOutlivesToolWait(t *testing.T) {
	f := newSourceGateAPI(t, true)
	crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := repositories.NewAESReportCipher(crypto)
	if err != nil {
		t.Fatal(err)
	}
	// T19 remains fixture-bound. This exercises its actual async workflow,
	// explanation SDK and production run/message repositories, not ERP metrics.
	assistant := services.NewAssistantService(services.NewAssistantChatRepository(f.chats), services.NewAssistantMessageRepository(f.chats),
		repositories.NewAssistantRunRepository(f.database.Collection("assistant_runs"), cipher), cipher, services.NewAssistantFixtureCatalog(), services.NewAssistantFixtureSource(), nil, f.policy,
		services.WithAssistantClock(services.AssistantFixtureClock), services.WithAssistantInstanceID("t38-nested-run"),
		services.WithAssistantExplanationModel(services.NewAssistantExplanationModel(f.modelClient)))
	read, err := f.policy.AcquireRead(context.Background(), f.owner, f.chat, f.connection)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	toolCtx, cancel := context.WithCancel(read.Context)
	defer cancel()
	executor := services.BuildToolExecutor(f.manager, f.owner.Hex(), f.chat.ID.Hex(), "postgresql", assistant)
	done := make(chan struct{})
	requestID := "t38-nested-" + primitive.NewObjectID().Hex()
	go func() {
		defer close(done)
		_, _ = executor(toolCtx, llm.ToolCall{ID: "nested", Name: llm.AnalyzeProductToolName, Arguments: map[string]interface{}{"request_id": requestID, "product_query": services.AssistantFixtureProduct}})
	}()
	select {
	case <-f.modelEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("actual async run did not reach explanation model HTTP")
	}
	var stored models.RunRecord
	if err := f.database.Collection("assistant_runs").FindOne(context.Background(), bson.M{"request_id": requestID}).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled tool wait did not end")
	}
	read.Close()
	f.writer(t, true)
	view, err := assistant.GetRun(context.Background(), models.AccessContext{OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex()}, stored.RunID)
	if err != nil || view.Status.IsTerminal() {
		t.Fatalf("tool wait ended the real run: %s %v", view.Status, err)
	}
	f.releaseOnce.Do(func() { close(f.modelRelease) })
	sourceGateEventually(t, func() bool {
		view, err = assistant.GetRun(context.Background(), models.AccessContext{OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex()}, stored.RunID)
		if err != nil {
			t.Fatal(err)
		}
		return view.Status.IsTerminal()
	})
	if view.Status != models.RunStatusCompleted || view.Report == nil {
		t.Fatalf("real fixture workflow did not persist its final report: %s", view.Status)
	}
	before := f.modelCalls.Load()
	replayed, err := assistant.CreateRun(context.Background(), models.AccessContext{OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex()}, stored.NormalizedInput)
	if err != nil || replayed.RunID != stored.RunID || f.modelCalls.Load() != before {
		t.Fatalf("replay restarted an execution: %v", err)
	}
	sourceGateEventually(t, func() bool {
		lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive, sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
		if lease == nil {
			if !errors.Is(err, sourcegate.ErrBusy) {
				t.Fatal(err)
			}
			return false
		}
		defer lease.Close()
		history, err := assistant.GetRun(context.Background(), models.AccessContext{OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex()}, stored.RunID)
		if err != nil || history.Report == nil || history.Report.ReportID != stored.RunID {
			t.Fatalf("persisted report unavailable during writer EX: %v", err)
		}
		return true
	})
}

func TestSourceGateIntegrationReadonlyTransactionLifetime(t *testing.T) {
	f := newSourceGateAPI(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read, err := f.policy.AcquireRead(ctx, f.owner, f.chat, f.connection)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	reader, err := dbmanager.NewSourceAssistantReadonlyReader(read.Context, sourceGateConnectionConfig(f.chat, f.connection), f.policy)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	probe, err := reader.RunWriteProbe(read.Context, "isolated-bound-value")
	if err != nil || probe.WriteSQLState != "25006" || probe.Echo != "isolated-bound-value" {
		t.Fatalf("real readonly boundary failed: %+v %v", probe, err)
	}
	if err := reader.Ping(context.Background()); err == nil {
		t.Fatal("bound reader accepted a missing source permit")
	}
	control, err := f.control.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Rollback()
	if _, err := control.ExecContext(context.Background(), `LOCK TABLE public."`+f.table+`" IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- reader.WithReadOnlyTx(read.Context, func(tx *sql.Tx) error {
			var id int
			return tx.QueryRowContext(ctx, `SELECT id FROM public."`+f.table+`"`).Scan(&id)
		})
	}()
	sourceGateEventually(t, func() bool {
		var blocked bool
		if err := f.control.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name LIKE 's1:%' AND state='active' AND wait_event_type='Lock' AND query LIKE $1)`, "%"+f.table+"%").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	read.Close()
	f.writer(t, true)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled readonly transaction succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("readonly transaction did not finish physical cleanup")
	}
	var active bool
	if err := f.control.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name LIKE 's1:%' AND state='active' AND query LIKE $1)`, "%"+f.table+"%").Scan(&active); err != nil || active {
		t.Fatalf("readonly SQL remained active: %v", err)
	}
	f.writer(t, false)
}

func TestSourceGateIntegrationDashboardWidgetJoin(t *testing.T) {
	f := newSourceGateAPI(t, false)
	root := "/api/chats/" + f.chat.ID.Hex()
	second := models.NewWidget(f.dashboard.ID, f.chat.ID, f.owner, "second widget", "table", "SELECT 2 AS independent_value")
	if err := f.dashboards.CreateWidget(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	streamCtx, stopStream := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopStream()
	streamRequest, _ := http.NewRequest("GET", f.server.URL+root+"/stream?stream_id=widget-gate", nil)
	streamRequest = streamRequest.WithContext(streamCtx)
	token, _ := f.jwt.GenerateToken(f.owner.Hex())
	streamRequest.Header.Set("Authorization", "Bearer "+*token)
	stream, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatalf("actual SSE connection status=%d", stream.StatusCode)
	}
	control, err := f.control.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Rollback()
	if _, err := control.ExecContext(context.Background(), `LOCK TABLE public."`+f.table+`" IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		request, _ := http.NewRequest("POST", f.server.URL+root+"/dashboards/"+f.dashboard.ID.Hex()+"/refresh?stream_id=widget-gate", nil)
		request.Header.Set("Authorization", "Bearer "+*token)
		response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
		if err != nil {
			done <- 0
			return
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		done <- response.StatusCode
	}()
	sourceGateEventually(t, func() bool {
		var blocked bool
		if err := f.control.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE state='active' AND wait_event_type='Lock' AND query LIKE $1)`, "%yanfa_operation:%"+f.table+"%").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	f.writer(t, true)
	if err := control.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-done:
		if status != 200 {
			t.Fatalf("real widget refresh status=%d", status)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("widget join did not complete")
	}
	scanner := bufio.NewScanner(stream.Body)
	seen := map[string]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != constants.SSEEventDashboardWidgetData {
			continue
		}
		var data dtos.DashboardWidgetDataEvent
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.WidgetID == f.widget.ID.Hex() {
			if len(data.Data) != 1 || data.Data[0]["label"] != "persisted source row" {
				t.Fatalf("wrong actual first widget data: %+v", data)
			}
			seen[data.WidgetID] = true
		}
		if data.WidgetID == second.ID.Hex() {
			if len(data.Data) != 1 || data.Data[0]["independent_value"] != float64(2) {
				t.Fatalf("wrong actual second widget data: %+v", data)
			}
			seen[data.WidgetID] = true
		}
		if seen[f.widget.ID.Hex()] && seen[second.ID.Hex()] {
			break
		}
	}
	if !seen[f.widget.ID.Hex()] || !seen[second.ID.Hex()] {
		t.Fatalf("actual widget SSE results missing: %v", scanner.Err())
	}
	f.writer(t, false)
}

func (f *sourceGateAPI) replacePolicy(t *testing.T, policy *services.SourceAccessService) {
	t.Helper()
	crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := repositories.NewAESReportCipher(crypto)
	if err != nil {
		t.Fatal(err)
	}
	f.policy = policy
	f.assistant = services.NewAssistantService(services.NewAssistantChatRepository(f.chats), services.NewAssistantMessageRepository(f.chats), repositories.NewAssistantRunRepository(f.database.Collection("assistant_runs"), cipher), cipher,
		services.NewAssistantFixtureCatalog(), services.NewAssistantFixtureSource(), nil, policy, services.WithAssistantClock(services.AssistantFixtureClock), services.WithAssistantInstanceID("t38-isolated-api"))
	wrapper := &mongodb.MongoDBClient{Client: f.mongo, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}
	f.service = services.NewChatService(f.chats, f.manager, f.modelClient, f.modelManager, f.redis, repositories.NewVisualizationRepository(wrapper, f.redis), nil, repositories.NewKnowledgeBaseRepository(wrapper), f.dashboards, f.assistant, policy)
	f.manager.SetStreamHandler(f.service)
	f.server.Close()
	f.startAPI(t, f.service, policy)
}

func TestSourceGateIntegrationRecoveryOutageAndIndependentPG(t *testing.T) {
	f := newSourceGateAPI(t, false)
	ctx := context.Background()
	root := "/api/chats/" + f.chat.ID.Hex()
	independentURL, err := url.Parse(os.Getenv("SOURCE_ACCESS_TEST_PG_URL"))
	if err != nil {
		t.Fatal(err)
	}
	independentURL.Path = "/v15_spreadsheets"
	independentDB, err := sql.Open("postgres", independentURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer independentDB.Close()
	independentTable := "source_gate_independent_" + primitive.NewObjectID().Hex()
	if _, err := independentDB.ExecContext(ctx, `CREATE TABLE public."`+independentTable+`" (id integer)`); err != nil {
		t.Fatal(err)
	}
	defer independentDB.ExecContext(context.Background(), `DROP TABLE public."`+independentTable+`"`)
	createIndependent := func(label string) {
		candidate := f.connection.Clone()
		request := &dtos.CreateChatRequest{Connection: dtos.CreateConnectionRequest{Type: "postgresql", Host: candidate.Host, Port: candidate.Port, Username: *candidate.Username, Password: candidate.Password, Database: "v15_spreadsheets"}}
		created, status, err := f.service.Create(f.owner.Hex(), request)
		if err != nil || status != 201 || created.SourceID != "" {
			t.Fatalf("%s: unrelated PG creation failed: status=%d err=%v", label, status, err)
		}
		if status, err := f.service.ConnectDB(ctx, f.owner.Hex(), created.ID, "independent-gate"); err != nil || status != 200 {
			t.Fatalf("%s: unrelated PG connect failed: %v", label, err)
		}
		if _, status, err := f.service.CreateMessage(ctx, f.owner.Hex(), created.ID, "independent-gate", "independent question "+label, ""); err != nil || status != 200 {
			t.Fatalf("%s: unrelated PG question failed: %v", label, err)
		}
		id, err := primitive.ObjectIDFromHex(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		sourceGateEventually(t, func() bool {
			rows, _, err := f.chats.FindMessagesByChat(id, 1, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Content == "source gate model completed" {
					return true
				}
			}
			return false
		})
		if status, err := f.service.DisconnectDB(ctx, f.owner.Hex(), created.ID, ""); err != nil || status != 200 {
			t.Fatalf("%s: independent cleanup failed: %v", label, err)
		}
	}
	for _, phase := range []models.ERPSourcePhase{models.ERPSourceSyncing, models.ERPSourceRecovering} {
		source, err := f.registry.Get(ctx, f.source.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		operation := &models.ERPSourceOperation{OperationID: uuid.NewString(), Kind: "isolated-maintenance", InstanceID: "t38-isolated-api"}
		previous := ""
		if source.ActiveOperation != nil {
			previous = source.ActiveOperation.OperationID
		}
		if err := f.registry.UpdateStateCAS(ctx, source.SourceID, source.StateRevision, previous, repositories.ERPSourceState{Phase: phase, ActiveOperation: operation, RecoveryVerifiedInstanceID: "t38-isolated-api"}); err != nil {
			t.Fatal(err)
		}
		wire, model := f.wire.Load(), f.modelCalls.Load()
		status, body := f.request(t, "POST", root+"/messages", map[string]string{"content": "must be refused", "stream_id": "gate-phase"}, f.owner)
		if status != 423 || f.wire.Load() != wire || f.modelCalls.Load() != model {
			t.Fatalf("%s did not deny before PG/model: %d %s", phase, status, body)
		}
		createIndependent(string(phase))
	}
	// Kill only this fixture's registry client. Chat Mongo and independent PG
	// remain real and available, proving an actual ERP storage-client outage.
	registryClient, err := mongo.Connect(ctx, options.Client().ApplyURI(f.mongoURI).SetServerSelectionTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	registry := repositories.NewERPSourceRepository(registryClient.Database(f.database.Name()))
	policy, err := services.NewSourceAccessService(registry, f.gate, config.ERPSyncConfig{InstanceID: "t38-isolated-api", DeploymentUID: uint32(os.Getuid()), LockDirectory: f.gate.Directory(), Associations: []config.ERPAssociation{{SourceID: f.source.SourceID, EndpointFingerprints: f.source.ApprovedEndpointAliases, ApprovalRef: "isolated-only"}}}, f.chats)
	if err != nil {
		t.Fatal(err)
	}
	if err := registryClient.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	f.replacePolicy(t, policy)
	wire, model := f.wire.Load(), f.modelCalls.Load()
	status, body := f.request(t, "POST", root+"/messages", map[string]string{"content": "must be refused", "stream_id": "gate-outage"}, f.owner)
	if status != 423 || f.wire.Load() != wire || f.modelCalls.Load() != model {
		t.Fatalf("ERP state outage leaked to source/model: %d %s", status, body)
	}
	createIndependent("registry-outage")
	current, err := f.chats.ReadCurrent(ctx, f.owner, f.chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := *current
	next.Connection = f.connection.Clone()
	next.Connection.Host = "unapproved.invalid"
	next.SourceConnectionFingerprint = services.ConnectionFingerprint(next.Connection)
	next.SourceBindingRevision++
	next.ConnectionRevision++
	next.SourceBindingConfirmed = false
	if err := utils.EncryptConnection(&next.Connection); err != nil {
		t.Fatal(err)
	}
	bindingLease, err := f.gate.Acquire(ctx, sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive,
		sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString(), OwnerID: f.owner.Hex(), ChatID: f.chat.ID.Hex()})
	if err != nil {
		t.Fatal(err)
	}
	bindingErr := f.chats.MarkPendingCAS(sourcegate.WithLease(ctx, bindingLease), current, &next)
	bindingLease.Close()
	if bindingErr != nil {
		t.Fatal(bindingErr)
	}
	wire, model = f.wire.Load(), f.modelCalls.Load()
	status, body = f.request(t, "POST", root+"/messages", map[string]string{"content": "sticky ERP cannot downgrade", "stream_id": "gate-unresolved"}, f.owner)
	if status != 423 || f.wire.Load() != wire || f.modelCalls.Load() != model {
		t.Fatalf("unresolved ERP reached source/model: %d %s", status, body)
	}
}

func TestSourceGateIntegrationGateErrorStopsModelLoop(t *testing.T) {
	f := newSourceGateAPI(t, true)
	f.modelUseSQL.Store(true)
	root := "/api/chats/" + f.chat.ID.Hex()
	status, body := f.request(t, "POST", root+"/messages", map[string]string{"content": "mid-loop gate failure", "stream_id": "gate-loop"}, f.owner)
	if status != 200 {
		t.Fatalf("question not accepted: %d %s", status, body)
	}
	select {
	case <-f.modelEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("model HTTP not reached")
	}
	source, err := f.registry.Get(context.Background(), f.source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.registry.UpdateStateCAS(context.Background(), source.SourceID, source.StateRevision, "", repositories.ERPSourceState{Phase: models.ERPSourceRecovering, RecoveryVerifiedInstanceID: "t38-isolated-api"}); err != nil {
		t.Fatal(err)
	}
	wire := f.wire.Load()
	f.releaseOnce.Do(func() { close(f.modelRelease) })
	sourceGateEventually(t, func() bool {
		lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive, sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
		if lease == nil {
			if !errors.Is(err, sourcegate.ErrBusy) {
				t.Fatal(err)
			}
			return false
		}
		lease.Close()
		return true
	})
	if f.wire.Load() != wire || f.modelCalls.Load() != 1 {
		t.Fatalf("gate error entered another SQL/model step: SQL bytes %d->%d model requests=%d", wire, f.wire.Load(), f.modelCalls.Load())
	}
}

func TestSourceGateIntegrationLocalKBEditDuringExclusive(t *testing.T) {
	f := newSourceGateAPI(t, false)
	address := os.Getenv("SOURCE_ACCESS_TEST_QDRANT_ADDR")
	if address == "" {
		t.Skip("SOURCE_ACCESS_TEST_QDRANT_ADDR must name the isolated manager-owned Qdrant gRPC endpoint")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := vectordb.NewQdrantClient(vectordb.QdrantConfig{Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer vectors.Close()
	embeddings, err := embedding.NewOpenAIProvider("isolated-nonsecret-key", "text-embedding-3-small", f.model.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	vectorization := services.NewVectorizationService(embeddings, vectors)
	if err := vectorization.EnsureReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrapper := &mongodb.MongoDBClient{Client: f.mongo, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}
	kbRepo := repositories.NewKnowledgeBaseRepository(wrapper)
	service := services.NewChatService(f.chats, f.manager, f.modelClient, f.modelManager, f.redis, repositories.NewVisualizationRepository(wrapper, f.redis), vectorization, kbRepo, f.dashboards, f.assistant, f.policy)
	writer, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive, sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	wire, model := f.wire.Load(), f.modelCalls.Load()
	_, status, err := service.UpdateKnowledgeBase(context.Background(), f.owner.Hex(), f.chat.ID.Hex(), []models.TableDescription{{TableName: "stored", FieldDescriptions: []models.FieldDescription{{FieldName: "label", Description: "local edit during synchronization"}}}})
	if err != nil || status != 200 {
		t.Fatalf("local KB edit was coupled to live source: %d %v", status, err)
	}
	kb, err := kbRepo.FindByChatID(context.Background(), f.chat.ID)
	if err != nil || kb == nil || len(kb.TableDescriptions) != 1 || len(kb.TableDescriptions[0].FieldDescriptions) != 1 || kb.TableDescriptions[0].FieldDescriptions[0].Description != "local edit during synchronization" {
		t.Fatalf("local KB edit was not persisted: %+v %v", kb, err)
	}
	if f.wire.Load() != wire || f.modelCalls.Load() != model {
		t.Fatal("local ERP metadata edit implicitly queried/embedded the source")
	}
}

func TestSourceGateIntegrationDetachedSchemaRefresh(t *testing.T) {
	f := newSourceGateAPI(t, true)
	// This lifecycle test deliberately covers the legacy model-enhancement
	// branch. Strict refresh has a separate catalog-only regression.
	enabled := true
	if err := f.chats.PatchMetadata(context.Background(), f.owner, f.chat.ID, repositories.ChatMetadataPatch{AutoExecuteQuery: &enabled}); err != nil {
		t.Fatal(err)
	}
	f.modelSchemaReply.Store(true)
	if _, err := f.control.ExecContext(context.Background(), `ALTER TABLE public."`+f.table+`" ADD COLUMN refresh_marker text DEFAULT 'schema probe'`); err != nil {
		t.Fatal(err)
	}
	wire := f.wire.Load()
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, "POST", f.server.URL+"/api/chats/"+f.chat.ID.Hex()+"/refresh-schema", nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.jwt.GenerateToken(f.owner.Hex())
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+*token)
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("refresh HTTP did not accept the admitted root: %d", response.StatusCode)
	}
	cancel()
	select {
	case <-f.modelEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("real schema model terminal was not reached after HTTP return")
	}
	inspection, err := f.policy.AcquireRead(context.Background(), f.owner, f.chat, f.connection)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.manager.GetSchemaManager().GetStoredSchemaInfo(inspection.Context, f.chat.ID.Hex())
	inspection.Close()
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Tables[f.table].Columns["refresh_marker"].Name != "refresh_marker" || f.wire.Load() <= wire {
		t.Fatal("refresh did not observe the actual new PostgreSQL column")
	}
	f.writer(t, true)
	f.releaseOnce.Do(func() { close(f.modelRelease) })
	sourceGateEventually(t, func() bool {
		lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive, sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
		if lease == nil {
			if !errors.Is(err, sourcegate.ErrBusy) {
				t.Fatal(err)
			}
			return false
		}
		lease.Close()
		return true
	})
	wrapper := &mongodb.MongoDBClient{Client: f.mongo, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}
	kb, err := repositories.NewKnowledgeBaseRepository(wrapper).FindByChatID(context.Background(), f.chat.ID)
	if err != nil || kb == nil || len(kb.TableDescriptions) != 1 || kb.TableDescriptions[0].Description != "actual admitted schema refresh" {
		t.Fatalf("refresh released before actual model/KB persistence: %+v %v", kb, err)
	}
}

// Forward every service event through the real SSE handler. Holding the error
// callback keeps the real producer on its cleanup path; no event is fabricated.
type sourceGateErrorStream struct {
	services.StreamHandler
	forwarded atomic.Int64
	entered   chan struct{}
	resume    chan struct{}
	once      sync.Once
}

func (s *sourceGateErrorStream) HandleStreamEvent(owner, chat, stream string, event dtos.StreamResponse) {
	s.StreamHandler.HandleStreamEvent(owner, chat, stream, event)
	s.forwarded.Add(1)
	if event.Event == "ai-response-error" {
		s.once.Do(func() { close(s.entered) })
		<-s.resume
	}
}

func (f *sourceGateAPI) writerAvailable(t *testing.T) bool {
	t.Helper()
	lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive,
		sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
	if errors.Is(err, sourcegate.ErrBusy) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	return true
}

func (f *sourceGateAPI) setSourcePhase(t *testing.T, phase models.ERPSourcePhase) {
	t.Helper()
	source, err := f.registry.Get(context.Background(), f.source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.registry.UpdateStateCAS(context.Background(), source.SourceID, source.StateRevision, "", repositories.ERPSourceState{Phase: phase, RecoveryVerifiedInstanceID: "t38-isolated-api"}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceGateIntegrationAutoRunGateFailureSSE(t *testing.T) {
	for _, action := range []string{"create", "edit"} {
		t.Run(action, func(t *testing.T) {
			f := newSourceGateAPI(t, true)
			auto := true
			if err := f.chats.PatchMetadata(context.Background(), f.owner, f.chat.ID, repositories.ChatMetadataPatch{AutoExecuteQuery: &auto}); err != nil {
				t.Fatal(err)
			}
			f.modelUseSQL.Store(true)
			streamID := "gate-auto-" + action
			root := "/api/chats/" + f.chat.ID.Hex()
			relay := &sourceGateErrorStream{StreamHandler: f.streamHandler, entered: make(chan struct{}), resume: make(chan struct{})}
			f.service.SetStreamHandler(relay)
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(relay.resume) }) }
			t.Cleanup(resume)
			streamCtx, stopStream := context.WithTimeout(context.Background(), 20*time.Second)
			defer stopStream()
			req, err := http.NewRequestWithContext(streamCtx, "GET", f.server.URL+root+"/stream?stream_id="+streamID, nil)
			if err != nil {
				t.Fatal(err)
			}
			token, err := f.jwt.GenerateToken(f.owner.Hex())
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+*token)
			stream, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Body.Close()
			if stream.StatusCode != http.StatusOK {
				t.Fatalf("actual SSE connection status=%d", stream.StatusCode)
			}
			method, path := "POST", root+"/messages"
			if action == "edit" {
				method, path = "PATCH", root+"/messages/"+f.userMessage.ID.Hex()
			}
			status, body := f.request(t, method, path, map[string]string{"content": "automatic mid-loop source refusal", "stream_id": streamID}, f.owner)
			if status != http.StatusOK {
				t.Fatalf("automatic %s not accepted: %d %s", action, status, body)
			}
			select {
			case <-f.modelEntered:
			case <-time.After(10 * time.Second):
				t.Fatal("automatic request did not reach the actual SDK HTTP barrier")
			}
			f.writer(t, true)
			f.setSourcePhase(t, models.ERPSourceRecovering)
			wire := f.wire.Load()
			f.releaseOnce.Do(func() { close(f.modelRelease) })
			errorPublished := false
			sourceGateEventually(t, func() bool {
				select {
				case <-relay.entered:
					errorPublished = true
					return true
				default:
					return f.writerAvailable(t)
				}
			})
			if errorPublished {
				// Publishing a terminal frame must not release this live task.
				f.writer(t, true)
			}
			resume()
			sourceGateEventually(t, func() bool { return f.writerAvailable(t) })
			if f.wire.Load() != wire || f.modelCalls.Load() != 1 {
				t.Fatalf("automatic gate refusal continued SQL/model: SQL bytes %d->%d SDK requests=%d", wire, f.wire.Load(), f.modelCalls.Load())
			}
			// EX proves all source tasks and cleanup have ended. Drain exactly the
			// events actually forwarded before asserting absence or duplicates;
			// TCP scheduling is not evidence that no terminal was published.
			expected := relay.forwarded.Load()
			scanner := bufio.NewScanner(stream.Body)
			var received, failures, otherTerminals int64
			for received < expected && scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event struct {
					Event string          `json:"event"`
					Data  json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					t.Fatal(err)
				}
				if event.Event == "connected" || event.Event == "heartbeat" {
					continue
				}
				received++
				switch event.Event {
				case "ai-response-error":
					failures++
					var data struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(event.Data, &data); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(data.Error, services.ErrSourceRecovering.Error()) {
						t.Fatalf("SSE lost the safe source refusal reason: %s", event.Data)
					}
					for _, private := range []string{f.table, f.gate.Directory(), *f.connection.Username, "SELECT id FROM"} {
						if strings.Contains(data.Error, private) {
							t.Fatal("SSE source refusal exposed source details")
						}
					}
				case "ai-response", "response-cancelled":
					otherTerminals++
				}
			}
			if received != expected {
				t.Fatalf("actual SSE wire not drained: received=%d forwarded=%d error=%v", received, expected, scanner.Err())
			}
			if failures != 1 || otherTerminals != 0 {
				t.Fatalf("automatic %s SSE terminal: failures=%d other_terminals=%d; want one safe ai-response-error after actual cleanup", action, failures, otherTerminals)
			}
			f.writer(t, false)
		})
	}
}

// The production startExecution goroutine calls this wrapper, which invokes the
// service's actual Execute. Only the scheduling boundary is controlled here.
type sourceGateRunExecutor struct {
	inner   services.AssistantExecutor
	entered chan string
	resume  chan struct{}
	calls   atomic.Int64
}

func (e *sourceGateRunExecutor) Execute(ctx context.Context, runID string) error {
	e.calls.Add(1)
	e.entered <- runID
	<-e.resume
	return e.inner.Execute(ctx, runID)
}

type sourceGateRunRepository struct {
	repositories.AssistantRunRepository
	failing       chan models.RunTransition
	write         chan struct{}
	written       chan error
	finish        chan struct{}
	running       atomic.Int64
	failAttempts  atomic.Int64
	failCommitted atomic.Int64
}

func (r *sourceGateRunRepository) Transition(ctx context.Context, transition models.RunTransition) (models.RunRecord, error) {
	if transition.To == models.RunStatusRunning {
		r.running.Add(1)
	}
	failure := transition.From == models.RunStatusCreated && transition.To == models.RunStatusFailed
	firstFailure := failure && r.failAttempts.Add(1) == 1
	if firstFailure {
		r.failing <- transition
		<-r.write
	}
	stored, err := r.AssistantRunRepository.Transition(ctx, transition)
	if failure && err == nil {
		r.failCommitted.Add(1)
	}
	if firstFailure {
		r.written <- err
		<-r.finish
	}
	return stored, err
}

type sourceGateRunSource struct {
	repositories.AssistantSourceRepository
	calls atomic.Int64
}

func (s *sourceGateRunSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	s.calls.Add(1)
	return s.AssistantSourceRepository.Capabilities(ctx)
}

func (s *sourceGateRunSource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	s.calls.Add(1)
	return s.AssistantSourceRepository.ResolveEntities(ctx, query)
}

func (s *sourceGateRunSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	s.calls.Add(1)
	return s.AssistantSourceRepository.ReadCoverage(ctx, query)
}

func (s *sourceGateRunSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.calls.Add(1)
	return s.AssistantSourceRepository.ReadSalesFacts(ctx, query)
}

func (s *sourceGateRunSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.calls.Add(1)
	return s.AssistantSourceRepository.ReadRefundFacts(ctx, query)
}

type sourceGateRunNotice struct {
	view   models.RunView
	stored models.RunRecord
	err    error
}

type sourceGateRunNotifier struct {
	runs      repositories.AssistantRunRepository
	published chan sourceGateRunNotice
	resume    chan struct{}
	calls     atomic.Int64
}

func (n *sourceGateRunNotifier) PublishRunUpdate(ctx context.Context, view models.RunView) error {
	n.calls.Add(1)
	stored, err := n.runs.GetOwned(ctx, view.OwnerID, view.ChatID, view.RunID)
	n.published <- sourceGateRunNotice{view: view, stored: stored, err: err}
	<-n.resume
	return err
}

func TestSourceGateIntegrationRunReadmissionFailure(t *testing.T) {
	for _, outcome := range []string{"failed", "cancel_wins"} {
		t.Run(outcome, func(t *testing.T) {
			f := newSourceGateAPI(t, false)
			crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := repositories.NewAESReportCipher(crypto)
			if err != nil {
				t.Fatal(err)
			}
			runs := &sourceGateRunRepository{
				AssistantRunRepository: repositories.NewAssistantRunRepository(f.database.Collection("assistant_runs"), cipher),
				failing:                make(chan models.RunTransition, 1), write: make(chan struct{}), written: make(chan error, 1), finish: make(chan struct{}),
			}
			notifier := &sourceGateRunNotifier{runs: runs.AssistantRunRepository, published: make(chan sourceGateRunNotice, 8), resume: make(chan struct{})}
			executor := &sourceGateRunExecutor{entered: make(chan string, 8), resume: make(chan struct{})}
			source := &sourceGateRunSource{AssistantSourceRepository: services.NewAssistantFixtureSource()}
			var beginOnce, writeOnce, finishOnce sync.Once
			begin := func() { beginOnce.Do(func() { close(executor.resume) }) }
			write := func() { writeOnce.Do(func() { close(runs.write) }) }
			finish := func() { finishOnce.Do(func() { close(runs.finish) }) }
			var notifyOnce sync.Once
			finishNotice := func() { notifyOnce.Do(func() { close(notifier.resume) }) }
			t.Cleanup(func() { begin(); write(); finish() })
			t.Cleanup(finishNotice)
			f.assistant = services.NewAssistantService(services.NewAssistantChatRepository(f.chats), services.NewAssistantMessageRepository(f.chats), runs, cipher,
				services.NewAssistantFixtureCatalog(), source, executor, f.policy,
				services.WithAssistantClock(services.AssistantFixtureClock), services.WithAssistantInstanceID("t38-readmission"),
				services.WithAssistantRunNotifier(notifier),
				services.WithAssistantExplanationModel(services.NewAssistantExplanationModel(f.modelClient)))
			executor.inner = f.assistant.(services.AssistantExecutor)
			f.server.Close()
			f.startAPI(t, f.service, f.policy)
			root := "/api/chats/" + f.chat.ID.Hex() + "/assistant/runs"
			requestID := "t38-readmission-" + primitive.NewObjectID().Hex()
			request := map[string]interface{}{"schema_version": 1, "request_id": requestID, "workflow": "product_analysis", "basis": "payment",
				"period": map[string]string{"start_date": "2026-09-01", "end_date_exclusive": "2026-09-08", "timezone": "Asia/Shanghai"},
				"scope":  map[string]string{"mode": "all"}, "product": map[string]string{"product_id": services.AssistantFixtureProductID}, "group_by": "shop", "metrics": []string{"quantity", "sales"}}
			status, body := f.request(t, "POST", root, request, f.owner)
			var accepted struct {
				Data dtos.AssistantRunView `json:"data"`
			}
			if status != http.StatusAccepted || json.Unmarshal(body, &accepted) != nil || accepted.Data.Status != string(models.RunStatusCreated) {
				t.Fatalf("real run not accepted as created: %d %s", status, body)
			}
			select {
			case runID := <-executor.entered:
				if runID != accepted.Data.RunID {
					t.Fatal("background executor received a different run")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("production run goroutine did not reach the Execute scheduling barrier")
			}
			load := func() models.RunRecord {
				record, err := runs.AssistantRunRepository.GetOwned(context.Background(), f.owner.Hex(), f.chat.ID.Hex(), accepted.Data.RunID)
				if err != nil {
					t.Fatal(err)
				}
				return record
			}
			created := load()
			if created.Status != models.RunStatusCreated || created.MessageID != accepted.Data.MessageID {
				t.Fatalf("run not durable before Execute: %+v", created)
			}
			messageID, err := primitive.ObjectIDFromHex(created.MessageID)
			if err != nil {
				t.Fatal(err)
			}
			message, err := f.chats.FindMessageByID(messageID)
			if err != nil || message == nil || message.ChatID != f.chat.ID || message.UserID != f.owner || message.AssistantRunID == nil || *message.AssistantRunID != created.RunID {
				t.Fatalf("real report reference not durable before Execute: %v", err)
			}
			f.writer(t, true)
			f.setSourcePhase(t, models.ERPSourceRecovering)
			wire, model := f.wire.Load(), f.modelCalls.Load()
			begin()
			var failure models.RunTransition
			failureEntered := false
			sourceGateEventually(t, func() bool {
				select {
				case failure = <-runs.failing:
					failureEntered = true
					return true
				default:
					return f.writerAvailable(t)
				}
			})
			wantStatus, wantStage, wantCommitted := models.RunStatusFailed, models.RunStageFailed, int64(1)
			if failureEntered {
				if failure.OwnerID != created.OwnerID || failure.ChatID != created.ChatID || failure.RunID != created.RunID || failure.ExpectedRevision != created.Revision {
					t.Fatal("startup failure CAS is not scoped to the accepted created revision")
				}
				if current := load(); current.Status != models.RunStatusCreated || current.Revision != created.Revision {
					t.Fatalf("run changed before the failed CAS: %+v", current)
				}
				f.writer(t, true)
				if notifier.calls.Load() != 0 {
					t.Fatal("startup refusal notification preceded the durable failure CAS")
				}
				if outcome == "cancel_wins" {
					status, body := f.request(t, "POST", root+"/"+created.RunID+"/cancel", nil, f.owner)
					if status != http.StatusOK {
						t.Fatalf("real cancellation rejected: %d %s", status, body)
					}
					cancelled := load()
					if cancelled.Status != models.RunStatusCancelled || !cancelled.CancellationMarked || cancelled.Revision != created.Revision+1 {
						t.Fatalf("cancel did not win the real Mongo CAS: %+v", cancelled)
					}
					wantStatus, wantStage, wantCommitted = models.RunStatusCancelled, models.RunStageCancelled, 0
				}
				write()
				select {
				case writeErr := <-runs.written:
					if outcome == "failed" && writeErr != nil {
						t.Fatalf("startup failure was not persisted: %v", writeErr)
					}
					if outcome == "cancel_wins" && writeErr == nil {
						t.Fatal("stale startup failure overwrote the cancellation winner")
					}
				case <-time.After(10 * time.Second):
					t.Fatal("real startup failure CAS did not return")
				}
				// Even after the actual Mongo write, the producer has not returned
				// from repository cleanup. It must still own the source lease.
				f.writer(t, true)
				finish()
			}
			if failureEntered && outcome == "failed" {
				select {
				case notice := <-notifier.published:
					if notice.err != nil || notice.view.RunID != created.RunID || notice.view.MessageID != created.MessageID || notice.view.Status != models.RunStatusFailed || notice.view.Revision != created.Revision+1 || notice.view.Report != nil || notice.view.SafeError == nil {
						t.Fatalf("startup refusal notification did not expose the persisted failure: %+v error=%v", notice.view, notice.err)
					}
					if notice.stored.Status != notice.view.Status || notice.stored.Revision != notice.view.Revision || notice.stored.SafeError == nil || *notice.stored.SafeError != *notice.view.SafeError {
						t.Fatal("startup refusal notification disagreed with the real Mongo terminal")
					}
				case <-time.After(10 * time.Second):
					t.Fatal("persisted startup failure was not notified")
				}
				f.writer(t, true)
				finishNotice()
			}
			sourceGateEventually(t, func() bool { return f.writerAvailable(t) })
			stored := load()
			if stored.Status != wantStatus || stored.Stage != wantStage || stored.Revision != created.Revision+1 {
				t.Fatalf("accepted run after real Execute refusal/cleanup: status=%s stage=%s revision=%d; want %s/%s revision=%d", stored.Status, stored.Stage, stored.Revision, wantStatus, wantStage, created.Revision+1)
			}
			if notifier.calls.Load() != wantCommitted {
				t.Fatalf("startup refusal notifications=%d; want %d committed failures", notifier.calls.Load(), wantCommitted)
			}
			if runs.failAttempts.Load() != 1 || runs.failCommitted.Load() != wantCommitted || runs.running.Load() != 0 || source.calls.Load() != 0 || f.wire.Load() != wire || f.modelCalls.Load() != model {
				t.Fatalf("startup refusal lifecycle: failed_attempts=%d failed_commits=%d running_attempts=%d source_calls=%d SQL_bytes=%d->%d SDK_requests=%d->%d",
					runs.failAttempts.Load(), runs.failCommitted.Load(), runs.running.Load(), source.calls.Load(), wire, f.wire.Load(), model, f.modelCalls.Load())
			}
			status, body = f.request(t, "GET", root+"/"+created.RunID, nil, f.owner)
			var persisted struct {
				Data dtos.AssistantRunView `json:"data"`
			}
			if status != http.StatusOK || json.Unmarshal(body, &persisted) != nil || persisted.Data.RunID != created.RunID || persisted.Data.MessageID != created.MessageID || persisted.Data.Status != string(wantStatus) || persisted.Data.Revision != stored.Revision {
				t.Fatalf("persistent HTTP GET lost the terminal run/reference: %d %s", status, body)
			}
			if outcome == "failed" {
				if stored.SafeError == nil || persisted.Data.SafeError == nil || stored.SafeError.Code != services.ErrSourceRecovering.Error() || *stored.SafeError != *persisted.Data.SafeError || stored.SafeError.Message == "" {
					t.Fatalf("persistent HTTP GET lost the safe source refusal reason: %s", body)
				}
				for _, private := range []string{f.table, f.gate.Directory(), *f.connection.Username, "SELECT id FROM"} {
					if strings.Contains(stored.SafeError.Message, private) {
						t.Fatal("persisted run failure exposed source details")
					}
				}
			} else if !stored.CancellationMarked || persisted.Data.SafeError != nil {
				t.Fatal("startup failure changed the cancelled terminal winner")
			}
			// Restore source availability so the replay reaches real idempotency,
			// rather than being refused at the request's first admission check.
			f.setSourcePhase(t, models.ERPSourceIdle)
			status, body = f.request(t, "POST", root, request, f.owner)
			var replayed struct {
				Data dtos.AssistantRunView `json:"data"`
			}
			if status != http.StatusAccepted || json.Unmarshal(body, &replayed) != nil || replayed.Data.RunID != created.RunID || replayed.Data.MessageID != created.MessageID || replayed.Data.Status != string(wantStatus) {
				t.Fatalf("same request_id did not replay the same terminal run: %d %s", status, body)
			}
			sourceGateEventually(t, func() bool { return f.writerAvailable(t) })
			if current := load(); current.Status != stored.Status || current.Revision != stored.Revision {
				t.Fatal("request replay changed the stored terminal run")
			}
			if executor.calls.Load() != 1 || runs.failAttempts.Load() != 1 || source.calls.Load() != 0 || f.modelCalls.Load() != model || f.wire.Load() != wire {
				t.Fatal("same request_id restarted background execution or source/model work")
			}
			if count, err := f.database.Collection("assistant_runs").CountDocuments(context.Background(), bson.M{"owner_id": f.owner.Hex(), "chat_id": f.chat.ID.Hex(), "request_id": requestID}); err != nil || count != 1 {
				t.Fatalf("request replay created another persistent run: count=%d error=%v", count, err)
			}
			if notifier.calls.Load() != wantCommitted {
				t.Fatal("request replay published another failure notification")
			}
			f.writer(t, false)
		})
	}
}

const sourceGateFinalQueryContent = "source gate executable final queries"

type sourceGateFinalStream struct {
	*sourceGateErrorStream
	step     string
	boundary chan struct{}
	proceed  chan struct{}
	once     sync.Once
}

func (s *sourceGateFinalStream) HandleStreamEvent(owner, chat, stream string, event dtos.StreamResponse) {
	s.sourceGateErrorStream.HandleStreamEvent(owner, chat, stream, event)
	if step, ok := event.Data.(string); event.Event == "ai-response-step" && ok && step == s.step {
		s.once.Do(func() {
			close(s.boundary)
			<-s.proceed
		})
	}
}

type sourceGateAdmissionReads struct {
	repositories.ChatRepository
	reads atomic.Int64
}

func (r *sourceGateAdmissionReads) ReadCurrent(ctx context.Context, owner, chat primitive.ObjectID) (*models.Chat, error) {
	r.reads.Add(1)
	return r.ChatRepository.ReadCurrent(ctx, owner, chat)
}

func TestSourceGateIntegrationFinalQueryGateFailureSSE(t *testing.T) {
	for _, phase := range []string{"sql", "chart"} {
		for _, action := range []string{"create", "edit"} {
			t.Run(phase+"/"+action, func(t *testing.T) {
				f := newSourceGateAPI(t, true)
				admissions := &sourceGateAdmissionReads{ChatRepository: f.chats}
				policy, err := services.NewSourceAccessService(f.registry, f.gate, config.ERPSyncConfig{
					InstanceID: "t38-isolated-api", DeploymentUID: uint32(os.Getuid()), LockDirectory: f.gate.Directory(),
					Associations: []config.ERPAssociation{{SourceID: f.source.SourceID, EndpointFingerprints: f.source.ApprovedEndpointAliases, ApprovalRef: "isolated-only"}},
				}, admissions)
				if err != nil {
					t.Fatal(err)
				}
				f.replacePolicy(t, policy)
				auto, chart := true, phase == "chart"
				if err := f.chats.PatchMetadata(context.Background(), f.owner, f.chat.ID, repositories.ChatMetadataPatch{AutoExecuteQuery: &auto, AutoGenerateVisualization: &chart}); err != nil {
					t.Fatal(err)
				}
				f.modelFinalQueries.Store(true)
				step := "Combining & structuring the response"
				if chart {
					step = "Generating visualization for the result"
				}
				relay := &sourceGateFinalStream{
					sourceGateErrorStream: &sourceGateErrorStream{StreamHandler: f.streamHandler, entered: make(chan struct{}), resume: make(chan struct{})},
					step:                  step, boundary: make(chan struct{}), proceed: make(chan struct{}),
				}
				f.service.SetStreamHandler(relay)
				var proceedOnce, errorOnce sync.Once
				proceed := func() { proceedOnce.Do(func() { close(relay.proceed) }) }
				finishError := func() { errorOnce.Do(func() { close(relay.resume) }) }
				t.Cleanup(func() { proceed(); finishError() })
				streamID := "gate-final-" + phase + "-" + action
				root := "/api/chats/" + f.chat.ID.Hex()
				streamCtx, stopStream := context.WithTimeout(context.Background(), 30*time.Second)
				defer stopStream()
				req, err := http.NewRequestWithContext(streamCtx, "GET", f.server.URL+root+"/stream?stream_id="+streamID, nil)
				if err != nil {
					t.Fatal(err)
				}
				token, err := f.jwt.GenerateToken(f.owner.Hex())
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+*token)
				stream, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Body.Close()
				if stream.StatusCode != http.StatusOK {
					t.Fatalf("actual final-query SSE status=%d", stream.StatusCode)
				}
				method, path := "POST", root+"/messages"
				if action == "edit" {
					method, path = "PATCH", root+"/messages/"+f.userMessage.ID.Hex()
				}
				status, body := f.request(t, method, path, map[string]string{"content": "execute final queries through the real automatic root", "stream_id": streamID}, f.owner)
				var accepted struct {
					Data dtos.MessageResponse `json:"data"`
				}
				if status != http.StatusOK || json.Unmarshal(body, &accepted) != nil || accepted.Data.ID == "" {
					t.Fatalf("final-query %s not accepted: %d %s", action, status, body)
				}
				if action == "edit" && (accepted.Data.ID != f.userMessage.ID.Hex() || !accepted.Data.IsEdited) {
					t.Fatal("final-query edit did not use the real existing user message")
				}
				select {
				case <-f.modelEntered:
				case <-time.After(10 * time.Second):
					t.Fatal("final-query request did not reach actual SDK HTTP")
				}
				modelWire := f.wire.Load()
				f.writer(t, true)
				f.releaseOnce.Do(func() { close(f.modelRelease) })
				select {
				case <-relay.boundary:
				case <-time.After(10 * time.Second):
					t.Fatal("real automatic root did not reach its final SQL/chart boundary")
				}
				var saved *models.Message
				sourceGateEventually(t, func() bool {
					messages, _, err := f.chats.FindMessagesByChat(f.chat.ID, 1, 20)
					if err != nil {
						t.Fatal(err)
					}
					for _, message := range messages {
						if message.Content == sourceGateFinalQueryContent && message.Type == "assistant" {
							saved = message
							return !chart || (saved.Queries != nil && len(*saved.Queries) == 2 && (*saved.Queries)[0].IsExecuted)
						}
					}
					return false
				})
				if saved.UserMessageId == nil || saved.UserMessageId.Hex() != accepted.Data.ID || saved.Queries == nil || len(*saved.Queries) != 2 {
					t.Fatal("SDK final queries were not really saved for the accepted user message")
				}
				first, next := (*saved.Queries)[0], (*saved.Queries)[1]
				if first.Query != `SELECT id, label FROM public."`+f.table+`" ORDER BY id` || first.QueryType == nil || *first.QueryType != "SELECT" || first.IsCritical || next.IsCritical || next.IsExecuted {
					t.Fatal("final SDK queries did not preserve their executable SELECT contract")
				}
				if chart {
					if f.wire.Load() <= modelWire || first.ExecutionResult == nil || first.Error != nil {
						t.Fatal("chart boundary was reached without a successful real source SQL result")
					}
					crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
					if err != nil {
						t.Fatal(err)
					}
					plain, err := crypto.DecryptField(*first.ExecutionResult)
					if err != nil || !strings.Contains(plain, "persisted source row") {
						t.Fatal("chart boundary did not retain the genuine persisted source row")
					}
				} else if first.IsExecuted || first.ExecutionResult != nil || f.wire.Load() != modelWire {
					t.Fatal("SQL ran before the final-query admission barrier")
				}
				f.writer(t, true)
				f.setSourcePhase(t, models.ERPSourceRecovering)
				wire, reads := f.wire.Load(), admissions.reads.Load()
				proceed()
				errorPublished := false
				sourceGateEventually(t, func() bool {
					select {
					case <-relay.entered:
						errorPublished = true
						return true
					default:
						return f.writerAvailable(t)
					}
				})
				if errorPublished {
					f.writer(t, true)
				}
				finishError()
				sourceGateEventually(t, func() bool { return f.writerAvailable(t) })
				if f.wire.Load() != wire || f.modelCalls.Load() != 1 {
					t.Errorf("final %s refusal continued SQL/model: bytes %d->%d SDK requests=%d", phase, wire, f.wire.Load(), f.modelCalls.Load())
				}
				if admissions.reads.Load() != reads+1 {
					t.Errorf("final %s refusal continued another source admission/query: reads %d->%d; want one refused attempt", phase, reads, admissions.reads.Load())
				}
				expected := relay.forwarded.Load()
				scanner := bufio.NewScanner(stream.Body)
				var received, failures, otherTerminals int64
				for received < expected && scanner.Scan() {
					line := scanner.Text()
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event struct {
						Event string          `json:"event"`
						Data  json.RawMessage `json:"data"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
						t.Fatal(err)
					}
					if event.Event == "connected" || event.Event == "heartbeat" {
						continue
					}
					received++
					switch event.Event {
					case "ai-response-error":
						failures++
						var data struct {
							Error string `json:"error"`
						}
						if err := json.Unmarshal(event.Data, &data); err != nil {
							t.Fatal(err)
						}
						if !strings.Contains(data.Error, services.ErrSourceRecovering.Error()) {
							t.Fatalf("final-query SSE lost the specific safe refusal: %s", event.Data)
						}
						for _, private := range []string{f.table, f.gate.Directory(), *f.connection.Username, "SELECT id"} {
							if strings.Contains(data.Error, private) {
								t.Fatal("final-query refusal exposed source details")
							}
						}
					case "ai-response", "response-cancelled":
						otherTerminals++
					}
				}
				if received != expected {
					t.Fatalf("final-query actual SSE wire not drained: received=%d forwarded=%d error=%v", received, expected, scanner.Err())
				}
				if failures != 1 || otherTerminals != 0 {
					t.Fatalf("final %s/%s SSE terminal: failures=%d other_terminals=%d; want one specific safe error", phase, action, failures, otherTerminals)
				}
				f.writer(t, false)
			})
		}
	}
}

// Real PG collection followed by a blocked, real Mongo persistence terminal.
// The wrapper observes lifecycle only; it delegates the actual schema write.
type sourceGateSchemaWriteBarrier struct {
	repositories.ChatRepository
	entered chan string
	release chan struct{}
}

func (r *sourceGateSchemaWriteBarrier) UpdateConnectionSchema(ctx context.Context, chat *models.Chat, schema string) error {
	r.entered <- schema
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.ChatRepository.UpdateConnectionSchema(ctx, chat, schema)
}
func TestSourceGateIntegrationStrictDetachedSchemaRefresh(t *testing.T) {
	f := newSourceGateAPI(t, false)
	barrier := &sourceGateSchemaWriteBarrier{ChatRepository: f.chats, entered: make(chan string, 1), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(barrier.release) })
	wrapper := &mongodb.MongoDBClient{Client: f.mongo, Config: mongodb.MongoDbConfigModel{DatabaseName: f.database.Name()}}
	service := services.NewChatService(barrier, f.manager, f.modelClient, f.modelManager, f.redis, repositories.NewVisualizationRepository(wrapper, f.redis), nil, repositories.NewKnowledgeBaseRepository(wrapper), f.dashboards, f.assistant, f.policy)
	f.startAPI(t, service, f.policy)
	if _, err := f.control.Exec(`ALTER TABLE public."` + f.table + `" ADD COLUMN strict_refresh_marker text`); err != nil {
		t.Fatal(err)
	}
	beforeModel, wire := f.modelCalls.Load(), f.wire.Load()
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, "POST", f.server.URL+"/api/chats/"+f.chat.ID.Hex()+"/refresh-schema", nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.jwt.GenerateToken(f.owner.Hex())
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+*token)
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("refresh status=%d", response.StatusCode)
	}
	cancel()
	select {
	case schema := <-barrier.entered:
		if !strings.Contains(schema, "strict_refresh_marker") {
			t.Fatal("actual new catalog column missing")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("strict catalog did not reach persistence after HTTP cancel")
	}
	f.writer(t, true)
	release.Do(func() { close(barrier.release) })
	sourceGateEventually(t, func() bool {
		lease, err := f.gate.Acquire(context.Background(), sourcegate.Target{SourceID: f.source.SourceID, Device: f.source.LockDevice, Inode: f.source.LockInode}, sourcegate.Exclusive, sourcegate.Identity{SourceID: f.source.SourceID, OperationID: uuid.NewString()})
		if lease == nil {
			if !errors.Is(err, sourcegate.ErrBusy) {
				t.Fatal(err)
			}
			return false
		}
		lease.Close()
		return true
	})
	stored, err := f.chats.ReadCurrent(context.Background(), f.owner, f.chat.ID)
	if err != nil || stored.Connection.CurrentSchema == nil || !strings.Contains(*stored.Connection.CurrentSchema, "strict_refresh_marker") {
		t.Fatalf("strict schema not persisted: %v", err)
	}
	if f.modelCalls.Load() != beforeModel || f.wire.Load() <= wire {
		t.Fatal("strict refresh must collect actual catalog without model enhancement")
	}
}
