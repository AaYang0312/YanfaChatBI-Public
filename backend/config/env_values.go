package config

import (
	"fmt"
	"os"
	"strconv"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/vectordb"

	"github.com/joho/godotenv"
)

type Environment struct {
	QueryObjectsEnabled bool
	// Context compaction and result feedback (see docs/specs/context-compaction.md)
	ContextCompactionEnabled     bool
	ContextCompactionThreshold   float64
	ContextCompactionKeepTurns   int
	ContextCompactionMinMessages int
	ContextSummaryMaxTokens      int
	ContextSummaryModel          string
	RagAfterCompactionTopK       int
	ResultDigestMaxChars         int
	ResultDigestTotalMaxChars    int
	// Server configs
	IsDocker                     bool
	Port                         string
	Environment                  string
	MaxChatsPerUser              int
	CorsAllowedOrigin            string
	LandingPageCorsAllowedOrigin string
	ExampleDatabaseType          string
	ExampleDatabaseHost          string
	ExampleDatabasePort          string
	ExampleDatabaseName          string
	ExampleDatabaseUsername      string
	ExampleDatabasePassword      string
	// Auth configs
	SchemaEncryptionKey              string
	JWTSecret                        string
	JWTExpirationMilliseconds        int
	JWTRefreshExpirationMilliseconds int
	AdminUser                        string
	AdminPassword                    string
	DefaultLLMModel                  string

	// Database configs
	MongoURI          string
	MongoDatabaseName string

	// Redis configs
	RedisHost     string
	RedisPort     string
	RedisUsername string
	RedisPassword string

	// OpenAI configs (API key only, models defined in constants)
	OpenAIAPIKey  string
	OpenAIBaseURL string // Optional OpenAI-compatible base URL override (proxy endpoints)
	// Gemini configs (API key only, models defined in constants)

	// Claude configs (API key only, models defined in constants)

	// Ollama configs (base URL only, models defined in constants)

	// SMTP Email configs
	SMTPHost      string
	SMTPPort      int
	SMTPSecure    bool
	SMTPUser      string
	SMTPPassword  string
	SMTPFromName  string
	SMTPFromEmail string

	// Spreadsheet PostgreSQL configs
	SpreadsheetPostgresHost      string
	SpreadsheetPostgresPort      string
	SpreadsheetPostgresDatabase  string
	SpreadsheetPostgresUsername  string
	SpreadsheetPostgresPassword  string
	SpreadsheetPostgresSSLMode   string
	SpreadsheetDataEncryptionKey string

	// Google OAuth configs
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string

	// Qdrant Vector DB configs
	QdrantHost   string
	QdrantPort   string
	QdrantAPIKey string
	QdrantUseTLS bool

	// Embedding configs
	EmbeddingProvider               string // "openai" or "gemini" — auto-detected if empty
	EmbeddingModel                  string // e.g., "text-embedding-3-small" or "text-embedding-004"
	EmbeddingAPIKey                 string // Optional dedicated API key for embeddings (falls back to provider key)
	EmbeddingBaseURL                string // Optional OpenAI-compatible base URL for the embedding endpoint
	EmbeddingSchemaCollection       string
	EmbeddingMessageCollection      string
	EmbeddingObjectSchemaCollection string
}

var Env Environment

