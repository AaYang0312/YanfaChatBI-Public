package services

import (
	"context"
	"fmt"
	"log"
	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/schemaobjects"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Used by Handler
type StreamHandler interface {
	HandleStreamEvent(userID, chatID, streamID string, response dtos.StreamResponse)
	HasStream(userID, chatID, streamID string) bool
}

type ChatService interface {
	SetStreamHandler(handler StreamHandler)
	// SetModelProviderService wires the admin-configured model catalogue used to
	// resolve "slug/model" identifiers into provider clients.
	SetModelProviderService(providers *ModelProviderService)
	// SetContextSummaryRepository wires the conversation summary store.
	SetContextSummaryRepository(repo repositories.ContextSummaryRepository)
	// GetContextStatus reports compaction state for one chat.
	GetContextStatus(ctx context.Context, userID, chatID string) (*dtos.ContextStatusResponse, uint32, error)

	// CRUD operations
	Create(userID string, req *dtos.CreateChatRequest) (*dtos.ChatResponse, uint32, error)
	CreateWithoutConnectionPing(userID string, req *dtos.CreateChatRequest) (*dtos.ChatResponse, uint32, error)
	Update(userID, chatID string, req *dtos.UpdateChatRequest) (*dtos.ChatResponse, uint32, error)
	Delete(userID, chatID string) (uint32, error)
	GetByID(userID, chatID string) (*dtos.ChatResponse, uint32, error)
	List(userID string, page, pageSize int) (*dtos.ChatListResponse, uint32, error)
	CreateMessage(ctx context.Context, userID, chatID string, streamID string, content string, llmModel string) (*dtos.MessageResponse, uint16, error)
	UpdateMessage(ctx context.Context, userID, chatID, messageID string, streamID string, req *dtos.CreateMessageRequest) (*dtos.MessageResponse, uint32, error)
	DeleteMessages(userID, chatID string) (uint32, error)
	Duplicate(userID, chatID string, duplicateMessages bool, duplicateDashboards bool, selectedCollections ...*string) (*dtos.ChatResponse, uint32, error)
	ListMessages(userID, chatID string, page, pageSize int) (*dtos.MessageListResponse, uint32, error)
	PinMessage(userID, chatID, messageID string) (interface{}, uint32, error)
	UnpinMessage(userID, chatID, messageID string) (interface{}, uint32, error)
	ListPinnedMessages(userID, chatID string) (*dtos.MessageListResponse, uint32, error)
	EditQuery(ctx context.Context, userID, chatID, messageID, queryID string, query string) (*dtos.EditQueryResponse, uint32, error)
	GetDBConnectionStatus(ctx context.Context, userID, chatID string) (*dtos.ConnectionStatusResponse, uint32, error)
	HandleSchemaChange(ctx context.Context, userID, chatID, streamID string, diff interface{}) error
	HandleDBEvent(userID, chatID, streamID string, response dtos.StreamResponse)
	GetAllTables(ctx context.Context, userID, chatID string) (*dtos.TablesResponse, uint32, error)
	GetSelectedCollections(chatID string) (string, error)

	// Execution operations
	CancelProcessing(userID, chatID, streamID string)
	ConnectDB(ctx context.Context, userID, chatID string, streamID string) (uint32, error)
	DisconnectDB(ctx context.Context, userID, chatID string, streamID string) (uint32, error)
	ExecuteQuery(ctx context.Context, userID, chatID string, req *dtos.ExecuteQueryRequest) (*dtos.QueryExecutionResponse, uint32, error)
	RollbackQuery(ctx context.Context, userID, chatID string, req *dtos.RollbackQueryRequest) (*dtos.QueryExecutionResponse, uint32, error)
	CancelQueryExecution(userID, chatID, messageID, queryID, streamID string)
	processMessage(ctx context.Context, userID, chatID string, messageID, streamID string) error
	processLLMResponseAndRunQuery(ctx context.Context, userID, chatID string, messageID, streamID string) error

	// Spreadsheet operations
	StoreSpreadsheetData(userID, chatID, tableName string, columns []string, data [][]string, mergeStrategy string, mergeOptions MergeOptions) (*dtos.SpreadsheetUploadResponse, uint32, error)
	ProcessAndStoreSpreadsheetUnified(userID, chatID, tableName string, data [][]interface{}, mergeStrategy string, mergeOptions MergeOptions) (*dtos.SpreadsheetUploadResponse, uint32, error)
	GetSpreadsheetTableData(userID, chatID, tableName string, page, pageSize int) (*dtos.SpreadsheetTableDataResponse, uint32, error)
	DeleteSpreadsheetTable(userID, chatID, tableName string) (uint32, error)
	DeleteSpreadsheetRow(userID, chatID, tableName string, rowID string) (uint32, error)
	DownloadSpreadsheetTableData(userID, chatID, tableName string) (*dtos.SpreadsheetDownloadResponse, uint32, error)
	DownloadSpreadsheetTableDataWithFilter(userID, chatID, tableName string, rowIDs []string) (*dtos.SpreadsheetDownloadResponse, uint32, error)

	RefreshSchema(ctx context.Context, userID, chatID string, sync bool) (uint32, error)
	GetQueryResults(ctx context.Context, userID, chatID, messageID, queryID, streamID string, offset int, cursor *string) (*dtos.QueryResultsResponse, uint32, error)
	GetQueryRecommendations(ctx context.Context, userID, chatID string, streamID string) (*dtos.QueryRecommendationsResponse, uint32, error)
	GetImportMetadata(ctx context.Context, userID, chatID string) (*dtos.ImportMetadata, uint32, error)

	// Visualization operations
	GenerateVisualizationForQueryResults(ctx context.Context, userID, chatID string, chat *models.Chat, selectedLLMModel, userQuestion string, executedQueries []interface{}, queryResults []map[string]interface{}, isExplicitRequest bool) (*dtos.VisualizationResponse, error)
	GenerateVisualizationForMessage(ctx context.Context, userID, chatID, messageID, queryID string, selectedLLMModel string) (*dtos.VisualizationResponse, error) // New: Fetch message data and generate visualization
	SaveVisualizationToMessage(ctx context.Context, messageID, chatID, userID string, visualization *dtos.VisualizationResponse, queryID string) (string, error)
	GetVisualizationForQuery(ctx context.Context, queryID string) (*dtos.VisualizationResponse, error)                           // Get visualization by query ID (per-query visualization)
	GetVisualizationData(ctx context.Context, userID, chatID, messageID, queryID string, limit, offset int) (interface{}, error) // Lazy-load visualization data on demand
	ExecuteChartQuery(ctx context.Context, userID, chatID string, chartConfig *dtos.ChartConfiguration, limit int) ([]map[string]interface{}, error)

	// Knowledge Base operations
	GetKnowledgeBase(ctx context.Context, userID, chatID string) (*models.KnowledgeBase, uint32, error)
	UpdateKnowledgeBase(ctx context.Context, userID, chatID string, tableDescs []models.TableDescription) (*models.KnowledgeBase, uint32, error)
}

type chatService struct {
	chatRepo           repositories.ChatRepository
	visualizationRepo  repositories.IVisualizationRepository
	dbManager          *dbmanager.Manager
	llmClient          llm.Client
	llmManager         *llm.Manager // Added to support multiple LLM providers
	streamChans        map[string]chan dtos.StreamResponse
	streamHandler      StreamHandler
	activeProcesses    map[string]context.CancelFunc // key: owner/Chat/stream
	processesMu        sync.RWMutex
	queryCancellations map[string]*queryCancellation
	crypto             *utils.AESGCMCrypto
	redisRepo          redis.IRedisRepositories
	vectorizationSvc   VectorizationService                 // RAG pipeline — can be nil if unavailable
	kbRepo             repositories.KnowledgeBaseRepository // Knowledge base persistence
	dashboardRepo      repositories.DashboardRepository     // Dashboard persistence for duplication
	assistant          AssistantService                     // Business tools share this service; nil fails closed
	sourceAccess       *SourceAccessService
	objectState        repositories.ObjectStateRepository
	modelProviders     *ModelProviderService
	contextSummaries   repositories.ContextSummaryRepository
}

func isValidDBType(dbType string) bool {
	validTypes := []string{
		constants.DatabaseTypePostgreSQL,
		constants.DatabaseTypeYugabyteDB,
		constants.DatabaseTypeMySQL,
		constants.DatabaseTypeClickhouse,
		constants.DatabaseTypeMongoDB,
		constants.DatabaseTypeRedis,
		constants.DatabaseTypeNeo4j,
		constants.DatabaseTypeSpreadsheet,
		constants.DatabaseTypeGoogleSheets,
		constants.DatabaseTypeTimescaleDB,
		constants.DatabaseTypeStarRocks,
	}

	for _, validType := range validTypes {
		if dbType == validType {
			return true
		}
	}

	return false
}

func (s *chatService) SetStreamHandler(handler StreamHandler) {
	s.streamHandler = handler
}

// Helper method to send stream events
func (s *chatService) sendStreamEvent(userID, chatID, streamID string, response dtos.StreamResponse) {
	log.Printf("sendStreamEvent -> userID: %s, chatID: %s, streamID: %s", userID, chatID, streamID)
	if s.streamHandler != nil {
		s.streamHandler.HandleStreamEvent(userID, chatID, streamID, response)
	} else {
		log.Printf("sendStreamEvent -> no stream handler set")
	}
}

// Add method to handle DB status events
func (s *chatService) HandleDBEvent(userID, chatID, streamID string, response dtos.StreamResponse) {
	// Send to stream handler
	log.Printf("ChatService -> HandleDBEvent -> response: %+v", response)
	if s.streamHandler != nil {
		s.streamHandler.HandleStreamEvent(userID, chatID, streamID, response)
	}
}

func NewChatService(
	chatRepo repositories.ChatRepository,
	dbManager *dbmanager.Manager,
	llmClient llm.Client,
	llmManager *llm.Manager,
	redisRepo redis.IRedisRepositories,
	visualizationRepo repositories.IVisualizationRepository,
	vectorizationSvc VectorizationService,
	kbRepo repositories.KnowledgeBaseRepository,
	dashboardRepo repositories.DashboardRepository,
	assistant AssistantService,
	sourceAccess *SourceAccessService,
) ChatService {
	// Initialize crypto instance
	crypto, err := utils.NewFromConfig()
	if err != nil {
		log.Printf("ChatService -> NewChatService -> Failed to initialize crypto: %v", err)
		// Continue without crypto for backward compatibility
	}

	if sourceAccess == nil {
		panic("source access policy is required")
	}
	dbManager.SetSourceAccess(sourceAccess)
	objectState, _ := chatRepo.(repositories.ObjectStateRepository)
	return &chatService{
		chatRepo:          chatRepo,
		visualizationRepo: visualizationRepo,
		dbManager:         dbManager,
		llmClient:         llmClient,
		llmManager:        llmManager,
		streamChans:       make(map[string]chan dtos.StreamResponse),
		activeProcesses:   make(map[string]context.CancelFunc),
		crypto:            crypto,
		redisRepo:         redisRepo,
		vectorizationSvc:  vectorizationSvc,
		kbRepo:            kbRepo,
		dashboardRepo:     dashboardRepo,
		assistant:         assistant,
		sourceAccess:      sourceAccess,
		objectState:       objectState,
	}
}

// encryptQueryResult encrypts a query result for storage
func (s *chatService) encryptQueryResult(result string) string {
	if s.crypto == nil || result == "" {
		return result
	}

	encrypted, err := s.crypto.EncryptField(result)
	if err != nil {
		log.Printf("ChatService -> encryptQueryResult -> Failed to encrypt: %v", err)
		return result // Return unencrypted for backward compatibility
	}

	return encrypted
}

// decryptQueryResult decrypts a query result from storage
func (s *chatService) decryptQueryResult(result string) string {
	if s.crypto == nil || result == "" {
		return result
	}

	decrypted, err := s.crypto.DecryptField(result)
	if err != nil {
		log.Printf("ChatService -> decryptQueryResult -> Failed to decrypt: %v", err)
		return result // Return as-is for backward compatibility
	}

	return decrypted
}

func (s *chatService) Create(userID string, req *dtos.CreateChatRequest) (*dtos.ChatResponse, uint32, error) {
	return s.createChat(userID, req, true)
}

func (s *chatService) createChat(userID string, req *dtos.CreateChatRequest, ping bool) (*dtos.ChatResponse, uint32, error) {
	log.Printf("Creating chat for user %s", userID)
	objects, selected, _, err := prepareObjectSelection(nil, req.SelectedObjects, req.SelectedCollections, nil)
	if req.SelectedObjects != nil && (!config.Env.QueryObjectsEnabled || req.Connection.Type != constants.DatabaseTypePostgreSQL) {
		return nil, http.StatusConflict, ErrQueryObjectsDisabled
	}
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	// If 0, means trial mode, so user cannot create more than 1 chat
	if config.Env.MaxChatsPerUser == 0 {
		// Apply check that single user cannot have more than 1 chat
		userObjID, err := primitive.ObjectIDFromHex(userID)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
		}
		chats, _, err := s.chatRepo.FindByUserID(userObjID, 1, 3) // Trying to fetch 3 chats
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
		}
		if len(chats) >= 2 {
			return nil, http.StatusBadRequest, fmt.Errorf("You cannot have more than 2 chats in trial mode")
		}
	}

	// Validate database type
	if !isValidDBType(req.Connection.Type) {
		return nil, http.StatusBadRequest, fmt.Errorf("Unsupported data source type: %s", req.Connection.Type)
	}

	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	// Create connection object with SSL configuration
	connection := models.Connection{
		Type: req.Connection.Type,
		Base: models.NewBase(),
	}

	// For spreadsheet and Google Sheets connections, we store placeholder values
	if req.Connection.Type == constants.DatabaseTypeSpreadsheet || req.Connection.Type == constants.DatabaseTypeGoogleSheets {
		// Set minimal required fields for spreadsheet
		connection.IsExampleDB = false
		// Store placeholder values - these will be replaced with real credentials when connecting
		if req.Connection.Type == constants.DatabaseTypeGoogleSheets {
			connection.Host = "google-sheets"
			connection.GoogleSheetID = req.Connection.GoogleSheetID
			connection.GoogleSheetURL = req.Connection.GoogleSheetURL
			connection.GoogleAuthToken = req.Connection.GoogleAuthToken
			connection.GoogleRefreshToken = req.Connection.GoogleRefreshToken
			// Use the database name from the request or set a default
			if req.Connection.Database != "" {
				connection.Database = req.Connection.Database
			} else {
				connection.Database = "google_sheets_db"
			}
		} else {
			connection.Host = "internal-spreadsheet"
			connection.Database = "spreadsheet_db"
		}
		// Set placeholder username and password
		placeholderUsername := "spreadsheet_user"
		placeholderPassword := "internal"
		placeholderPort := "0"
		connection.Username = &placeholderUsername
		connection.Password = &placeholderPassword
		connection.Port = &placeholderPort
	} else {
		// For traditional database connections
		connection.Host = req.Connection.Host
		connection.Port = req.Connection.Port
		connection.Username = &req.Connection.Username
		connection.Password = req.Connection.Password
		connection.Database = req.Connection.Database
		connection.AuthDatabase = req.Connection.AuthDatabase
		connection.UseSSL = req.Connection.UseSSL
		connection.SSLMode = req.Connection.SSLMode
		connection.SSLCertURL = req.Connection.SSLCertURL
		connection.SSLKeyURL = req.Connection.SSLKeyURL
		connection.SSLRootCertURL = req.Connection.SSLRootCertURL
	}
	if !ping && connection.Type != constants.DatabaseTypeSpreadsheet && connection.Type != constants.DatabaseTypeGoogleSheets {
		connection.IsExampleDB = true
	}
	plaintext := connection.Clone()
	connection = connection.Clone()

	// Encrypt connection details
	if err := utils.EncryptConnection(&connection); err != nil {
		log.Printf("Warning: Failed to encrypt connection details: %v", err)
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to secure connection details: %v", err)
	}

	settings := models.DefaultChatSettings()
	if req.Settings.AutoExecuteQuery != nil {
		settings.AutoExecuteQuery = *req.Settings.AutoExecuteQuery
	}
	if req.Settings.NonTechMode != nil {
		settings.NonTechMode = *req.Settings.NonTechMode
	}
	if req.Settings.AutoGenerateVisualization != nil {
		settings.AutoGenerateVisualization = *req.Settings.AutoGenerateVisualization
	}
	if req.Settings.AutoAnalyzeResults != nil {
		settings.AutoAnalyzeResults = *req.Settings.AutoAnalyzeResults
	}
	log.Printf("ChatService -> Create -> Creating chat with settings: AutoExecuteQuery=%v, NonTechMode=%v, AutoGenerateVisualization=%v",
		settings.AutoExecuteQuery, settings.NonTechMode, settings.AutoGenerateVisualization)
	// Create chat with connection
	chat := models.NewChat(userObjID, connection, settings)
	chat.SelectedCollections = selected
	chat.SelectedObjects = objects
	if objects != nil {
		chat.SelectionRevision = 1
	}
	if req.DisplayName != nil {
		chat.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if err := s.persistNewChat(context.Background(), chat, plaintext, ping); err != nil {
		return nil, http.StatusBadRequest, err
	}
	return s.buildChatResponse(chat), http.StatusCreated, nil
}

