package di

import (
	"context"
	"fmt"
	"log"
	"yanfachatbi/config"
	"yanfachatbi/internal/apis/handlers"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/erpsyncinstall"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/embedding"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/sourcegate"
	"yanfachatbi/pkg/vectordb"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"go.uber.org/dig"
)

// Only an explicit operator-owned, non-secret deployment document enables the
// approved directory. No request, Chat name or implicit endpoint provisions it.
func deploymentSourceAccess(chats repositories.ChatRepository, client *mongodb.MongoDBClient) (*services.SourceAccessService, error) {
	path := os.Getenv("YANFA_ERP_SOURCE_CONFIG")
	if path == "" {
		return services.NewUnconfiguredSourceAccessService(chats), nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("ERP source configuration requires an absolute path")
	}
	raw, err := erpsyncinstall.ReadPublicFile(path, uint32(os.Geteuid()), 1<<20)
	var cfg config.ERPSyncConfig
	if err != nil || erpsyncinstall.DecodeJSON(raw, &cfg) != nil {
		return nil, fmt.Errorf("invalid ERP source configuration")
	}
	if err := cfg.ValidateDeployment(); err != nil {
		return nil, err
	}
	// Every actual backend lifetime has a distinct reader/recovery identity.
	// The approved deployment label remains the prefix; a restart cannot inherit
	// its predecessor's recovery_verified_instance_id.
	cfg.InstanceID += ":" + uuid.NewString()
	gate, err := sourcegate.New(cfg.LockDirectory, cfg.DeploymentUID)
	if err != nil {
		return nil, err
	}
	repo := repositories.NewERPSourceRepository(client.GetCollectionByName("erp_sources").Database())
	policy, err := services.NewSourceAccessService(repo, gate, cfg, chats)
	if err != nil {
		gate.Close()
		return nil, err
	}
	return policy, nil
}

var DiContainer *dig.Container