// LoadEnv loads environment variables from .env file if present
// and validates required variables
func LoadEnv() error {
	// Check if running in Docker
	Env.IsDocker = os.Getenv("IS_DOCKER") == "true"

	// Load .env file only if not running in Docker
	if !Env.IsDocker {
		if err := godotenv.Load(); err != nil {
			fmt.Printf("Warning: .env file not found: %v\n", err)
		}
	}

	// Server configs
	Env.Port = getEnvWithDefault("PORT", "3000")
	Env.Environment = getEnvWithDefault("ENVIRONMENT", "DEVELOPMENT")
	Env.MaxChatsPerUser = getIntEnvWithDefault("MAX_CHATS_PER_USER", 1)
	Env.CorsAllowedOrigin = getEnvWithDefault("CORS_ALLOWED_ORIGIN", "http://localhost:5173")
	Env.LandingPageCorsAllowedOrigin = getEnvWithDefault("LANDING_PAGE_CORS_ALLOWED_ORIGIN", "")
	// Auth configs
	Env.SchemaEncryptionKey = getRequiredEnv("SCHEMA_ENCRYPTION_KEY", "yanfachatbi_schema_encryption_key")
	Env.JWTSecret = getRequiredEnv("JWT_SECRET", "yanfachatbi_jwt_secret")
	Env.JWTExpirationMilliseconds = getIntEnvWithDefault("JWT_EXPIRATION_MILLISECONDS",
		getIntEnvWithDefault("USER_JWT_EXPIRATION_MILLISECONDS", 1000*60*60*24*10)) // 10 days default
	Env.JWTRefreshExpirationMilliseconds = getIntEnvWithDefault("JWT_REFRESH_EXPIRATION_MILLISECONDS",
		getIntEnvWithDefault("USER_JWT_REFRESH_EXPIRATION_MILLISECONDS", 1000*60*60*24*30)) // 30 days default
	Env.AdminUser = getRenamedEnv("YANFA_ADMIN_USERNAME", "NEOBASE_ADMIN_USERNAME", "admin")
	Env.AdminPassword = getRenamedEnv("YANFA_ADMIN_PASSWORD", "NEOBASE_ADMIN_PASSWORD", "change-me")

	// Database configs
	Env.MongoURI = getRenamedEnv("YANFA_MONGODB_URI", "NEOBASE_MONGODB_URI", "mongodb://localhost:27017/yanfachatbi")
	Env.MongoDatabaseName = getRenamedEnv("YANFA_MONGODB_NAME", "NEOBASE_MONGODB_NAME", "yanfachatbi")
	Env.RedisHost = getRenamedEnv("YANFA_REDIS_HOST", "NEOBASE_REDIS_HOST", "localhost")
	Env.RedisPort = getRenamedEnv("YANFA_REDIS_PORT", "NEOBASE_REDIS_PORT", "6379")
	Env.RedisUsername = getRenamedEnv("YANFA_REDIS_USERNAME", "NEOBASE_REDIS_USERNAME", "")
	Env.RedisPassword = getRenamedEnv("YANFA_REDIS_PASSWORD", "NEOBASE_REDIS_PASSWORD", "default")

	// Example DB For Development Environment
	Env.ExampleDatabaseType = getRequiredEnv("EXAMPLE_DB_TYPE", "postgres")
	Env.ExampleDatabaseHost = getRequiredEnv("EXAMPLE_DB_HOST", "localhost")
	Env.ExampleDatabasePort = getRequiredEnv("EXAMPLE_DB_PORT", "5432")
	Env.ExampleDatabaseName = getRequiredEnv("EXAMPLE_DB_NAME", "postgres")
	Env.ExampleDatabaseUsername = getRequiredEnv("EXAMPLE_DB_USERNAME", "")
	Env.ExampleDatabasePassword = getRequiredEnv("EXAMPLE_DB_PASSWORD", "")

	// LLM configs
	Env.DefaultLLMModel = getEnvWithDefault("DEFAULT_LLM_MODEL", "")

	// OpenAI configs - API key only, models defined in constants/supported_models.go
	Env.OpenAIAPIKey = getRequiredEnv("OPENAI_API_KEY", "")
	// Optional OpenAI-compatible base URL override (proxy endpoints); empty = official OpenAI API
	Env.OpenAIBaseURL = getEnvWithDefault("OPENAI_BASE_URL", "")

	// Gemini configs - API key only, models defined in constants/supported_models.go

	// Claude configs - API key only, models defined in constants/supported_models.go

	// Ollama configs - base URL only, models defined in constants/supported_models.go
	// Empty by default - only enable when explicitly configured

	// SMTP Email configs
	Env.SMTPHost = getEnvWithDefault("SMTP_HOST", "")
	Env.SMTPPort = getIntEnvWithDefault("SMTP_PORT", 587)
	Env.SMTPSecure = getEnvWithDefault("SMTP_SECURE", "false") == "true"
	Env.SMTPUser = getEnvWithDefault("SMTP_USER", "")
	Env.SMTPPassword = getEnvWithDefault("SMTP_PASSWORD", "")
	Env.SMTPFromName = getEnvWithDefault("SMTP_FROM_NAME", "YanfaChatBI")
	Env.SMTPFromEmail = getEnvWithDefault("SMTP_FROM_EMAIL", Env.SMTPUser)

	// Spreadsheet PostgreSQL configs
	Env.SpreadsheetPostgresHost = getEnvWithDefault("SPREADSHEET_POSTGRES_HOST", "localhost")
	Env.SpreadsheetPostgresPort = getEnvWithDefault("SPREADSHEET_POSTGRES_PORT", "5432")
	Env.SpreadsheetPostgresDatabase = getEnvWithDefault("SPREADSHEET_POSTGRES_DATABASE", "yanfachatbi_spreadsheet_storage")
	Env.SpreadsheetPostgresUsername = getEnvWithDefault("SPREADSHEET_POSTGRES_USERNAME", "yanfachatbi_spreadsheet")
	Env.SpreadsheetPostgresPassword = getRequiredEnv("SPREADSHEET_POSTGRES_PASSWORD", "")
	Env.SpreadsheetPostgresSSLMode = getEnvWithDefault("SPREADSHEET_POSTGRES_SSL_MODE", "disable")
	Env.SpreadsheetDataEncryptionKey = getRequiredEnv("SPREADSHEET_DATA_ENCRYPTION_KEY", "spreadsheet_data_key_32bytes")

	// Google OAuth configs (used for both authentication and Google Sheets integration)
	Env.GoogleClientID = getEnvWithDefault("GOOGLE_CLIENT_ID", "")
	Env.GoogleClientSecret = getEnvWithDefault("GOOGLE_CLIENT_SECRET", "")
	Env.GoogleRedirectURL = getEnvWithDefault("GOOGLE_REDIRECT_URL", "http://localhost:5173/auth/google/callback")

	// Qdrant Vector DB configs
	Env.QdrantHost = getEnvWithDefault("QDRANT_HOST", "localhost")
	Env.QdrantPort = getEnvWithDefault("QDRANT_PORT", "6334")
	Env.QdrantAPIKey = getEnvWithDefault("QDRANT_API_KEY", "")
	Env.QdrantUseTLS = getEnvWithDefault("QDRANT_USE_TLS", "false") == "true"

	// Embedding configs — auto-detect provider from available API keys if not set
	Env.QueryObjectsEnabled = getEnvWithDefault("QUERY_OBJECTS_ENABLED", "false") == "true"
	Env.ContextCompactionEnabled = getEnvWithDefault("CONTEXT_COMPACTION_ENABLED", "true") == "true"
	Env.ContextCompactionThreshold = getFloatEnvWithDefault("CONTEXT_COMPACTION_THRESHOLD", 0.75)
	Env.ContextCompactionKeepTurns = getIntEnvWithDefault("CONTEXT_COMPACTION_KEEP_TURNS", 6)
	Env.ContextCompactionMinMessages = getIntEnvWithDefault("CONTEXT_COMPACTION_MIN_MESSAGES", 4)
	Env.ContextSummaryMaxTokens = getIntEnvWithDefault("CONTEXT_SUMMARY_MAX_TOKENS", 4000)
	Env.ContextSummaryModel = getEnvWithDefault("CONTEXT_SUMMARY_MODEL", "")
	Env.RagAfterCompactionTopK = getIntEnvWithDefault("RAG_AFTER_COMPACTION_TOP_K", 3)
	Env.ResultDigestMaxChars = getIntEnvWithDefault("RESULT_DIGEST_MAX_CHARS", 4000)
	Env.ResultDigestTotalMaxChars = getIntEnvWithDefault("RESULT_DIGEST_TOTAL_MAX_CHARS", 8000)
	Env.EmbeddingProvider = getEnvWithDefault("EMBEDDING_PROVIDER", "")
	Env.EmbeddingModel = getEnvWithDefault("EMBEDDING_MODEL", "")
	Env.EmbeddingAPIKey = getEnvWithDefault("EMBEDDING_API_KEY", "")
	Env.EmbeddingBaseURL = getEnvWithDefault("EMBEDDING_BASE_URL", "")
	Env.EmbeddingSchemaCollection = getEnvWithDefault("EMBEDDING_SCHEMA_COLLECTION", "")
	Env.EmbeddingMessageCollection = getEnvWithDefault("EMBEDDING_MESSAGE_COLLECTION", "")
	Env.EmbeddingObjectSchemaCollection = getEnvWithDefault("EMBEDDING_OBJECT_SCHEMA_COLLECTION", "")

	return validateConfig()
}