// Create a new chat without connection ping
func (s *chatService) CreateWithoutConnectionPing(userID string, req *dtos.CreateChatRequest) (*dtos.ChatResponse, uint32, error) {
	return s.createChat(userID, req, false)
}

// Update a chat details such as connection, selected collections, auto execute query flag
func (s *chatService) Update(userID, chatID string, req *dtos.UpdateChatRequest) (*dtos.ChatResponse, uint32, error) {
	ctx := context.Background()
	owner, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	id, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	previous, err := s.chatRepo.FindByID(id)
	if err != nil || previous == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if previous.UserID != owner {
		return nil, http.StatusForbidden, ErrSourceForbidden
	}
	next := *previous
	connection := previous.Connection.Clone()
	utils.DecryptConnection(&connection)
	bindingChanged := false
	if req.Connection != nil {
		if !isValidDBType(req.Connection.Type) {
			return nil, http.StatusBadRequest, fmt.Errorf("unsupported data source type")
		}
		oldConnection := connection
		connection.Type, connection.Host, connection.Port = req.Connection.Type, req.Connection.Host, req.Connection.Port
		connection.Username = &req.Connection.Username
		if req.Connection.Password != nil {
			connection.Password = req.Connection.Password
		}
		connection.Database, connection.AuthDatabase = req.Connection.Database, req.Connection.AuthDatabase
		connection.UseSSL, connection.SSLMode = req.Connection.UseSSL, req.Connection.SSLMode
		connection.SSLCertURL, connection.SSLKeyURL, connection.SSLRootCertURL = req.Connection.SSLCertURL, req.Connection.SSLKeyURL, req.Connection.SSLRootCertURL
		bindingChanged = !reflect.DeepEqual(oldConnection, connection)
	}
	if req.SelectedObjects != nil || previous.SelectedObjects != nil {
		return s.updateQueryObjectChat(ctx, owner, previous, connection, bindingChanged, req)
	}
	if req.SelectionRevision != nil && *req.SelectionRevision != previous.SelectionRevision {
		return nil, http.StatusConflict, ErrSelectionConflict
	}
	patch := repositories.ChatMetadataPatch{DisplayName: req.DisplayName, SelectedCollections: req.SelectedCollections, PreferredLLMModel: req.PreferredLLMModel, PreferredReasoningLevel: req.PreferredReasoningLevel}
	if bindingChanged && patch.SelectedCollections == nil {
		selected := ""
		patch.SelectedCollections = &selected
	}
	if patch.DisplayName != nil {
		name := strings.TrimSpace(*patch.DisplayName)
		patch.DisplayName = &name
	}
	if req.Settings != nil {
		patch.AutoExecuteQuery = req.Settings.AutoExecuteQuery
		patch.NonTechMode, patch.AutoGenerateVisualization = req.Settings.NonTechMode, req.Settings.AutoGenerateVisualization
		patch.AutoAnalyzeResults = req.Settings.AutoAnalyzeResults
	}
	classification, _ := s.sourcePolicy().Classify(previous, connection)
	if bindingChanged {
		next.Connection = connection.Clone()
		if err := utils.EncryptConnection(&next.Connection); err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if classification == models.SourceIndependent {
			if err := s.probeConnection(ctx, previous, connection); err != nil {
				return nil, http.StatusBadRequest, err
			}
		}
	}
	if !bindingChanged {
		if err := s.chatRepo.PatchMetadata(ctx, owner, id, patch); err != nil {
			return nil, http.StatusInternalServerError, err
		}
	}
	if bindingChanged {
		// The target selection (including the legacy default on a new connection)
		// becomes visible only with the connection/binding CAS. A failed CAS must
		// leave the old connection's selection untouched.
		next.SelectedCollections = *patch.SelectedCollections
		next.SelectionRevision = previous.SelectionRevision + 1
		patch.SelectedCollections = nil
		if classification != models.SourceIndependent {
			if _, err := s.sourcePolicy().ChangeBinding(ctx, owner, previous, &next, connection, s.chatRepo, &chatBindingEffects{service: s, owner: owner, chatID: id, probe: true, refresh: true, metadata: &patch}); err != nil {
				return nil, http.StatusConflict, err
			}
		} else {
			store, ok := s.chatRepo.(repositories.LegacyConnectionRepository)
			if !ok {
				return nil, http.StatusServiceUnavailable, fmt.Errorf("atomic legacy connection repository unavailable")
			}
			if err := store.UpdateIndependentLegacyConnectionCAS(ctx, previous, &next); err != nil {
				return nil, http.StatusConflict, err
			}
			if err := s.chatRepo.PatchMetadata(ctx, owner, id, patch); err != nil {
				return nil, http.StatusInternalServerError, err
			}
			if _, exists := s.dbManager.GetConnectionInfo(chatID); exists {
				if err := s.dbManager.Disconnect(chatID, userID, false); err != nil {
					return nil, http.StatusInternalServerError, err
				}
			}
		}
	}
	chat, err := s.chatRepo.ReadCurrent(ctx, owner, id)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if !bindingChanged && req.SelectedCollections != nil && *req.SelectedCollections != previous.SelectedCollections {
		if _, err := s.RefreshSchema(ctx, userID, chatID, false); err != nil {
			return nil, http.StatusConflict, err
		}
	}
	return s.buildChatResponse(chat), http.StatusOK, nil
}