func Initialize() {
	DiContainer = dig.New()

	// Initialize MongoDB
	dbConfig := mongodb.MongoDbConfigModel{
		ConnectionUrl: config.Env.MongoURI,
		DatabaseName:  config.Env.MongoDatabaseName,
	}
	mongodbClient := mongodb.InitializeDatabaseConnection(dbConfig)

	// Initialize Redis
	redisClient, err := redis.RedisClient(config.Env.RedisHost, config.Env.RedisPort, config.Env.RedisUsername, config.Env.RedisPassword)
	if err != nil {
		log.Fatalf("Failed to initialize Redis client: %v", err)
	}

	// Initialize services and repositories
	redisRepo := redis.NewRedisRepositories(redisClient)
	jwtService := utils.NewJWTService(
		config.Env.JWTSecret,
		time.Millisecond*time.Duration(config.Env.JWTExpirationMilliseconds),
		time.Millisecond*time.Duration(config.Env.JWTRefreshExpirationMilliseconds),
	)

	// Initialize token repository
	tokenRepo := repositories.NewTokenRepository(redisRepo)

	// Initialize chat repository with Redis support
	chatRepo := repositories.NewChatRepository(mongodbClient, redisRepo)

	// Initialize visualization repository with Redis support
	visualizationRepo := repositories.NewVisualizationRepository(mongodbClient, redisRepo)

	// Initialize dashboard repository with Redis support
	dashboardRepo := repositories.NewDashboardRepository(mongodbClient, redisRepo)

	// Provide all dependencies to the container
	if err := DiContainer.Provide(func() *mongodb.MongoDBClient { return mongodbClient }); err != nil {
		log.Fatalf("Failed to provide MongoDB client: %v", err)
	}

	if err := DiContainer.Provide(func() redis.IRedisRepositories { return redisRepo }); err != nil {
		log.Fatalf("Failed to provide Redis repositories: %v", err)
	}

	if err := DiContainer.Provide(func() utils.JWTService { return jwtService }); err != nil {
		log.Fatalf("Failed to provide JWT service: %v", err)
	}

	if err := DiContainer.Provide(func() repositories.ChatRepository { return chatRepo }); err != nil {
		log.Fatalf("Failed to provide chat repository: %v", err)
	}

	if err := DiContainer.Provide(func() repositories.IVisualizationRepository { return visualizationRepo }); err != nil {
		log.Fatalf("Failed to provide visualization repository: %v", err)
	}

	if err := DiContainer.Provide(func() repositories.DashboardRepository { return dashboardRepo }); err != nil {
		log.Fatalf("Failed to provide dashboard repository: %v", err)
	}

	if err := DiContainer.Provide(func() (*services.SourceAccessService, error) {
		return deploymentSourceAccess(chatRepo, mongodbClient)
	}); err != nil {
		log.Fatalf("Failed to provide source access policy: %v", err)
	}

	if err := DiContainer.Provide(func(sourceAccess *services.SourceAccessService) *services.DataSyncService {
		return services.NewDeploymentDataSyncService(sourceAccess, repositories.NewDataSyncRepository(mongodbClient.GetCollectionByName("data_sync_jobs").Database()))
	}); err != nil {
		log.Fatal("Failed to provide data sync service")
	}
	if err := DiContainer.Provide(func(service *services.DataSyncService) *handlers.DataSyncHandler {
		return handlers.NewDataSyncHandler(service)
	}); err != nil {
		log.Fatal("Failed to provide data sync handler")
	}

	if err := DiContainer.Provide(func(redisRepo redis.IRedisRepositories, sourceAccess *services.SourceAccessService) (*dbmanager.Manager, error) {
		encryptionKey := config.Env.SchemaEncryptionKey
		manager, err := dbmanager.NewManager(redisRepo, encryptionKey)
		if err != nil {
			log.Fatalf("Failed to provide DB manager: %v", err)
		}
		manager.SetSourceAccess(sourceAccess)
		// Register database drivers
		manager.RegisterDriver(constants.DatabaseTypePostgreSQL, dbmanager.NewPostgresDriver())
		manager.RegisterDriver(constants.DatabaseTypeYugabyteDB, dbmanager.NewPostgresDriver())  // Use same driver for both
		manager.RegisterDriver(constants.DatabaseTypeTimescaleDB, dbmanager.NewPostgresDriver()) // TimescaleDB is a PostgreSQL extension
		manager.RegisterDriver(constants.DatabaseTypeMySQL, dbmanager.NewMySQLDriver())
		manager.RegisterDriver(constants.DatabaseTypeStarRocks, dbmanager.NewMySQLDriver()) // StarRocks uses MySQL wire protocol
		manager.RegisterDriver(constants.DatabaseTypeClickhouse, dbmanager.NewClickHouseDriver())
		manager.RegisterDriver(constants.DatabaseTypeMongoDB, dbmanager.NewMongoDBDriver())
		manager.RegisterDriver(constants.DatabaseTypeSpreadsheet, dbmanager.NewSpreadsheetDriver())

		// Register schema fetchers
		manager.RegisterFetcher(constants.DatabaseTypePostgreSQL, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return &dbmanager.PostgresDriver{}
		})
		manager.RegisterFetcher(constants.DatabaseTypeYugabyteDB, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return &dbmanager.PostgresDriver{}
		})
		manager.RegisterFetcher(constants.DatabaseTypeTimescaleDB, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return &dbmanager.PostgresDriver{} // TimescaleDB is a PostgreSQL extension
		})
		manager.RegisterFetcher(constants.DatabaseTypeMySQL, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return dbmanager.NewMySQLSchemaFetcher(db)
		})
		manager.RegisterFetcher(constants.DatabaseTypeStarRocks, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return dbmanager.NewMySQLSchemaFetcher(db) // StarRocks is MySQL-wire-compatible
		})
		manager.RegisterFetcher(constants.DatabaseTypeClickhouse, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return &dbmanager.ClickHouseDriver{}
		})
		manager.RegisterFetcher(constants.DatabaseTypeMongoDB, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return &dbmanager.MongoDBDriver{}
		})
		manager.RegisterFetcher(constants.DatabaseTypeSpreadsheet, func(db dbmanager.DBExecutor) dbmanager.SchemaFetcher {
			return &dbmanager.PostgresDriver{}
		})

		return manager, nil
	}); err != nil {
		log.Fatalf("Failed to provide DB manager: %v", err)
	}

	if err := DiContainer.Provide(func(db *mongodb.MongoDBClient, redisRepo redis.IRedisRepositories) repositories.UserRepository {
		return repositories.NewUserRepository(db, redisRepo)
	}); err != nil {
		log.Fatalf("Failed to provide user repository: %v", err)
	}

	if err := DiContainer.Provide(func() repositories.TokenRepository { return tokenRepo }); err != nil {
		log.Fatalf("Failed to provide token repository: %v", err)
	}

	// Provide email service
	if err := DiContainer.Provide(func() services.EmailService {
		return services.NewEmailService()
	}); err != nil {
		log.Fatalf("Failed to provide email service: %v", err)
	}

	// Provide Google OAuth service
	if err := DiContainer.Provide(func() services.GoogleOAuthService {
		return services.NewGoogleOAuthService()
	}); err != nil {
		log.Fatalf("Failed to provide Google OAuth service: %v", err)
	}

	// Provide waitlist repository
	if err := DiContainer.Provide(func(db *mongodb.MongoDBClient) *repositories.WaitlistRepository {
		return repositories.NewWaitlistRepository(db.Client.Database(db.Config.DatabaseName))
	}); err != nil {
		log.Fatalf("Failed to provide waitlist repository: %v", err)
	}

	// Provide waitlist service
	if err := DiContainer.Provide(func(waitlistRepo *repositories.WaitlistRepository, emailService services.EmailService) *services.WaitlistService {
		return services.NewWaitlistService(waitlistRepo, emailService)
	}); err != nil {
		log.Fatalf("Failed to provide waitlist service: %v", err)
	}

	// Provide waitlist handler
	if err := DiContainer.Provide(func(waitlistService *services.WaitlistService) *handlers.WaitlistHandler {
		return handlers.NewWaitlistHandler(waitlistService)
	}); err != nil {
		log.Fatalf("Failed to provide waitlist handler: %v", err)
	}

	// Provide services
	if err := DiContainer.Provide(func(userRepo repositories.UserRepository, tokenRepo repositories.TokenRepository, jwt utils.JWTService, emailService services.EmailService, googleOAuthService services.GoogleOAuthService) services.AuthService {
		return services.NewAuthService(userRepo, jwt, tokenRepo, emailService, googleOAuthService)
	}); err != nil {
		log.Fatalf("Failed to provide auth service: %v", err)
	}

	// Conversation summary repository (context compaction)
	if err := DiContainer.Provide(func(mongoClient *mongodb.MongoDBClient) repositories.ContextSummaryRepository {
		return repositories.NewContextSummaryRepository(mongoClient)
	}); err != nil {
		log.Fatalf("Failed to provide context summary repository: %v", err)
	}

	// Model provider repository (persisted OpenAI-compatible configurations)
	if err := DiContainer.Provide(func(mongoClient *mongodb.MongoDBClient) repositories.ModelProviderRepository {
		return repositories.NewModelProviderRepository(mongoClient)
	}); err != nil {
		log.Fatalf("Failed to provide model provider repository: %v", err)
	}

	// LLM Manager: bootstrap the default provider from env on first boot, then
	// build one OpenAI-compatible client per enabled provider.
	if err := DiContainer.Provide(func(repo repositories.ModelProviderRepository) (*llm.Manager, error) {
		ctx := context.Background()
		crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("model provider encryption key: %w", err)
		}
		if err := repo.EnsureIndexes(ctx); err != nil {
			log.Printf("Warning: model provider indexes: %v", err)
		}
		if err := bootstrapModelProvider(ctx, repo, crypto); err != nil {
			return nil, err
		}
		providers, err := repo.List(ctx)
		if err != nil {
			return nil, err
		}
		clients, order, err := buildLLMClients(providers, providerKeys(providers, crypto))
		if err != nil {
			return nil, err
		}
		manager := llm.NewManager()
		manager.ReplaceClients(clients)
		if len(order) > 0 {
			manager.SetDefaultClient(order[0])
			log.Printf("LLM Manager -> %d provider(s) registered, default: %s", len(order), order[0])
		} else {
			log.Printf("Warning: no LLM provider configured; chats cannot generate responses until an admin adds one")
		}
		return manager, nil
	}); err != nil {
		log.Fatalf("Failed to provide LLM manager: %v", err)
	}

	// Model provider service: CRUD, probing, and hot rebuild of LLM clients.
	if err := DiContainer.Provide(func(repo repositories.ModelProviderRepository, manager *llm.Manager) *services.ModelProviderService {
		crypto, _ := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
		rebuild := func(providers []*models.ModelProvider, keys map[string]string) error {
			clients, order, err := buildLLMClients(providers, keys)
			if err != nil {
				return err
			}
			manager.ReplaceClients(clients)
			if len(order) > 0 {
				manager.SetDefaultClient(order[0])
			}
			return nil
		}
		return services.NewModelProviderService(repo, crypto, rebuild)
	}); err != nil {
		log.Fatalf("Failed to provide model provider service: %v", err)
	}

	// Model provider handlers
	if err := DiContainer.Provide(func(providerService *services.ModelProviderService, userRepo repositories.UserRepository) *handlers.ModelProviderHandler {
		return handlers.NewModelProviderHandler(providerService, userRepo)
	}); err != nil {
		log.Fatalf("Failed to provide model provider handler: %v", err)
	}
	if err := DiContainer.Provide(func(providerService *services.ModelProviderService) *handlers.LLMModelsHandler {
		return handlers.NewLLMModelsHandler(providerService)
	}); err != nil {
		log.Fatalf("Failed to provide llm models handler: %v", err)
	}

	// Knowledge Base Repository
	if err := DiContainer.Provide(func(
		mongoClient *mongodb.MongoDBClient,
	) repositories.KnowledgeBaseRepository {
		return repositories.NewKnowledgeBaseRepository(mongoClient)
	}); err != nil {
		log.Fatalf("Failed to provide knowledge base repository: %v", err)
	}

	assistantRuns := mongodbClient.GetCollectionByName("assistant_runs")
	if err := repositories.EnsureAssistantRunIndexes(context.Background(), assistantRuns); err != nil {
		log.Fatalf("Failed to create assistant run indexes: %v", err)
	}
	if err := DiContainer.Provide(func() (repositories.AssistantRunRepository, error) {
		crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
		if err != nil {
			return nil, err
		}
		cipher, err := repositories.NewAESReportCipher(crypto)
		if err != nil {
			return nil, err
		}
		return repositories.NewAssistantRunRepository(assistantRuns, cipher), nil
	}); err != nil {
		log.Fatalf("Failed to provide assistant run repository: %v", err)
	}
	if err := DiContainer.Provide(func(
		chats repositories.ChatRepository,
		runs repositories.AssistantRunRepository,
		llmManager *llm.Manager,
		sourceAccess *services.SourceAccessService,
		providerService *services.ModelProviderService,
	) (services.AssistantService, error) {
		crypto, err := utils.NewAESGCMCrypto(config.Env.SchemaEncryptionKey)
		if err != nil {
			return nil, err
		}
		cipher, err := repositories.NewAESReportCipher(crypto)
		if err != nil {
			return nil, err
		}
		demoEnabled := config.Env.Environment == "DEVELOPMENT" && os.Getenv("YANFA_ASSISTANT_DEMO") == "true"
		opts := []services.AssistantServiceOption{
			services.WithAssistantDemoIsolation(demoEnabled),
			services.WithAssistantERPQuantityPilot(os.Getenv("YANFA_ASSISTANT_ERP_QUANTITY_SOURCE_ID"), os.Getenv("YANFA_ASSISTANT_ERP_QUANTITY_SCOPE_SHA256")),
		}
		if model := selectAssistantExplanationModel(llmManager, providerService, chats); model != nil {
			opts = append(opts, services.WithAssistantExplanationModel(model))
		}
		return services.NewAssistantService(
			services.NewAssistantChatRepository(chats),
			services.NewAssistantMessageRepository(chats),
			runs,
			cipher,
			services.NewAssistantFixtureCatalog(),
			services.NewAssistantFixtureSource(),
			nil,
			sourceAccess,
			opts...,
		), nil
	}); err != nil {
		log.Fatalf("Failed to provide assistant service: %v", err)
	}

	// Update Chat Service provider to include DB manager setup
	if err := DiContainer.Provide(func(
		chatRepo repositories.ChatRepository,
		visualizationRepo repositories.IVisualizationRepository,
		dbManager *dbmanager.Manager,
		llmManager *llm.Manager,
		redisRepo redis.IRedisRepositories,
		mongoClient *mongodb.MongoDBClient,
		kbRepo repositories.KnowledgeBaseRepository,
		dashboardRepo repositories.DashboardRepository,
		assistant services.AssistantService,
		sourceAccess *services.SourceAccessService,
	) (services.ChatService, error) {
		// The default client is the first enabled configured provider.
		llmClient, _ := llmManager.DefaultClient()
		if llmClient == nil {
			log.Printf("Warning: No LLM provider configured. Add one in model settings.")
		}

		// Initialize embedding provider (can be nil if no API keys)
		// EMBEDDING_API_KEY/EMBEDDING_BASE_URL let embeddings use a different
		// OpenAI-compatible endpoint than the LLM (falls back to provider keys).
		embeddingAPIKey := config.Env.EmbeddingAPIKey
		if embeddingAPIKey == "" {
			embeddingAPIKey = config.Env.OpenAIAPIKey
		}
		embeddingProvider, embErr := embedding.AutoDetectProvider(
			embeddingAPIKey,
			config.Env.EmbeddingProvider,
			config.Env.EmbeddingModel,
			config.Env.EmbeddingBaseURL,
		)
		if embErr != nil {
			log.Printf("Warning: Failed to initialize embedding provider: %v", embErr)
		}
		if embeddingProvider != nil {
			log.Printf("Embedding provider initialized: %s/%s (dimension: %d)",
				embeddingProvider.GetProviderName(),
				embeddingProvider.GetModelName(),
				embeddingProvider.GetDimension(),
			)
		} else {
			log.Printf("Warning: No embedding provider available. RAG pipeline disabled.")
		}

		vectorizationSvc, vectorErr := initializeVectorization(config.Env, embeddingProvider)
		if vectorErr != nil {
			return nil, vectorErr
		}

		chatService := services.NewChatService(chatRepo, dbManager, llmClient, llmManager, redisRepo, visualizationRepo, vectorizationSvc, kbRepo, dashboardRepo, assistant, sourceAccess)
		// Wire the admin-configured catalogue so "slug/model" selections resolve.
		if providerService, providerErr := GetModelProviderService(); providerErr == nil {
			chatService.SetModelProviderService(providerService)
		} else {
			log.Printf("Warning: model provider service unavailable to chat service: %v", providerErr)
		}
		_ = DiContainer.Invoke(func(repo repositories.ContextSummaryRepository) {
			chatService.SetContextSummaryRepository(repo)
		})

		// Set chat service as stream handler for DB manager
		dbManager.SetStreamHandler(chatService)

		// Set chat service in auth service
		err = DiContainer.Invoke(func(authService services.AuthService) {
			authService.SetChatService(chatService)
		})
		if err != nil {
			log.Fatalf("Failed to set chat service in auth service: %v", err)
		}
		return chatService, nil
	}); err != nil {
		log.Fatalf("Failed to provide chat service: %v", err)
	}

	if err := DiContainer.Provide(func(redisRepo redis.IRedisRepositories) services.GitHubService {
		return services.NewGitHubService(redisRepo)
	}); err != nil {
		log.Fatalf("Failed to provide github handler: %v", err)
	}

	// Dashboard Service
	if err := DiContainer.Provide(func(
		dashboardRepo repositories.DashboardRepository,
		chatRepo repositories.ChatRepository,
		dbManager *dbmanager.Manager,
		llmManager *llm.Manager,
		redisRepo redis.IRedisRepositories,
		kbRepo repositories.KnowledgeBaseRepository,
		sourceAccess *services.SourceAccessService,
		chatService services.ChatService,
	) services.DashboardService {
		provider, _ := chatService.(services.QueryObjectContextProvider)
		dashboardService := services.NewDashboardService(dashboardRepo, chatRepo, dbManager, llmManager, redisRepo, kbRepo, sourceAccess, services.DashboardGenerationOptions{CurrentProjection: services.NewDashboardProjectionLoader(provider)})
		if providerService, providerErr := GetModelProviderService(); providerErr == nil {
			dashboardService.SetModelProviderService(providerService)
		} else {
			log.Printf("Warning: model provider service unavailable to dashboard service: %v", providerErr)
		}
		return dashboardService
	}); err != nil {
		log.Fatalf("Failed to provide dashboard service: %v", err)
	}

	// Dashboard Import/Export Service
	if err := DiContainer.Provide(func(
		dashboardRepo repositories.DashboardRepository,
		chatRepo repositories.ChatRepository,
		dbManager *dbmanager.Manager,
	) services.DashboardImportExportService {
		return services.NewDashboardImportExportService(dashboardRepo, chatRepo, dbManager)
	}); err != nil {
		log.Fatalf("Failed to provide dashboard import/export service: %v", err)
	}

	// Provide handlers
	if err := DiContainer.Provide(func(authService services.AuthService) *handlers.AuthHandler {
		return handlers.NewAuthHandler(authService)
	}); err != nil {
		log.Fatalf("Failed to provide auth handler: %v", err)
	}

	if err := DiContainer.Provide(func(githubService services.GitHubService) *handlers.GitHubHandler {
		return handlers.NewGitHubHandler(githubService)
	}); err != nil {
		log.Fatalf("Failed to provide github handler: %v", err)
	}

	// Chat Handler
	if err := DiContainer.Provide(func(
		chatService services.ChatService,
	) *handlers.ChatHandler {
		handler := handlers.NewChatHandler(chatService)
		chatService.SetStreamHandler(handler)
		return handler
	}); err != nil {
		log.Fatalf("Failed to provide chat handler: %v", err)
	}

	// Visualization Handler
	if err := DiContainer.Provide(func(
		chatService services.ChatService,
	) *handlers.VisualizationHandler {
		return handlers.NewVisualizationHandler(chatService)
	}); err != nil {
		log.Fatalf("Failed to provide visualization handler: %v", err)
	}

	// Dashboard Handler
	if err := DiContainer.Provide(func(
		dashboardService services.DashboardService,
		dashboardImportExportService services.DashboardImportExportService,
		chatService services.ChatService,
		chatHandler *handlers.ChatHandler,
	) *handlers.DashboardHandler {
		// Reuse chat handler as stream handler so dashboard events
		// flow through the same SSE connection
		dashboardService.SetStreamHandler(chatHandler)
		return handlers.NewDashboardHandler(dashboardService, dashboardImportExportService)
	}); err != nil {
		log.Fatalf("Failed to provide dashboard handler: %v", err)
	}

	if err := DiContainer.Provide(func(service services.AssistantService) *handlers.AssistantHandler {
		return handlers.NewAssistantHandler(service)
	}); err != nil {
		log.Fatalf("Failed to provide assistant handler: %v", err)
	}
}