// Helper functions to get environment variables with defaults and validation
func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getRenamedEnv reads the new name and falls back to the legacy NEOBASE_*
// name so existing deployments keep starting after the rename.
func getRenamedEnv(key, legacyKey, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	if legacyKey != "" {
		if value := os.Getenv(legacyKey); value != "" {
			return value
		}
	}
	return defaultValue
}

func getRequiredEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getIntEnvWithDefault(key string, defaultValue int) int {
	strValue := os.Getenv(key)
	if strValue == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(strValue)
	if err != nil {
		fmt.Printf("Warning: Invalid value for %s, using default: %d\n", key, defaultValue)
		return defaultValue
	}
	return value
}

func getFloatEnvWithDefault(key string, defaultValue float64) float64 {
	strValue := os.Getenv(key)
	if strValue == "" {
		return defaultValue
	}

	value, err := strconv.ParseFloat(strValue, 64)
	if err != nil {
		fmt.Printf("Warning: Invalid value for %s, using default: %f\n", key, defaultValue)
		return defaultValue
	}
	return value
}

func validateConfig() error {
	objectCollection, objectErr := vectordb.ResolveObjectCollectionName(Env.EmbeddingSchemaCollection, Env.EmbeddingMessageCollection, Env.EmbeddingObjectSchemaCollection)
	if objectErr != nil {
		return objectErr
	}
	Env.EmbeddingObjectSchemaCollection = objectCollection
	if _, err := vectordb.ResolveCollectionNames(Env.EmbeddingSchemaCollection, Env.EmbeddingMessageCollection); err != nil {
		return err
	}
	// Validate MongoDB URI format
	if !isValidURI(Env.MongoURI) {
		return fmt.Errorf("invalid MONGODB_URI format: %s", Env.MongoURI)
	}

	// Validate JWT expiration
	if Env.JWTExpirationMilliseconds <= 0 {
		return fmt.Errorf("JWT_EXPIRATION_MILLISECONDS must be positive, got: %d", Env.JWTExpirationMilliseconds)
	}

	if Env.AdminUser == "yanfachatbi-admin" || Env.AdminPassword == "yanfachatbi-password" ||
		(Env.AdminUser == "admin" && Env.AdminPassword == "change-me") {
		return fmt.Errorf("default credentials must be changed before starting")
	}

	// Validate Spreadsheet PostgreSQL configs
	if len(Env.SpreadsheetDataEncryptionKey) != 32 {
		return fmt.Errorf("SPREADSHEET_DATA_ENCRYPTION_KEY must be exactly 32 bytes for AES-GCM encryption, got: %d bytes", len(Env.SpreadsheetDataEncryptionKey))
	}

	// Validate SSL mode
	validSSLModes := map[string]bool{"disable": true, "require": true, "verify-ca": true, "verify-full": true}
	if !validSSLModes[Env.SpreadsheetPostgresSSLMode] {
		return fmt.Errorf("invalid SPREADSHEET_POSTGRES_SSL_MODE: %s, must be one of: disable, require, verify-ca, verify-full", Env.SpreadsheetPostgresSSLMode)
	}

	// Disable models for providers without API keys configured
	constants.DisableUnavailableProviders(Env.OpenAIAPIKey)

	// Restrict the catalogue to models reachable through the configured endpoint.
	constants.ApplyOpenAIProxyOverride(Env.OpenAIBaseURL)

	// Validate Default LLM Model if specified
	if Env.DefaultLLMModel != "" {
		model := constants.GetLLMModel(Env.DefaultLLMModel)
		if model == nil {
			return fmt.Errorf("invalid DEFAULT_LLM_MODEL: %s, model not found in supported models", Env.DefaultLLMModel)
		}
		if !model.IsEnabled {
			return fmt.Errorf("DEFAULT_LLM_MODEL: %s is disabled", Env.DefaultLLMModel)
		}
	}

	// Validate & resolve Embedding Provider + Model against supported constants
	embeddingOpenAIKey := Env.EmbeddingAPIKey
	if embeddingOpenAIKey == "" {
		embeddingOpenAIKey = Env.OpenAIAPIKey
	}
	resolvedProvider, resolvedModel, embErr := constants.ValidateEmbeddingConfig(
		Env.EmbeddingProvider,
		Env.EmbeddingModel,
		embeddingOpenAIKey,
	)
	if embErr != nil {
		return embErr
	}
	// Write back the resolved (possibly auto-detected) values
	Env.EmbeddingProvider = resolvedProvider
	Env.EmbeddingModel = resolvedModel

	// Log embedding initialization status
	constants.LogEmbeddingInitialization(Env.EmbeddingProvider, Env.EmbeddingModel, embeddingOpenAIKey)

	return nil
}

func isValidURI(uri string) bool {
	return len(uri) > 0 && (len(uri) > 10)
}