func (s *chatService) sourcePolicy() *SourceAccessService {
	return s.sourceAccess
}

func sourceProcessKey(ownerID, chatID, streamID string) string {
	return ownerID + ":" + chatID + ":" + streamID
}

type queryCancellation struct{ cancel context.CancelFunc }

func queryRootKey(owner, chat, message, query, stream string) string {
	return sourceProcessKey(owner, chat, stream) + ":" + message + ":" + query
}

func (s *chatService) trackQueryRoot(ctx context.Context, owner, chat, message, query, stream string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	entry := &queryCancellation{cancel: cancel}
	key := queryRootKey(owner, chat, message, query, stream)
	s.processesMu.Lock()
	if s.queryCancellations == nil {
		s.queryCancellations = make(map[string]*queryCancellation)
	}
	s.queryCancellations[key] = entry
	s.processesMu.Unlock()
	return ctx, func() {
		cancel()
		s.processesMu.Lock()
		if s.queryCancellations[key] == entry {
			delete(s.queryCancellations, key)
		}
		s.processesMu.Unlock()
	}
}

func connectionConfig(chat *models.Chat, connection models.Connection) dbmanager.ConnectionConfig {
	return dbmanager.ConnectionConfig{SourceChat: chat, ChatID: chat.ID.Hex(), Type: connection.Type, Host: connection.Host,
		Port: connection.Port, Username: connection.Username, Password: connection.Password, Database: connection.Database,
		AuthDatabase: connection.AuthDatabase, UseSSL: connection.UseSSL, SSLMode: connection.SSLMode,
		SSLCertURL: connection.SSLCertURL, SSLKeyURL: connection.SSLKeyURL, SSLRootCertURL: connection.SSLRootCertURL,
		SSHEnabled: connection.SSHEnabled, SSHHost: connection.SSHHost, SSHPort: connection.SSHPort, SSHUsername: connection.SSHUsername,
		SSHAuthMethod: connection.SSHAuthMethod, SSHPrivateKey: connection.SSHPrivateKey, SSHPrivateKeyURL: connection.SSHPrivateKeyURL,
		SSHPassphrase: connection.SSHPassphrase, SSHPassword: connection.SSHPassword,
		GoogleSheetID: connection.GoogleSheetID, GoogleAuthToken: connection.GoogleAuthToken, GoogleRefreshToken: connection.GoogleRefreshToken}
}

func (s *chatService) probeConnection(ctx context.Context, chat *models.Chat, connection models.Connection) error {
	if connection.Type == constants.DatabaseTypeSpreadsheet || connection.Type == constants.DatabaseTypeGoogleSheets {
		return nil
	}
	config := connectionConfig(chat, connection)
	if chat.ERPAssociated {
		config.ApplicationName = s.sourcePolicy().applicationName(chat)
	}
	return s.dbManager.TestConnection(ctx, &config)
}

func (s *chatService) persistNewChat(ctx context.Context, chat *models.Chat, plaintext models.Connection, ping bool) error {
	classification, sourceID := s.sourcePolicy().Classify(chat, plaintext)
	if classification == models.SourceIndependent {
		if ping {
			if err := s.probeConnection(ctx, chat, plaintext); err != nil {
				return err
			}
		}
		return s.chatRepo.Create(chat)
	}
	chat.ERPAssociated, chat.SourceBindingConfirmed = true, false
	if sourceID != "" {
		chat.SourceID = sourceID
	}
	chat.SourceConnectionFingerprint = ConnectionFingerprint(plaintext)
	chat.SourceBindingRevision, chat.ConnectionRevision = 0, 0
	chat.Connection.CurrentSchema, chat.Connection.SchemaUpdatedAt = nil, nil
	if err := s.chatRepo.Create(chat); err != nil {
		return err
	}
	next := *chat
	if next.SelectedObjects != nil {
		next.SelectionRevision = chat.SelectionRevision + 1
	}
	if _, err := s.sourcePolicy().ChangeBinding(ctx, chat.UserID, chat, &next, plaintext, s.chatRepo,
		&chatBindingEffects{service: s, owner: chat.UserID, chatID: chat.ID, probe: ping, refresh: ping}); err != nil {
		return err
	}
	current, err := s.chatRepo.ReadCurrent(ctx, chat.UserID, chat.ID)
	if err != nil {
		return err
	}
	*chat = *current
	return nil
}

type chatBindingEffects struct {
	service  *chatService
	owner    primitive.ObjectID
	chatID   primitive.ObjectID
	probe    bool
	refresh  bool
	metadata *repositories.ChatMetadataPatch
}

func (e *chatBindingEffects) Probe(ctx context.Context, connection models.Connection) error {
	chat, err := e.service.chatRepo.ReadCurrent(ctx, e.owner, e.chatID)
	if err != nil {
		return err
	}
	if err := e.service.sourcePolicy().RequireRead(ctx, e.owner, chat, connection); err != nil {
		return err
	}
	if !e.probe {
		return ctx.Err()
	}
	return e.service.probeConnection(ctx, chat, connection)
}

func (e *chatBindingEffects) Invalidate(ctx context.Context, previous *models.Chat) error {
	connection := previous.Connection.Clone()
	utils.DecryptConnection(&connection)
	if err := e.service.sourcePolicy().RequireRead(ctx, e.owner, previous, connection); err != nil {
		return err
	}
	if err := e.service.dbManager.InvalidateConnection(ctx, connectionConfig(previous, connection)); err != nil {
		return err
	}
	// All source EX/identity/predecessor checks and the connection probe have
	// completed. Persist before ChangeBinding can launch schema descendants;
	// a write failure returns synchronously and retains its recovery fence.
	if e.metadata != nil {
		// Selection was committed with the connection. Reapplying it here would
		// advance its revision twice or overwrite a concurrent selection update.
		metadata := *e.metadata
		metadata.SelectedCollections = nil
		return e.service.chatRepo.PatchMetadata(ctx, e.owner, e.chatID, metadata)
	}
	return nil
}

func (e *chatBindingEffects) Refresh(ctx context.Context, confirmed *models.Chat) error {
	if !e.refresh {
		return ctx.Err()
	}
	current, err := e.service.chatRepo.ReadCurrent(ctx, e.owner, confirmed.ID)
	if err != nil {
		return err
	}
	if !sameChatBinding(current, confirmed) {
		return ErrSourceBindingConflict
	}
	return e.service.refreshSchemaJoined(ctx, current)
}

// Delete a chat
func (s *chatService) Delete(userID, chatID string) (uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	// Verify ownership
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	// Delete dashboards and widgets first (before deleting chat)
	if s.dashboardRepo != nil {
		dashboards, err := s.dashboardRepo.FindDashboardsByChatID(context.Background(), chatObjID)
		if err != nil {
			log.Printf("Warning: failed to fetch dashboards for deletion: %v", err)
		} else {
			for _, dashboard := range dashboards {
				// Delete widgets first
				if err := s.dashboardRepo.DeleteWidgetsByDashboardID(context.Background(), dashboard.ID); err != nil {
					log.Printf("Warning: failed to delete widgets for dashboard %s: %v", dashboard.ID.Hex(), err)
				}
				// Delete dashboard
				if err := s.dashboardRepo.DeleteDashboard(context.Background(), dashboard.ID); err != nil {
					log.Printf("Warning: failed to delete dashboard %s: %v", dashboard.ID.Hex(), err)
				}
			}
			if len(dashboards) > 0 {
				log.Printf("Deleted %d dashboards for chat %s", len(dashboards), chatID)
			}
		}
	}

	// Delete visualizations (fetch messages first to get their IDs)
	if s.visualizationRepo != nil {
		messages, _, err := s.chatRepo.FindMessagesByChat(chatObjID, 1, 10000) // Large page to get all
		if err != nil {
			log.Printf("Warning: failed to fetch messages for visualization cleanup: %v", err)
		} else {
			vizDeletedCount := 0
			for _, msg := range messages {
				if err := s.visualizationRepo.DeleteVisualizationsByMessageID(context.Background(), msg.ID); err != nil {
					log.Printf("Warning: failed to delete visualizations for message %s: %v", msg.ID.Hex(), err)
				} else {
					vizDeletedCount++
				}
			}
			if vizDeletedCount > 0 {
				log.Printf("Deleted visualizations for %d messages in chat %s", vizDeletedCount, chatID)
			}
		}
	}

	// Delete chat and its messages
	if err := s.chatRepo.Delete(chatObjID); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to delete chat: %v", err)
	}

	// Delete messages
	if err := s.chatRepo.DeleteMessages(chatObjID); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to delete chat messages: %v", err)
	}

	go func() {
		// Delete DB connection with connection type for safety validation
		if err := s.dbManager.DisconnectWithType(chatID, userID, chat.Connection.Type, true); err != nil {
			log.Printf("failed to delete DB connection: %v", err)
		}

		// Delete vectors from Qdrant
		if s.vectorizationSvc != nil && s.vectorizationSvc.IsAvailable(context.Background()) {
			if err := s.vectorizationSvc.DeleteChatVectors(context.Background(), chatID); err != nil {
				log.Printf("failed to delete chat vectors: %v", err)
			}
		}

		// Delete knowledge base from MongoDB
		if s.kbRepo != nil {
			if err := s.kbRepo.DeleteByChatID(context.Background(), chatObjID); err != nil {
				log.Printf("failed to delete knowledge base: %v", err)
			}
		}
	}()

	return http.StatusOK, nil
}

// Get a chat by ID
func (s *chatService) GetByID(userID, chatID string) (*dtos.ChatResponse, uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	return s.buildChatResponse(chat), http.StatusOK, nil
}