func GetDataSyncHandler() (*handlers.DataSyncHandler, error) {
	var handler *handlers.DataSyncHandler
	err := DiContainer.Invoke(func(h *handlers.DataSyncHandler) { handler = h })
	return handler, err
}

func GetDataSyncService() (*services.DataSyncService, error) {
	var service *services.DataSyncService
	err := DiContainer.Invoke(func(s *services.DataSyncService) { service = s })
	return service, err
}

// GetAuthHandler retrieves the AuthHandler from the DI container
func GetAuthHandler() (*handlers.AuthHandler, error) {
	var handler *handlers.AuthHandler
	err := DiContainer.Invoke(func(h *handlers.AuthHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetChatHandler retrieves the ChatHandler from the DI container
func GetChatHandler() (*handlers.ChatHandler, error) {
	var handler *handlers.ChatHandler
	err := DiContainer.Invoke(func(h *handlers.ChatHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetAssistantHandler retrieves the AssistantHandler from the DI container.
func GetAssistantHandler() (*handlers.AssistantHandler, error) {
	var handler *handlers.AssistantHandler
	err := DiContainer.Invoke(func(h *handlers.AssistantHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetGitHubHandler retrieves the GitHubHandler from the DI container

// GetModelProviderHandler resolves the admin provider management handler.

// GetModelProviderService resolves the provider configuration service.
func GetModelProviderService() (*services.ModelProviderService, error) {
	var service *services.ModelProviderService
	err := DiContainer.Invoke(func(s *services.ModelProviderService) {
		service = s
	})
	if err != nil {
		return nil, err
	}
	return service, nil
}

func GetModelProviderHandler() (*handlers.ModelProviderHandler, error) {
	var handler *handlers.ModelProviderHandler
	err := DiContainer.Invoke(func(h *handlers.ModelProviderHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetLLMModelsHandler resolves the catalogue handler.
func GetLLMModelsHandler() (*handlers.LLMModelsHandler, error) {
	var handler *handlers.LLMModelsHandler
	err := DiContainer.Invoke(func(h *handlers.LLMModelsHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

func GetGitHubHandler() (*handlers.GitHubHandler, error) {
	var handler *handlers.GitHubHandler
	err := DiContainer.Invoke(func(h *handlers.GitHubHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetWaitlistHandler retrieves the WaitlistHandler from the DI container
func GetWaitlistHandler() (*handlers.WaitlistHandler, error) {
	var handler *handlers.WaitlistHandler
	err := DiContainer.Invoke(func(h *handlers.WaitlistHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetVisualizationHandler retrieves the VisualizationHandler from the DI container
func GetVisualizationHandler() (*handlers.VisualizationHandler, error) {
	var handler *handlers.VisualizationHandler
	err := DiContainer.Invoke(func(h *handlers.VisualizationHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetDashboardHandler retrieves the DashboardHandler from the DI container
func GetDashboardHandler() (*handlers.DashboardHandler, error) {
	var handler *handlers.DashboardHandler
	err := DiContainer.Invoke(func(h *handlers.DashboardHandler) {
		handler = h
	})
	if err != nil {
		return nil, err
	}
	return handler, nil
}

// GetChatService retrieves the ChatService from the DI container
func GetChatService() (services.ChatService, error) {
	var service services.ChatService
	err := DiContainer.Invoke(func(s services.ChatService) {
		service = s
	})
	if err != nil {
		return nil, err
	}
	return service, nil
}

// GetChatRepository retrieves the ChatRepository from the DI container
func GetChatRepository() (repositories.ChatRepository, error) {
	var repo repositories.ChatRepository
	err := DiContainer.Invoke(func(r repositories.ChatRepository) {
		repo = r
	})
	if err != nil {
		return nil, err
	}
	return repo, nil
}

// GetVisualizationRepository retrieves the VisualizationRepository from the DI container
func GetVisualizationRepository() (repositories.IVisualizationRepository, error) {
	var repo repositories.IVisualizationRepository
	err := DiContainer.Invoke(func(r repositories.IVisualizationRepository) {
		repo = r
	})
	if err != nil {
		return nil, err
	}
	return repo, nil
}

// GetDBManager retrieves the DB Manager from the DI container
func GetDBManager() (*dbmanager.Manager, error) {
	var manager *dbmanager.Manager
	err := DiContainer.Invoke(func(m *dbmanager.Manager) {
		manager = m
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

// selectAssistantExplanationModel returns the explanation model adapter only
// when an LLM client is explicitly configured, or nil otherwise. It mirrors the
// chat service's default provider preference (OpenAI, then Gemini) and never
// hardcodes a model or a fixed output: the adapter uses the client's default
// model and decodes whatever the provider returns.
func selectAssistantExplanationModel(llmManager *llm.Manager, providers *services.ModelProviderService, chats repositories.ChatRepository) services.AssistantExplanationModel {
	if llmManager == nil {
		return nil
	}
	client, err := llmManager.DefaultClient()
	if err != nil || client == nil {
		return nil
	}
	return services.NewAssistantExplanationModel(client, services.WithExplanationCatalogue(llmManager, providers, chats))
}

// initializeVectorization preserves best-effort legacy startup, but an opted-in
// collection pair must be configured and fully ready before DI exposes chat.
func initializeVectorization(env config.Environment, provider embedding.Provider) (services.VectorizationService, error) {
	names, err := vectordb.ResolveCollectionNames(env.EmbeddingSchemaCollection, env.EmbeddingMessageCollection)
	if err != nil {
		return nil, err
	}
	strict := !names.IsDefault()
	if strict && (provider == nil || env.QdrantHost == "") {
		return nil, fmt.Errorf("custom embedding collections require an embedding provider and QDRANT_HOST")
	}
	var vectorClient vectordb.Client
	if env.QdrantHost != "" {
		port := env.QdrantPort
		if port == "" {
			port = "6334"
		}
		client, err := vectordb.NewQdrantClient(vectordb.QdrantConfig{
			Host: env.QdrantHost, Port: port, APIKey: env.QdrantAPIKey, UseTLS: env.QdrantUseTLS,
			SchemaCollection: names.Schema, MessageCollection: names.Messages,
		})
		if err != nil {
			if strict {
				return nil, fmt.Errorf("failed to initialize custom embedding collections: %w", err)
			}
			log.Printf("Warning: Failed to connect to Qdrant: %v", err)
		} else {
			vectorClient = client
			log.Printf("Qdrant client initialized: %s:%s", env.QdrantHost, port)
		}
	} else {
		log.Printf("Warning: Qdrant not configured (QDRANT_HOST empty). RAG pipeline disabled.")
	}
	service := services.NewVectorizationService(provider, vectorClient)
	if service == nil {
		return nil, nil
	}
	if strict {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := service.EnsureReady(ctx); err != nil {
			vectorClient.Close()
			return nil, fmt.Errorf("custom embedding collections are not ready: %w", err)
		}
	} else {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := service.EnsureReady(ctx); err != nil {
				log.Printf("Warning: Failed to ensure Qdrant collection: %v", err)
			}
		}()
	}
	return service, nil
}