// List all chats for a user
func (s *chatService) List(userID string, page, pageSize int) (*dtos.ChatListResponse, uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chats, total, err := s.chatRepo.FindByUserID(userObjID, page, pageSize)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chats: %v", err)
	}

	response := &dtos.ChatListResponse{
		Chats: make([]dtos.ChatResponse, len(chats)),
		Total: total,
	}

	for i, chat := range chats {
		log.Printf("ChatService -> List -> Chat %s settings: AutoExecuteQuery=%v, NonTechMode=%v",
			chat.ID.Hex(), chat.Settings.AutoExecuteQuery, chat.Settings.NonTechMode)
		response.Chats[i] = *s.buildChatResponse(chat)
	}

	return response, http.StatusOK, nil
}

// Create a new message
func (s *chatService) CreateMessage(ctx context.Context, userID, chatID string, streamID string, content string, llmModel string) (*dtos.MessageResponse, uint16, error) {
	chat, read, err := acquireChatSource(ctx, s.chatRepo, s.sourceAccess, userID, chatID)
	if err != nil {
		return nil, uint16(sourceHTTPStatus(err)), err
	}
	defer read.Close()
	// Freeze at the first authorized request read, before persistence/readiness.
	ctx = freezeGenerationContext(read.Context, chat)
	chatObjID := chat.ID
	if llmModel != "" {
		// The catalogue is provider-configured; unknown IDs fall back to the
		// default model at generation time instead of rejecting the message.
		if _, _, ok := s.resolveLLMSelection(ctx, llmModel); !ok {
			log.Printf("ChatService -> CreateMessage -> model %q is not in the catalogue, using the default", llmModel)
		}
	}

	// Create and save the user message first
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	msg := &models.Message{
		Base:    models.NewBase(),
		UserID:  userObjID,
		ChatID:  chatObjID,
		Content: content,
		Type:    string(constants.MessageTypeUser),
	}
	if llmModel != "" {
		msg.LLMModel = &llmModel // Store the selected LLM model with the user message
	}

	if err := s.chatRepo.CreateMessage(msg); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to save message: %v", err)
	}

	// Vectorize the user message in the background for conversational RAG retrieval
	vectorCtx, releaseVector, err := read.retain(context.WithoutCancel(ctx))
	if err != nil {
		return nil, uint16(sourceHTTPStatus(err)), err
	}
	go func() {
		defer releaseVector()
		bgCtx := vectorCtx
		if s.vectorizationSvc != nil {
			// Use the total message count as a rough message index for ordering
			_, total, _ := s.chatRepo.FindMessagesByChat(chatObjID, 1, 1)
			if err := s.vectorizationSvc.VectorizeMessage(bgCtx, chatID, msg.ID.Hex(), "user", content, int(total)); err != nil {
				log.Printf("ChatService -> CreateMessage -> Failed to vectorize user message: %v", err)
			}
		}
	}()

	log.Printf("ChatService -> CreateMessage -> AutoExecuteQuery: %v", chat.Settings.AutoExecuteQuery)

	// Check if schema is ready before processing LLM response
	// If schema is not ready, return a system message asking user to refresh schema
	// Updated IsSchemaReady now checks both in-memory cache AND Redis (works for all DB types)
	schemaReady, schemaErr := s.schemaReadyForGeneration(ctx, chat)
	if schemaErr != nil {
		return nil, uint16(sourceHTTPStatus(schemaErr)), schemaErr
	}
	if !schemaReady {
		log.Printf("ChatService -> CreateMessage -> Schema not ready for chatID: %s, returning schema refresh message", chatID)

		// Create a system message telling user to refresh schema
		systemMsg := &models.Message{
			Base:          models.NewBase(),
			UserID:        userObjID,
			ChatID:        chatObjID,
			UserMessageId: &msg.ID,
			Content:       "Your Knowledge Base requires to be refreshed for latest knowledge, please refresh it to get accurate insights & analytics and then send a new message.",
			Type:          string(constants.MessageTypeAssistant),
			ActionButtons: &[]models.ActionButton{
				{
					Label:     "Refresh Knowledge Base",
					Action:    "refresh_schema",
					IsPrimary: true,
				},
			},
		}

		// Ensure system message has a created_at that is ALWAYS after user message for correct ordering
		// Add 2 second offset to guarantee system message appears after user message in sorted results
		systemMsg.CreatedAt = msg.CreatedAt.Add(2 * time.Second)
		systemMsg.UpdatedAt = systemMsg.CreatedAt

		if err := s.chatRepo.CreateMessage(systemMsg); err != nil {
			log.Printf("ChatService -> CreateMessage -> Error saving system message: %v", err)
			// Still return success to user, but log the error
		}

		// Send system message via SSE after a 1-second delay to allow frontend to create temporary streaming message first
		// This prevents race condition where system message arrives before temp message is created
		go func() {
			time.Sleep(1 * time.Second)
			s.sendStreamEvent(userID, chatID, streamID, dtos.StreamResponse{
				Event: "system-message",
				Data: map[string]interface{}{
					"chat_id":        chatID,
					"message_id":     systemMsg.ID.Hex(),
					"content":        systemMsg.Content,
					"type":           systemMsg.Type,
					"action_buttons": dtos.ToActionButtonDto(systemMsg.ActionButtons),
					"created_at":     systemMsg.CreatedAt.Format(time.RFC3339),
				},
			})
		}()

		// Return user message response (schema not ready yet)
		return &dtos.MessageResponse{
			ID:        msg.ID.Hex(),
			ChatID:    chatID,
			Content:   content,
			Type:      string(constants.MessageTypeUser),
			CreatedAt: msg.CreatedAt.Format(time.RFC3339),
		}, http.StatusOK, nil
	}

	// If auto execute query is true, we need to process LLM response & run query automatically
	if chat.Settings.AutoExecuteQuery {
		if err := s.processLLMResponseAndRunQuery(ctx, userID, chatID, msg.ID.Hex(), streamID); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to process message: %v", err)
		}
	} else {
		// Start processing the message asynchronously
		if err := s.processMessage(ctx, userID, chatID, msg.ID.Hex(), streamID); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to process message: %v", err)
		}
	}

	// Return the actual message ID
	return &dtos.MessageResponse{
		ID:        msg.ID.Hex(), // Use actual message ID
		ChatID:    chatID,
		Content:   content,
		Type:      string(constants.MessageTypeUser),
		CreatedAt: msg.CreatedAt.Format(time.RFC3339),
	}, http.StatusOK, nil
}

// Update a message
func (s *chatService) UpdateMessage(ctx context.Context, userID, chatID, messageID string, streamID string, req *dtos.CreateMessageRequest) (*dtos.MessageResponse, uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	messageObjID, err := primitive.ObjectIDFromHex(messageID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid message ID format")
	}

	message, err := s.chatRepo.FindMessageByID(messageObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch message: %v", err)
	}
	if message == nil {
		return nil, http.StatusNotFound, fmt.Errorf("message not found")
	}

	if message.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to message")
	}

	if message.ChatID != chatObjID {
		return nil, http.StatusBadRequest, fmt.Errorf("message does not belong to chat")
	}

	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil || chat.UserID != userObjID {
		return nil, http.StatusForbidden, ErrSourceForbidden
	}
	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	read, err := s.sourcePolicy().AcquireRead(ctx, userObjID, chat, connection)
	if err != nil {
		return nil, sourceHTTPStatus(err), err
	}
	defer read.Close()
	// Freeze at the first authorized request read, before persistence/readiness.
	ctx = freezeGenerationContext(read.Context, chat)

	log.Printf("UpdateMessage -> content: %+v", req.Content)
	// Update message content, This is a user message
	message.Content = req.Content
	message.IsEdited = true
	log.Printf("UpdateMessage -> message: %+v", message)
	log.Printf("UpdateMessage -> message.Content: %+v", message.Content)
	err = s.chatRepo.UpdateMessage(message.ID, message)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to update message: %v", err)
	}

	if s.vectorizationSvc != nil {
		if delErr := s.vectorizationSvc.DeleteMessageVector(ctx, chatID, messageID); delErr != nil {
			log.Printf("UpdateMessage -> Failed to delete old message vector: %v", delErr)
		}
	}
	// Find the next AI message after the edited message
	nextMessage, err := s.chatRepo.FindNextMessageByID(messageObjID)
	if err == nil && nextMessage != nil && nextMessage.Type == string(constants.MessageTypeAssistant) {
		log.Printf("UpdateMessage -> Found next AI message: %v", nextMessage.ID)

		// Delete old AI message vector — it will be recreated after LLM re-generation
		if s.vectorizationSvc != nil {
			if delErr := s.vectorizationSvc.DeleteMessageVector(ctx, chatID, nextMessage.ID.Hex()); delErr != nil {
				log.Printf("UpdateMessage -> Failed to delete old AI message vector: %v", delErr)
			}
		}
		if nextMessage.Queries != nil {
			for i := range *nextMessage.Queries {
				(*nextMessage.Queries)[i].IsExecuted = false
				(*nextMessage.Queries)[i].IsRolledBack = false
				(*nextMessage.Queries)[i].ExecutionResult = nil
				(*nextMessage.Queries)[i].ExecutionTime = nil
				(*nextMessage.Queries)[i].Error = nil
			}

			// Update the AI message with reset query states
			if err := s.chatRepo.UpdateMessage(nextMessage.ID, nextMessage); err != nil {
				log.Printf("UpdateMessage -> Failed to update AI message: %v", err)
				// Continue even if this fails, as it's not critical
			}
		}
	}

	// If auto execute query is true, we need to process LLM response & run query automatically
	if chat.Settings.AutoExecuteQuery {
		if err := s.processLLMResponseAndRunQuery(ctx, userID, chatID, messageID, streamID); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to process message: %v", err)
		}
	} else {
		// Start processing the message asynchronously
		if err := s.processMessage(ctx, userID, chatID, messageID, streamID); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to process message: %v", err)
		}
	}
	return s.buildMessageResponse(message), http.StatusOK, nil
}

// Delete messages
func (s *chatService) DeleteMessages(userID, chatID string) (uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	// Verify chat ownership
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	if err := s.chatRepo.DeleteMessages(chatObjID); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to delete messages: %v", err)
	}

	// Clean up all message vectors in the background (schema vectors are preserved)
	go func() {
		if s.vectorizationSvc != nil {
			bgCtx := context.Background()
			if err := s.vectorizationSvc.DeleteChatMessageVectors(bgCtx, chatID); err != nil {
				log.Printf("DeleteMessages -> Failed to delete message vectors: %v", err)
			}
		}
	}()

	return http.StatusOK, nil
}

// Duplicate a chat
func (s *chatService) Duplicate(userID, chatID string, duplicateMessages bool, duplicateDashboards bool, selectedCollections ...*string) (*dtos.ChatResponse, uint32, error) {
	return s.duplicateWithObjects(userID, chatID, duplicateMessages, duplicateDashboards, nil, selectedCollections...)
}
func (s *chatService) DuplicateObjects(userID, chatID string, duplicateMessages, duplicateDashboards bool, objects *schemaobjects.Selection) (*dtos.ChatResponse, uint32, error) {
	return s.duplicateWithObjects(userID, chatID, duplicateMessages, duplicateDashboards, objects)
}
func (s *chatService) duplicateWithObjects(userID, chatID string, duplicateMessages bool, duplicateDashboards bool, objectSelection *schemaobjects.Selection, selectedCollections ...*string) (*dtos.ChatResponse, uint32, error) {
	// Validate user ID
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	// Validate chat ID
	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	// Verify chat ownership & check if chat exists
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	var selection *string
	if len(selectedCollections) > 1 {
		return nil, http.StatusBadRequest, fmt.Errorf("only one selected_collections value is allowed")
	}
	if len(selectedCollections) == 1 {
		selection = selectedCollections[0]
	}
	if selection != nil && chat.SelectedObjects != nil {
		return nil, http.StatusConflict, ErrSelectionConflict
	}
	if objectSelection == nil && selection == nil && chat.SelectedObjects != nil {
		inherited, err := chat.QuerySelection()
		if err != nil {
			return nil, http.StatusConflict, err
		}
		objectSelection = &inherited
	}
	var selected string
	if objectSelection != nil {
		if !config.Env.QueryObjectsEnabled || chat.Connection.Type != constants.DatabaseTypePostgreSQL {
			return nil, http.StatusConflict, ErrQueryObjectsDisabled
		}
		objectSelection, selected, _, err = prepareObjectSelection(nil, objectSelection, selection, nil)
	} else {
		selected, err = newChatSelection(selection, chat.SelectedCollections)
	}
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	// If trial mode, check if user already has 2 chats, return error
	if config.Env.MaxChatsPerUser == 0 { // 0 == Trial Mode
		chats, _, err := s.chatRepo.FindByUserID(userObjID, 1, 3) // Trying to fetch 3 chats
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
		}
		if len(chats) >= 2 {
			return nil, http.StatusBadRequest, fmt.Errorf("You cannot have more than 2 chats in trial mode")
		}
	}
	// Duplicate the chat
	newChat := &models.Chat{
		UserID:                      userObjID,
		DisplayName:                 chat.DisplayName,
		SourceID:                    chat.SourceID,
		SourceBindingRevision:       chat.SourceBindingRevision,
		SourceConnectionFingerprint: chat.SourceConnectionFingerprint,
		ERPAssociated:               chat.ERPAssociated || chat.SourceID != "",
		// The new Chat needs its own registry binding; never copy confirmation.
		SourceBindingConfirmed: false,
		Connection:             chat.Connection.Clone(),
		SelectedCollections:    selected,
		SelectedObjects:        objectSelection,
		Settings:               chat.Settings,
		Base:                   models.NewBase(), // Create a new Base with new ID and timestamps
	}

	if selection != nil || objectSelection != nil {
		// An explicit scope must not inherit a schema captured for another scope.
		newChat.Connection.CurrentSchema, newChat.Connection.SchemaUpdatedAt = nil, nil
	}

	if objectSelection != nil {
		newChat.SelectionRevision = 1
	}
	plaintext := newChat.Connection.Clone()
	utils.DecryptConnection(&plaintext)
	classification, _ := s.sourcePolicy().Classify(newChat, plaintext)
	if classification == models.SourceIndependent {
		if err := s.chatRepo.Create(newChat); err != nil {
			return nil, http.StatusInternalServerError, err
		}
	} else if err := s.persistNewChat(context.Background(), newChat, plaintext, true); err != nil {
		return nil, http.StatusConflict, err
	}

	// if duplicateMessages is true, then we duplicate both regular messages and LLM messages
	if duplicateMessages {
		// Create a mapping of old message IDs to new message IDs to maintain relationships
		messageIDMap := make(map[primitive.ObjectID]primitive.ObjectID)
		messageIDMapMutex := &sync.Mutex{}

		// First, get all messages in the original chat in a single query to maintain their ordering
		allMessages, _, err := s.chatRepo.FindMessagesByChat(chatObjID, 1, 1000) // Large page size to get all
		if err != nil {
			log.Printf("Warning: failed to fetch messages: %v", err)
			// Continue without messages, at least the chat was duplicated
			return s.buildChatResponse(newChat), http.StatusOK, nil
		}

		if len(allMessages) > 0 {
			// Sort messages by created_at to ensure proper ordering
			sort.Slice(allMessages, func(i, j int) bool {
				return allMessages[i].CreatedAt.Before(allMessages[j].CreatedAt)
			})

			log.Printf("Duplicating %d messages in order", len(allMessages))

			// Process messages sequentially to ensure correct ordering
			baseTime := time.Now()
			for i, originalMsg := range allMessages {
				// Create a new message with the same content but for the new chat
				newMsg := &models.Message{
					UserID:   userObjID,
					ChatID:   newChat.ID,
					Type:     originalMsg.Type,
					Content:  originalMsg.Content,
					IsEdited: originalMsg.IsEdited,
					Base:     models.NewBase(), // Create a new Base with new ID and timestamps
				}

				// Set timestamps with precise sequential ordering
				newMsg.CreatedAt = baseTime.Add(time.Duration(i*1000) * time.Millisecond) // 1 second increment
				newMsg.UpdatedAt = newMsg.CreatedAt
				// A copied chat must not keep a reference to the source run.
				// The run document stays on the original chat. Q08 still has no
				// retention decision, so duplication neither clones nor deletes it.
				if originalMsg.AssistantRunID != nil {
					newMsg.AssistantRunID = nil
					newMsg.Content = strings.TrimRight(originalMsg.Content, "\n") + "\n" + assistantReportNotCopiedNotice
				}

				if originalMsg.UserMessageId != nil {
					messageIDMapMutex.Lock()
					if newID, exists := messageIDMap[*originalMsg.UserMessageId]; exists {
						newMsg.UserMessageId = &newID
					}
					messageIDMapMutex.Unlock()
				}

				// Copy queries if they exist
				if originalMsg.Queries != nil {
					queries := make([]models.Query, len(*originalMsg.Queries))
					for i, q := range *originalMsg.Queries {
						// Create a copy of the query with a new ID
						queries[i] = models.Query{
							ID:                     primitive.NewObjectID(),
							Query:                  q.Query,
							QueryType:              q.QueryType,
							Tables:                 q.Tables,
							Description:            q.Description,
							RollbackDependentQuery: q.RollbackDependentQuery, // Will update in second pass
							RollbackQuery:          q.RollbackQuery,
							ExecutionTime:          q.ExecutionTime,
							ExampleExecutionTime:   q.ExampleExecutionTime,
							CanRollback:            q.CanRollback,
							IsCritical:             q.IsCritical,
							IsExecuted:             false, // Reset execution state in the duplicate
							IsRolledBack:           false, // Reset rollback state
							Error:                  q.Error,
							ExampleResult:          q.ExampleResult,
							ExecutionResult:        nil, // Clear execution results
							IsEdited:               q.IsEdited,
							Metadata:               q.Metadata,
							ActionAt:               q.ActionAt,
						}

						// Copy pagination if it exists
						if q.Pagination != nil {
							queries[i].Pagination = &models.Pagination{
								TotalRecordsCount: q.Pagination.TotalRecordsCount,
								PaginatedQuery:    q.Pagination.PaginatedQuery,
								CountQuery:        q.Pagination.CountQuery,
							}
						}
					}
					newMsg.Queries = &queries
				}

				// Copy action buttons if they exist
				if originalMsg.ActionButtons != nil {
					actionButtons := make([]models.ActionButton, len(*originalMsg.ActionButtons))
					for i, btn := range *originalMsg.ActionButtons {
						actionButtons[i] = models.ActionButton{
							ID:        primitive.NewObjectID(),
							Label:     btn.Label,
							Action:    btn.Action,
							IsPrimary: btn.IsPrimary,
						}
					}
					newMsg.ActionButtons = &actionButtons
				}

				// Save the new message
				if err := s.chatRepo.CreateMessage(newMsg); err != nil {
					log.Printf("Error duplicating message: %v", err)
					continue
				}

				// Store the ID mapping
				messageIDMapMutex.Lock()
				messageIDMap[originalMsg.ID] = newMsg.ID
				messageIDMapMutex.Unlock()
			}
		}

		// Second pass to update complex relationships if needed
		newMessages, _, err := s.chatRepo.FindMessagesByChat(newChat.ID, 1, 1000)
		if err == nil && len(newMessages) > 0 {
			for _, message := range newMessages {
				needsUpdate := false

				// Update query relationships if needed
				if message.Queries != nil {
					for i := range *message.Queries {
						// Update RollbackDependentQuery if it exists
						if (*message.Queries)[i].RollbackDependentQuery != nil {
							// For simplicity, set to nil
							(*message.Queries)[i].RollbackDependentQuery = nil
							needsUpdate = true
						}
					}
				}

				if needsUpdate {
					if err := s.chatRepo.UpdateMessage(message.ID, message); err != nil {
						log.Printf("Error updating duplicated message relationships: %v", err)
					}
				}
			}
		}

		log.Printf("Chat duplication completed successfully with messages. New chat ID: %s", newChat.ID.Hex())

		// Explicit scope requests rebuild knowledge, even when the scope is unchanged.
		// The copy API bundles schema and message vectors, so skip both.
		// Copy vectors (schema + messages) only for legacy unscoped duplication.
		if selection == nil && objectSelection == nil && classification == models.SourceIndependent && s.vectorizationSvc != nil {
			go func() {
				copyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()

				if !s.vectorizationSvc.IsAvailable(copyCtx) {
					log.Printf("Chat duplication -> Skipping vector copy: vectorization service unavailable")
					return
				}

				// Build string-based message ID map for vector remapping
				msgIDMapStr := make(map[string]string, len(messageIDMap))
				messageIDMapMutex.Lock()
				for oldID, newID := range messageIDMap {
					msgIDMapStr[oldID.Hex()] = newID.Hex()
				}
				messageIDMapMutex.Unlock()

				if err := s.vectorizationSvc.CopyVectorsForChat(copyCtx, chatID, newChat.ID.Hex(), true, msgIDMapStr); err != nil {
					log.Printf("Chat duplication -> Warning: failed to copy vectors: %v", err)
				}
			}()
		}
	} else {
		// No messages duplicated — still copy schema vectors (same DB connection)
		if selection == nil && objectSelection == nil && classification == models.SourceIndependent && s.vectorizationSvc != nil {
			go func() {
				copyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()

				if !s.vectorizationSvc.IsAvailable(copyCtx) {
					log.Printf("Chat duplication -> Skipping vector copy: vectorization service unavailable")
					return
				}

				if err := s.vectorizationSvc.CopyVectorsForChat(copyCtx, chatID, newChat.ID.Hex(), false, nil); err != nil {
					log.Printf("Chat duplication -> Warning: failed to copy schema vectors: %v", err)
				}
			}()
		}
	}

	// Only legacy unscoped duplication may inherit the old knowledge base.
	if selection == nil && objectSelection == nil && classification == models.SourceIndependent && s.kbRepo != nil {
		go func() {
			kbCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			sourceKB, err := s.kbRepo.FindByChatID(kbCtx, chatObjID)
			if err != nil {
				log.Printf("Chat duplication -> Warning: failed to fetch source KB: %v", err)
				return
			}
			if sourceKB == nil {
				log.Printf("Chat duplication -> No knowledge base to copy for chat %s", chatID)
				return
			}

			newKB := models.NewKnowledgeBase(newChat.ID)
			newKB.UserID = userObjID
			newKB.TableDescriptions = sourceKB.TableDescriptions

			if err := s.kbRepo.Upsert(kbCtx, newKB); err != nil {
				log.Printf("Chat duplication -> Warning: failed to copy knowledge base: %v", err)
			} else {
				log.Printf("Chat duplication -> Copied knowledge base (%d tables) to new chat %s",
					len(sourceKB.TableDescriptions), newChat.ID.Hex())
			}
		}()
	}

	// Copy dashboards and their widgets in background
	if duplicateDashboards && s.dashboardRepo != nil {
		go func() {
			dashCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			sourceDashboards, err := s.dashboardRepo.FindDashboardsByChatID(dashCtx, chatObjID)
			if err != nil {
				log.Printf("Chat duplication -> Warning: failed to fetch source dashboards: %v", err)
				return
			}
			if len(sourceDashboards) == 0 {
				log.Printf("Chat duplication -> No dashboards to copy for chat %s", chatID)
				return
			}

			for _, srcDashboard := range sourceDashboards {
				// Create new dashboard for the new chat
				newDashboard := models.NewDashboard(userObjID, newChat.ID, srcDashboard.Name)
				newDashboard.Description = srcDashboard.Description
				newDashboard.TemplateType = srcDashboard.TemplateType
				newDashboard.IsDefault = srcDashboard.IsDefault
				newDashboard.RefreshInterval = srcDashboard.RefreshInterval
				newDashboard.TimeRange = srcDashboard.TimeRange
				newDashboard.GeneratedPrompt = srcDashboard.GeneratedPrompt
				newDashboard.LLMModel = srcDashboard.LLMModel

				if err := s.dashboardRepo.CreateDashboard(dashCtx, newDashboard); err != nil {
					log.Printf("Chat duplication -> Warning: failed to create dashboard '%s': %v", srcDashboard.Name, err)
					continue
				}

				// Fetch and duplicate widgets
				srcWidgets, err := s.dashboardRepo.FindWidgetsByDashboardID(dashCtx, srcDashboard.ID)
				if err != nil {
					log.Printf("Chat duplication -> Warning: failed to fetch widgets for dashboard '%s': %v", srcDashboard.Name, err)
					continue
				}

				// Build widget ID mapping for layout references
				widgetIDMap := make(map[string]string) // old widget ID -> new widget ID
				newWidgets := make([]*models.Widget, 0, len(srcWidgets))

				for _, srcWidget := range srcWidgets {
					newWidget := models.NewWidget(newDashboard.ID, newChat.ID, userObjID, srcWidget.Title, srcWidget.WidgetType, srcWidget.Query)
					newWidget.Description = srcWidget.Description
					newWidget.QueryType = srcWidget.QueryType
					newWidget.Tables = srcWidget.Tables
					newWidget.ChartConfigJSON = srcWidget.ChartConfigJSON
					newWidget.GeneratedPrompt = srcWidget.GeneratedPrompt
					newWidget.LLMModel = srcWidget.LLMModel

					if srcWidget.StatConfig != nil {
						statCopy := *srcWidget.StatConfig
						newWidget.StatConfig = &statCopy
					}
					if srcWidget.TableConfig != nil {
						tableCopy := *srcWidget.TableConfig
						newWidget.TableConfig = &tableCopy
					}

					widgetIDMap[srcWidget.ID.Hex()] = newWidget.ID.Hex()
					newWidgets = append(newWidgets, newWidget)
				}

				if len(newWidgets) > 0 {
					if err := s.dashboardRepo.CreateWidgets(dashCtx, newWidgets); err != nil {
						log.Printf("Chat duplication -> Warning: failed to create widgets for dashboard '%s': %v", srcDashboard.Name, err)
						// Try individual creation as fallback
						for _, w := range newWidgets {
							if err := s.dashboardRepo.CreateWidget(dashCtx, w); err != nil {
								log.Printf("Chat duplication -> Warning: failed to create widget '%s': %v", w.Title, err)
							}
						}
					}
				}

				// Remap layout widget IDs to the new widget IDs
				newLayout := make([]models.WidgetLayout, 0, len(srcDashboard.Layout))
				for _, srcLayout := range srcDashboard.Layout {
					newWidgetID, ok := widgetIDMap[srcLayout.WidgetID]
					if !ok {
						continue
					}
					newLayout = append(newLayout, models.WidgetLayout{
						WidgetID: newWidgetID,
						X:        srcLayout.X,
						Y:        srcLayout.Y,
						W:        srcLayout.W,
						H:        srcLayout.H,
						MinW:     srcLayout.MinW,
						MinH:     srcLayout.MinH,
					})
				}
				newDashboard.Layout = newLayout
				if err := s.dashboardRepo.UpdateDashboard(dashCtx, newDashboard.ID, newDashboard); err != nil {
					log.Printf("Chat duplication -> Warning: failed to update dashboard layout: %v", err)
				}

				log.Printf("Chat duplication -> Copied dashboard '%s' with %d widgets to new chat %s",
					srcDashboard.Name, len(newWidgets), newChat.ID.Hex())
			}

			log.Printf("Chat duplication -> Copied %d dashboards to new chat %s", len(sourceDashboards), newChat.ID.Hex())
		}()
	}

	return s.buildChatResponse(newChat), http.StatusOK, nil
}

// List messages for a chat
func (s *chatService) ListMessages(userID, chatID string, page, pageSize int) (*dtos.MessageListResponse, uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	// Verify chat ownership
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	messages, total, err := s.chatRepo.FindLatestMessageByChat(chatObjID, page, pageSize)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch messages: %v", err)
	}

	response := &dtos.MessageListResponse{
		Messages: make([]dtos.MessageResponse, len(messages)),
		Total:    total,
	}

	for i, msg := range messages {
		response.Messages[i] = *s.buildMessageResponse(msg)
	}

	return response, http.StatusOK, nil
}

// PinMessage pins a message and its related message in the cluster
func (s *chatService) PinMessage(userID, chatID, messageID string) (interface{}, uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	messageObjID, err := primitive.ObjectIDFromHex(messageID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid message ID format")
	}

	// Verify chat ownership
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	// Get the message
	message, err := s.chatRepo.FindMessageByID(messageObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch message: %v", err)
	}
	if message == nil {
		return nil, http.StatusNotFound, fmt.Errorf("message not found")
	}

	// Pin the message
	message.IsPinned = true
	now := time.Now()
	message.PinnedAt = &now
	if err := s.chatRepo.UpdateMessage(message.ID, message); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to pin message: %v", err)
	}

	// Handle cluster pinning logic
	if message.Type == string(constants.MessageTypeUser) {
		// If pinning a user message, also pin the AI response below it
		messages, _, err := s.chatRepo.FindMessagesByChatAfterTime(chatObjID, message.CreatedAt, 1, 2)
		if err == nil && len(messages) > 1 {
			for _, msg := range messages {
				if msg.ID != message.ID && msg.Type == string(constants.MessageTypeAssistant) {
					msg.IsPinned = true
					msg.PinnedAt = &now
					s.chatRepo.UpdateMessage(msg.ID, &msg)
					break
				}
			}
		}
	} else if message.Type == string(constants.MessageTypeAssistant) {
		// If pinning an AI message, also pin the user message above it
		if message.UserMessageId != nil {
			userMsg, err := s.chatRepo.FindMessageByID(*message.UserMessageId)
			if err == nil && userMsg != nil {
				userMsg.IsPinned = true
				userMsg.PinnedAt = &now
				s.chatRepo.UpdateMessage(userMsg.ID, userMsg)
			}
		}
	}

	return map[string]interface{}{
		"message": "Message pinned successfully",
	}, http.StatusOK, nil
}

// UnpinMessage unpins a message and its related message in the cluster
func (s *chatService) UnpinMessage(userID, chatID, messageID string) (interface{}, uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	messageObjID, err := primitive.ObjectIDFromHex(messageID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid message ID format")
	}

	// Verify chat ownership
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	// Get the message
	message, err := s.chatRepo.FindMessageByID(messageObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch message: %v", err)
	}
	if message == nil {
		return nil, http.StatusNotFound, fmt.Errorf("message not found")
	}

	// Unpin the message
	message.IsPinned = false
	message.PinnedAt = nil
	if err := s.chatRepo.UpdateMessage(message.ID, message); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to unpin message: %v", err)
	}

	// Handle cluster unpinning logic
	if message.Type == string(constants.MessageTypeUser) {
		// If unpinning a user message, also unpin the AI response below it
		messages, _, err := s.chatRepo.FindMessagesByChatAfterTime(chatObjID, message.CreatedAt, 1, 2)
		if err == nil && len(messages) > 1 {
			for _, msg := range messages {
				if msg.ID != message.ID && msg.Type == string(constants.MessageTypeAssistant) {
					msg.IsPinned = false
					msg.PinnedAt = nil
					s.chatRepo.UpdateMessage(msg.ID, &msg)
					break
				}
			}
		}
	} else if message.Type == string(constants.MessageTypeAssistant) {
		// If unpinning an AI message, also unpin the user message above it
		if message.UserMessageId != nil {
			userMsg, err := s.chatRepo.FindMessageByID(*message.UserMessageId)
			if err == nil && userMsg != nil {
				userMsg.IsPinned = false
				userMsg.PinnedAt = nil
				s.chatRepo.UpdateMessage(userMsg.ID, userMsg)
			}
		}
	}

	return map[string]interface{}{
		"message": "Message unpinned successfully",
	}, http.StatusOK, nil
}

// ListPinnedMessages lists all pinned messages for a chat
func (s *chatService) ListPinnedMessages(userID, chatID string) (*dtos.MessageListResponse, uint32, error) {
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	// Verify chat ownership
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
	}
	if chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}
	if chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	// Get all pinned messages
	messages, err := s.chatRepo.FindPinnedMessagesByChat(chatObjID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch pinned messages: %v", err)
	}

	response := &dtos.MessageListResponse{
		Messages: make([]dtos.MessageResponse, len(messages)),
		Total:    int64(len(messages)),
	}

	for i, msg := range messages {
		response.Messages[i] = *s.buildMessageResponse(&msg)
	}

	return response, http.StatusOK, nil
}

// Edit a query, this can be done only before the query is executed
func (s *chatService) EditQuery(ctx context.Context, userID, chatID, messageID, queryID string, query string) (*dtos.EditQueryResponse, uint32, error) {
	log.Printf("ChatService -> EditQuery -> userID: %s, chatID: %s, messageID: %s, queryID: %s, query: %s", userID, chatID, messageID, queryID, query)

	_, message, queryData, err := s.verifyQueryOwnership(userID, chatID, messageID, queryID)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	if queryData.IsExecuted || queryData.IsRolledBack {
		return nil, http.StatusBadRequest, fmt.Errorf("query has already been executed, cannot edit")
	}

	originalQuery := queryData.Query
	// Fix the query update logic
	for i := range *message.Queries {
		if (*message.Queries)[i].ID == queryData.ID {
			(*message.Queries)[i].Query = query
			(*message.Queries)[i].IsEdited = true
			if (*message.Queries)[i].Pagination != nil && (*message.Queries)[i].Pagination.PaginatedQuery != nil {
				(*message.Queries)[i].Pagination.PaginatedQuery = utils.StringPtr(strings.Replace(*(*message.Queries)[i].Pagination.PaginatedQuery, originalQuery, query, 1))
			}
		}
	}

	message.IsEdited = true
	if err := s.chatRepo.UpdateMessage(message.ID, message); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("failed to update message: %v", err)
	}

	return &dtos.EditQueryResponse{
		ChatID:    chatID,
		MessageID: messageID,
		QueryID:   queryID,
		Query:     query,
		IsEdited:  true,
	}, http.StatusOK, nil
}

// Get the DB connection status for current chat
func (s *chatService) GetDBConnectionStatus(ctx context.Context, userID, chatID string) (*dtos.ConnectionStatusResponse, uint32, error) {
	_, read, err := acquireChatSource(ctx, s.chatRepo, s.sourceAccess, userID, chatID)
	if err != nil {
		return nil, sourceHTTPStatus(err), err
	}
	defer read.Close()
	ctx = read.Context
	// Get connection info
	connInfo, exists := s.dbManager.GetConnectionInfo(chatID)
	if !exists {
		return nil, http.StatusNotFound, fmt.Errorf("no connection found")
	}

	// Check if connection is active
	isConnected, err := s.dbManager.PingConnection(ctx, chatID)
	if err != nil {
		return nil, sourceHTTPStatus(err), err
	}

	// Convert port string to int
	var port *int
	if connInfo.Config.Port != nil {
		portVal, err := strconv.Atoi(*connInfo.Config.Port)
		if err != nil {
			defaultPort := 0
			port = &defaultPort // Default value if conversion fails
		} else {
			port = &portVal
		}
	}

	return &dtos.ConnectionStatusResponse{
		IsConnected: isConnected,
		Type:        connInfo.Config.Type,
		Host:        connInfo.Config.Host,
		Port:        port,
		Database:    connInfo.Config.Database,
		Username:    *connInfo.Config.Username,
	}, http.StatusOK, nil
}

// ERP callbacks join every terminal; independent optional enrichment remains asynchronous.
func (s *chatService) usesERPQuantityPilot(read *SourceRead) bool {
	pilot, ok := s.assistant.(*assistantService)
	return ok && pilot != nil && pilot.useERPQuantityPilot(read)
}

func (s *chatService) HandleSchemaChange(ctx context.Context, userID, chatID, streamID string, diff interface{}) error {
	owner, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	id, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return err
	}
	chat, err := s.chatRepo.ReadCurrent(ctx, owner, id)
	if err != nil {
		return err
	}
	connection := chat.Connection.Clone()
	utils.DecryptConnection(&connection)
	classification, _ := s.sourcePolicy().Classify(chat, connection)
	if pilot, ok := s.assistant.(*assistantService); ok && pilot != nil &&
		classification == models.SourceRegisteredERP && chat.SourceID == pilot.erpQuantitySourceID {
		read, err := s.sourcePolicy().AcquireRead(ctx, owner, chat, connection)
		if err != nil {
			return err
		}
		defer read.Close()
		if s.usesERPQuantityPilot(read) {
			// The payment quantity workflow needs no model or vector index.
			// Automatic connection/schema checks must not export pilot examples.
			_, err := s.refreshSchemaStructure(read.Context, chat)
			return err
		}
	}
	if classification == models.SourceIndependent {
		if queryObjectsEnabled(chat) || chat.SelectedObjects != nil || !frozenGenerationPolicy(ctx, chat).AllowsAutomaticDataExecution() {
			return s.refreshSchemaJoined(ctx, chat)
		}
		schema, err := s.refreshSchemaStructure(ctx, chat)
		if err != nil {
			return err
		}
		go func() {
			childCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Minute)
			defer cancel()
			if err := s.enhanceSchemaJoined(childCtx, chatID, schema); err != nil {
				log.Printf("independent schema enrichment for chat %s: %v", chatID, err)
			}
		}()
		return nil
	}
	return s.refreshSchemaJoined(ctx, chat)
}

// Helper methods for building responses

func (s *chatService) buildChatResponse(chat *models.Chat) *dtos.ChatResponse {
	// Create a copy of the connection to avoid modifying the original
	connectionCopy := chat.Connection.Clone()

	// Decrypt connection details for the response
	utils.DecryptConnection(&connectionCopy)

	log.Printf("ChatService -> buildChatResponse -> Building response for chat %s with NonTechMode=%v, PreferredLLMModel=%v",
		chat.ID.Hex(), chat.Settings.NonTechMode, chat.PreferredLLMModel)

	// Handle username for spreadsheet connections which might not have it
	var username string
	if connectionCopy.Username != nil {
		username = *connectionCopy.Username
	}

	// Classification is local identity metadata, never an execution permit.
	// History-only services may have no policy; they publish no authority.
	classification := ""
	if policy := s.sourcePolicy(); policy != nil {
		kind, _ := policy.Classify(chat, connectionCopy)
		classification = string(kind)
	}

	return &dtos.ChatResponse{
		ID:                    chat.ID.Hex(),
		UserID:                chat.UserID.Hex(),
		DisplayName:           chat.DisplayName,
		SourceID:              chat.SourceID,
		SourceBindingRevision: chat.SourceBindingRevision,
		SourceClassification:  classification,
		Connection: dtos.ConnectionResponse{
			ID:             chat.ID.Hex(),
			Type:           connectionCopy.Type,
			Host:           connectionCopy.Host,
			Port:           connectionCopy.Port,
			Username:       username,
			Database:       connectionCopy.Database,
			IsExampleDB:    connectionCopy.IsExampleDB,
			UseSSL:         connectionCopy.UseSSL,
			SSLMode:        connectionCopy.SSLMode,
			SSLCertURL:     connectionCopy.SSLCertURL,
			SSLKeyURL:      connectionCopy.SSLKeyURL,
			SSLRootCertURL: connectionCopy.SSLRootCertURL,
			GoogleSheetID:  connectionCopy.GoogleSheetID,
			GoogleSheetURL: connectionCopy.GoogleSheetURL,
		},
		SelectedCollections: chat.SelectedCollections,
		SelectedObjects:     chat.SelectedObjects,
		SelectionRevision:   chat.SelectionRevision,
		QueryObjectIndex:    objectIndexStatus(chat),
		CreatedAt:           chat.CreatedAt.Format(time.RFC3339),
		UpdatedAt:           chat.UpdatedAt.Format(time.RFC3339),
		Settings: dtos.ChatSettingsResponse{
			AutoExecuteQuery:          chat.Settings.AutoExecuteQuery,
			ShareDataWithAI:           false, // Retain the wire field for older clients.
			NonTechMode:               chat.Settings.NonTechMode,
			AutoGenerateVisualization: chat.Settings.AutoGenerateVisualization,
			AutoAnalyzeResults:        chat.Settings.AutoAnalyzeResults,
		},
		PreferredLLMModel:       chat.PreferredLLMModel,
		PreferredReasoningLevel: chat.PreferredReasoningLevel,
	}
}

func (s *chatService) buildMessageResponse(msg *models.Message) *dtos.MessageResponse {
	var userMessageID *string
	if msg.UserMessageId != nil {
		id := msg.UserMessageId.Hex()
		userMessageID = &id
	}

	var pinnedAt *string
	if msg.PinnedAt != nil {
		pinnedAtStr := msg.PinnedAt.Format(time.RFC3339)
		pinnedAt = &pinnedAtStr
	}

	queriesDto := dtos.ToQueryDtoWithDecryption(msg.Queries, s.decryptQueryResult, s.visualizationRepo, context.Background())
	actionButtonsDto := dtos.ToActionButtonDto(msg.ActionButtons)

	// Get the display name for the LLM model if available
	var llmModelName *string
	if msg.LLMModel != nil {
		displayName := s.getModelDisplayName(*msg.LLMModel)
		llmModelName = &displayName
	}

	return &dtos.MessageResponse{
		ID:             msg.ID.Hex(),
		ChatID:         msg.ChatID.Hex(),
		UserMessageID:  userMessageID,
		Type:           msg.Type,
		Content:        msg.Content,
		Queries:        queriesDto,
		ActionButtons:  actionButtonsDto,
		IsEdited:       msg.IsEdited,
		NonTechMode:    msg.NonTechMode,
		IsPinned:       msg.IsPinned,
		PinnedAt:       pinnedAt,
		LLMModel:       msg.LLMModel,
		LLMModelName:   llmModelName,
		AssistantRunID: msg.AssistantRunID,
		CreatedAt:      msg.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      msg.UpdatedAt.Format(time.RFC3339),
	}
}

// BuildAssistantMessageResponse maps one stored message through the existing
// response builder. A nil service is enough: assistant_run_id does not need
// decryption or visualization lookup when Queries is nil.
func BuildAssistantMessageResponse(msg *models.Message) *dtos.MessageResponse {
	return (&chatService{}).buildMessageResponse(msg)
}

// Verify query ownership checks if the query belongs to the message and the message belongs to the chat
func (s *chatService) verifyQueryOwnership(userID, chatID, messageID, queryID string) (*models.Chat, *models.Message, *models.Query, error) {
	chat, err := ownedSourceChat(context.Background(), s.chatRepo, userID, chatID)
	if err != nil {
		return nil, nil, nil, err
	}
	// Convert IDs to ObjectIDs
	msgObjID, err := primitive.ObjectIDFromHex(messageID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid message ID format")
	}

	queryObjID, err := primitive.ObjectIDFromHex(queryID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid query ID format")
	}

	// Get message
	msg, err := s.chatRepo.FindMessageByID(msgObjID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to fetch message: %v", err)
	}
	if msg == nil {
		return nil, nil, nil, fmt.Errorf("message not found")
	}

	// Verify chat ownership
	if msg.ChatID.Hex() != chatID {
		return nil, nil, nil, fmt.Errorf("message does not belong to this chat")
	}
	if msg.UserID != chat.UserID {
		return nil, nil, nil, ErrSourceForbidden
	}

	log.Printf("ChatService -> verifyQueryOwnership -> msgObjID: %+v", msgObjID)
	log.Printf("ChatService -> verifyQueryOwnership -> queryObjID: %+v", queryObjID)
	log.Printf("ChatService -> verifyQueryOwnership -> msg.ChatID: %+v", msg.ChatID)

	log.Printf("ChatService -> verifyQueryOwnership -> msg: %+v", msg)
	// Find query in message
	var targetQuery *models.Query
	if msg.Queries != nil {
		for _, q := range *msg.Queries {
			if q.ID == queryObjID {
				targetQuery = &q
				break
			}
		}
	}
	if targetQuery == nil {
		return nil, nil, nil, fmt.Errorf("query not found in message")
	}

	return chat, msg, targetQuery, nil
}

// GetSelectedCollections retrieves the selected collections for a chat
// NOTE: This is used for UI display
func (s *chatService) GetSelectedCollections(chatID string) (string, error) {
	log.Printf("ChatService -> GetSelectedCollections -> Starting for chatID: %s", chatID)

	// Convert to ObjectID
	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		log.Printf("ChatService -> GetSelectedCollections -> Error getting chatID: %v", err)
		return "ALL", fmt.Errorf("invalid chat ID format: %v", err)
	}

	// Get chat to get selected collections
	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		log.Printf("ChatService -> GetSelectedCollections -> Error finding chat: %v", err)
		return "ALL", fmt.Errorf("failed to fetch chat: %v", err)
	}

	if chat == nil {
		log.Printf("ChatService -> GetSelectedCollections -> Chat not found for chatID: %s", chatID)
		return "ALL", fmt.Errorf("chat not found")
	}

	log.Printf("ChatService -> GetSelectedCollections -> Selected collections for chatID %s: %s", chatID, chat.SelectedCollections)

	// If SelectedCollections is empty, return "ALL"
	if chat.SelectedCollections == "" {
		return "ALL", nil
	}

	return chat.SelectedCollections, nil
}

// Fetch all tables for a chat
// NOTE: This is used for UI display
func (s *chatService) GetAllTables(ctx context.Context, userID, chatID string) (*dtos.TablesResponse, uint32, error) {
	owner, idErr := primitive.ObjectIDFromHex(userID)
	id, chatErr := primitive.ObjectIDFromHex(chatID)
	if idErr != nil || chatErr != nil {
		return nil, http.StatusBadRequest, ErrSourceForbidden
	}
	candidate, err := s.chatRepo.ReadCurrent(ctx, owner, id)
	if err != nil || candidate == nil {
		return nil, http.StatusNotFound, ErrSourceForbidden
	}
	if queryObjectsEnabled(candidate) {
		return s.getQueryObjectDirectory(ctx, candidate)
	}
	if candidate.SelectedObjects != nil {
		return nil, http.StatusConflict, ErrQueryObjectsDisabled
	}
	log.Printf("ChatService -> GetAllTables -> Starting for chatID: %s", chatID)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	select {
	case <-ctx.Done():
		return nil, http.StatusRequestTimeout, fmt.Errorf("request timed out")
	default:
		// Get chat details first
		chatObjID, err := primitive.ObjectIDFromHex(chatID)
		if err != nil {
			log.Printf("ChatService -> GetAllTables -> Error getting chatID: %v", err)
			return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
		}

		chat, err := s.chatRepo.FindByID(chatObjID)
		if err != nil {
			log.Printf("ChatService -> GetAllTables -> Error finding chat: %v", err)
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to fetch chat: %v", err)
		}

		if chat != nil {
			// Try to decrypt the connection details
			utils.DecryptConnection(&chat.Connection)
		}

		if chat == nil {
			log.Printf("ChatService -> GetAllTables -> Chat not found for chatID: %s", chatID)
			return nil, http.StatusNotFound, fmt.Errorf("chat not found")
		}
		owner, err := primitive.ObjectIDFromHex(userID)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		read, err := s.sourcePolicy().AcquireRead(ctx, owner, chat, chat.Connection)
		if err != nil {
			return nil, http.StatusConflict, err
		}
		defer read.Close()
		ctx = read.Context

		// For spreadsheet and Google Sheets connections with default database name, update it based on tables
		if (chat.Connection.Type == constants.DatabaseTypeSpreadsheet || chat.Connection.Type == constants.DatabaseTypeGoogleSheets) &&
			(chat.Connection.Database == "spreadsheet_db" || chat.Connection.Database == "spreadsheet_data" || chat.Connection.Database == "") {
			log.Printf("ChatService -> GetAllTables -> Spreadsheet connection has default database name, updating it")
			if err := s.updateSpreadsheetDatabaseName(chatID); err != nil {
				log.Printf("ChatService -> GetAllTables -> Failed to update spreadsheet database name: %v", err)
			}
			// Reload the chat to get the updated database name
			chat, err = s.chatRepo.FindByID(chatObjID)
			if err == nil && chat != nil {
				utils.DecryptConnection(&chat.Connection)
			}
		}

		// Get database connection
		dbConn, err := s.dbManager.GetConnectionContext(ctx, chatID)
		if err != nil {
			log.Printf("ChatService -> GetAllTables -> Connection not found, attempting to connect: %v", err)

			// Determine schema name for spreadsheet connections
			schemaName := ""
			if chat.Connection.Type == constants.DatabaseTypeSpreadsheet || chat.Connection.Type == constants.DatabaseTypeGoogleSheets {
				schemaName = fmt.Sprintf("conn_%s", chatID)
			}

			// Connection not found, try to connect with proper config
			config := connectionConfig(chat, chat.Connection)
			config.SchemaName, config.InitialSchemaDeferred = schemaName, true
			if chat.ERPAssociated {
				config.ApplicationName = s.sourcePolicy().applicationName(chat)
			}
			connectErr := s.dbManager.Connect(ctx, chatID, userID, "", config)
			if connectErr != nil {
				log.Printf("ChatService -> GetAllTables -> Failed to connect: %v", connectErr)
				return nil, http.StatusNotFound, fmt.Errorf("failed to establish database connection: %v", connectErr)
			}

			// Try to get connection again after connecting
			dbConn, err = s.dbManager.GetConnectionContext(ctx, chatID)
			if err != nil {
				log.Printf("ChatService -> GetAllTables -> Still failed to get connection after connect: %v", err)
				return nil, http.StatusNotFound, fmt.Errorf("connection established but not ready yet: %v", err)
			}
		}

		// Get connection info
		connInfo, exists := s.dbManager.GetConnectionInfo(chatID)
		if !exists {
			log.Printf("ChatService -> GetAllTables -> Connection info not found")
			return nil, http.StatusNotFound, fmt.Errorf("connection info not found")
		}

		// Convert the selectedCollections string to a slice
		var selectedCollectionsSlice []string
		if chat.SelectedCollections != "ALL" && chat.SelectedCollections != "" {
			selectedCollectionsSlice = strings.Split(chat.SelectedCollections, ",")
		}
		log.Printf("ChatService -> GetAllTables -> Selected collections: %v", selectedCollectionsSlice)

		// Create a map for quick lookup of selected tables
		selectedTablesMap := make(map[string]bool)
		for _, tableName := range selectedCollectionsSlice {
			selectedTablesMap[tableName] = true
		}
		isAllSelected := chat.SelectedCollections == "ALL" || chat.SelectedCollections == ""

		// Get schema manager
		schemaManager := s.dbManager.GetSchemaManager()

		log.Printf("ChatService -> GetAllTables -> Getting schema for chatID -> Database Host, Name, Type: %+v, %+v, %+v", connInfo.Config.Host, connInfo.Config.Database, connInfo.Config.Type)
		// Get schema from database - pass empty slice to get ALL tables
		schema, err := schemaManager.GetSchema(ctx, chatID, dbConn, connInfo.Config.Type, []string{})
		if err != nil {
			log.Printf("ChatService -> GetAllTables -> Error getting schema: %v", err)
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to get schema: %v", err)
		}

		// Convert schema tables to TableInfo objects
		var tables []dtos.TableInfo
		for tableName, tableSchema := range schema.Tables {
			tableInfo := dtos.TableInfo{
				Name:       tableName,
				Columns:    make([]dtos.ColumnInfo, 0, len(tableSchema.Columns)),
				IsSelected: isAllSelected || selectedTablesMap[tableName],
				RowCount:   tableSchema.RowCount,
				SizeBytes:  tableSchema.SizeBytes,
			}

			for columnName, columnInfo := range tableSchema.Columns {
				tableInfo.Columns = append(tableInfo.Columns, dtos.ColumnInfo{
					Name:       columnName,
					Type:       columnInfo.Type,
					IsNullable: columnInfo.IsNullable,
				})
			}

			tables = append(tables, tableInfo)
		}

		// Sort tables by name for consistent output
		sort.Slice(tables, func(i, j int) bool {
			return tables[i].Name < tables[j].Name
		})

		return &dtos.TablesResponse{
			Tables: tables,
		}, http.StatusOK, nil
	}
}

// GetImportMetadata retrieves import metadata for a chat
func (s *chatService) GetImportMetadata(ctx context.Context, userID, chatID string) (*dtos.ImportMetadata, uint32, error) {
	// Verify the chat belongs to the user
	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid user ID format")
	}

	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid chat ID format")
	}

	chat, err := s.chatRepo.FindByID(chatObjID)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, http.StatusNotFound, fmt.Errorf("chat not found")
		}
		return nil, http.StatusInternalServerError, err
	}

	if chat == nil {
		return nil, http.StatusNotFound, fmt.Errorf("chat not found")
	}

	// Verify ownership
	if chat.UserID != userObjID {
		return nil, http.StatusForbidden, fmt.Errorf("unauthorized access to chat")
	}

	// Get Redis repo from dbManager
	redisRepo := s.dbManager.GetRedisRepo()
	if redisRepo == nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("redis not available")
	}

	// Create metadata store and retrieve metadata
	metadataStore := dbmanager.NewImportMetadataStore(redisRepo)
	metadata, err := metadataStore.GetMetadata(chatID)
	if err != nil {
		log.Printf("Error retrieving import metadata: %v", err)
		return nil, http.StatusInternalServerError, err
	}

	if metadata == nil {
		// No metadata found, return empty response
		return nil, http.StatusNotFound, fmt.Errorf("no import metadata found")
	}

	return metadata, http.StatusOK, nil
}

// getModelDisplayName returns the human-readable display name for a model ID
// by looking it up in the SupportedLLMModels constant
func (s *chatService) getModelDisplayName(modelID string) string {
	// Search for the model in the supported models list
	for _, model := range constants.SupportedLLMModels {
		if model.ID == modelID {
			return model.DisplayName
		}
	}

	// Fallback: convert kebab-case to Title Case if model not found
	// e.g., "gpt-4o" -> "Gpt 4o"
	words := strings.Split(modelID, "-")
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(string(word[0])) + word[1:]
		}
	}
	return strings.Join(words, " ")
}
