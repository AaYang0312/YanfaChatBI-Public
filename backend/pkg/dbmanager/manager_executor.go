package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	// Database drivers
	mysqldriver "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"

	"crypto/tls"
	"crypto/x509"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/utils"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type QueryExecution struct {
	QueryID     string
	ChatID      string
	MessageID   string
	StartTime   time.Time
	IsExecuting bool
	IsRollback  bool
	Tx          Transaction // Changed from *sql.Tx to Transaction
	CancelFunc  context.CancelFunc
}

func (m *Manager) CancelQueryExecution(chatID, messageID, queryID, streamID string) {
	m.executionMu.Lock()
	defer m.executionMu.Unlock()

	if execution, exists := m.activeExecutions[queryExecutionKey(chatID, messageID, queryID, streamID)]; exists {
		log.Printf("Cancelling query execution for streamID: %s", streamID)

		// The execution owner joins SQL and rolls back before dropping its permit.
		execution.CancelFunc()
	}
}

func queryExecutionKey(chatID, messageID, queryID, streamID string) string {
	return chatID + ":" + messageID + ":" + queryID + ":" + streamID
}

// ExecuteQuery executes a query and returns the result, synchronous, no SSE events are sent, findCount is used to strictly get the number/count of records that the query returns
func (m *Manager) ExecuteQuery(ctx context.Context, chatID, messageID, queryID, streamID string, query string, queryType string, isRollback bool, findCount bool) (*QueryExecutionResult, *dtos.QueryError) {
	m.executionMu.Lock()

	// Create cancellable context with timeout
	execCtx, cancel := context.WithTimeout(ctx, 1*time.Minute) // 1 minute timeout

	// Track execution
	execution := &QueryExecution{
		ChatID:      chatID,
		QueryID:     queryID,
		MessageID:   messageID,
		StartTime:   time.Now(),
		IsExecuting: true,
		IsRollback:  isRollback,
		CancelFunc:  cancel,
	}
	key := queryExecutionKey(chatID, messageID, queryID, streamID)
	if streamID != "" {
		if _, exists := m.activeExecutions[key]; exists {
			m.executionMu.Unlock()
			cancel()
			return nil, &dtos.QueryError{Code: "QUERY_ALREADY_EXECUTING", Message: "query is already executing"}
		}
		m.activeExecutions[key] = execution
	}
	m.executionMu.Unlock()

	// Ensure cleanup
	defer func() {
		m.executionMu.Lock()
		if m.activeExecutions[key] == execution {
			delete(m.activeExecutions, key)
		}
		m.executionMu.Unlock()
		cancel()
	}()

	// Get connection and driver
	m.mu.RLock()
	conn, exists := m.connections[chatID]
	m.mu.RUnlock()
	if !exists || conn.Status != StatusConnected {
		return nil, &dtos.QueryError{
			Code:    "NO_CONNECTION_FOUND",
			Message: "no connection found",
			Details: "No connection found for chat ID: " + chatID,
		}
	}
	if err := m.requireSource(ctx, conn.Config); err != nil {
		return nil, &dtos.QueryError{Code: "SOURCE_ACCESS_DENIED", Message: err.Error()}
	}
	if m.isERP(conn.Config) {
		chat, connection := conn.Config.sourceSnapshot()
		admitted, release, err := m.sourceAccess.AcquireSource(execCtx, chat, connection)
		if err != nil {
			return nil, &dtos.QueryError{Code: "SOURCE_ACCESS_DENIED", Message: err.Error()}
		}
		defer release()
		execCtx = admitted
	}

	driver, exists := m.drivers[conn.Config.Type]
	if !exists {
		return nil, &dtos.QueryError{
			Code:    "NO_DRIVER_FOUND",
			Message: "no driver found",
			Details: "No driver found for type: " + conn.Config.Type,
		}
	}

	log.Printf("Manager -> ExecuteQuery -> Driver: %v", driver)

	// Validate query safety before executing
	if !isRollback { // Skip validation for rollback queries
		validator := GetValidatorForDatabase(conn.Config.Type)
		if validator != nil {
			// Try to get cached schema for better validation
			var tableMetadata map[string]TableSchema
			if m.schemaManager != nil {
				// Retrieve cached schema from storage
				storage, err := m.schemaManager.storageService.Retrieve(execCtx, chatID)
				if err == nil && storage != nil && storage.FullSchema != nil {
					tableMetadata = storage.FullSchema.Tables
					log.Printf("Manager -> ExecuteQuery -> Using cached schema with %d tables for validation", len(tableMetadata))
				} else {
					log.Printf("Manager -> ExecuteQuery -> No cached schema available, proceeding with basic validation")
					tableMetadata = make(map[string]TableSchema)
				}
			} else {
				tableMetadata = make(map[string]TableSchema)
			}

			// Validate the query
			if err := validator.ValidateSafety(query, queryType, tableMetadata); err != nil {
				log.Printf("Manager -> ExecuteQuery -> Query safety validation failed: %v", err)
				return nil, &dtos.QueryError{
					Code:    "SAFETY_VIOLATION",
					Message: "Query blocked by safety validation",
					Details: err.Error(),
				}
			}
			log.Printf("Manager -> ExecuteQuery -> Query passed safety validation")
		}
	}

	// Begin transaction
	tx := driver.BeginTx(execCtx, conn)
	if tx == nil {
		return nil, &dtos.QueryError{
			Code:    "FAILED_TO_START_TRANSACTION",
			Message: "failed to start transaction",
			Details: "Failed to start transaction",
		}
	}

	// Check if transaction has an error (MongoDB transaction might return a non-nil transaction with an error)
	if mongoTx, ok := tx.(*MongoDBTransaction); ok && mongoTx.Error != nil {
		log.Printf("Manager -> ExecuteQuery -> MongoDB transaction error: %v", mongoTx.Error)
		return nil, &dtos.QueryError{
			Code:    "FAILED_TO_START_TRANSACTION",
			Message: "failed to start transaction",
			Details: mongoTx.Error.Error(),
		}
	}

	execution.Tx = tx

	// Execute query with proper cancellation handling
	var result *QueryExecutionResult
	done := make(chan struct{})
	var queryErr *dtos.QueryError

	go func() {
		defer close(done)
		log.Printf("Manager -> ExecuteQuery -> Executing query: %v", query)
		var err error
		result, err = tx.ExecuteQuery(execCtx, query)
		if err != nil {
			log.Printf("Manager -> ExecuteQuery -> Error executing query: %v", err)
			result = &QueryExecutionResult{
				Error: &dtos.QueryError{
					Message: err.Error(),
					Code:    "EXECUTION_ERROR",
				},
			}
		}
		if result.Error != nil {
			queryErr = result.Error
		}
	}()

	select {
	case <-execCtx.Done():
		<-done
		if err := tx.Rollback(); err != nil {
			log.Printf("Error rolling back transaction: %v", err)
		}
		if execCtx.Err() == context.DeadlineExceeded {
			return nil, &dtos.QueryError{
				Code:    "QUERY_EXECUTION_TIMED_OUT",
				Message: "query execution timed out",
				Details: "Query execution timed out",
			}
		}
		return nil, &dtos.QueryError{
			Code:    "QUERY_EXECUTION_CANCELLED",
			Message: "query execution cancelled",
			Details: "Query execution cancelled",
		}

	case <-done:
		if queryErr != nil {
			if err := tx.Rollback(); err != nil {
				log.Printf("Error rolling back transaction: %v", err)
			}
			return result, queryErr
		}
		if err := tx.Commit(); err != nil {
			return nil, &dtos.QueryError{
				Code:    "QUERY_EXECUTION_FAILED",
				Message: "query execution failed",
				Details: err.Error(),
			}
		}
		log.Println("Manager -> ExecuteQuery -> Commit completed:")
		log.Printf("Manager -> ExecuteQuery -> Query type: %v", queryType)

		if conn.OnSchemaChange != nil && (queryType == "DDL" || queryType == "ALTER" || queryType == "DROP" || queryType == "CREATE_COLLECTION" || queryType == "DROP_COLLECTION") {
			if err := conn.OnSchemaChange(execCtx, conn.ChatID); err != nil {
				return nil, &dtos.QueryError{Code: "SOURCE_ACCESS_DENIED", Message: err.Error()}
			}
		}

		return result, nil
	}
}

// TestConnection tests if the provided credentials are valid without creating a persistent connection
func (m *Manager) TestConnection(ctx context.Context, config *ConnectionConfig) error {
	if err := m.requireSource(ctx, *config); err != nil {
		return err
	}
	if m.isERP(*config) {
		chat, connection := config.sourceSnapshot()
		admitted, release, err := m.sourceAccess.AcquireSource(ctx, chat, connection)
		if err != nil {
			return err
		}
		defer release()
		ctx = admitted
	}
	if config.Type == constants.DatabaseTypePostgreSQL || config.Type == constants.DatabaseTypeYugabyteDB || config.Type == constants.DatabaseTypeTimescaleDB {
		probe := *config
		probe.operationContext = ctx
		if probe.Port == nil || *probe.Port == "" {
			port := "5432"
			if probe.Type == constants.DatabaseTypeYugabyteDB {
				port = "5433"
			}
			probe.Port = &port
		}
		driver := &PostgresDriver{}
		connection, err := driver.Connect(probe)
		if err != nil {
			return err
		}
		if err := driver.Disconnect(connection); err != nil {
			return err
		}
		return ctx.Err()
	}
	var tempFiles []string

	// Test SSH tunnel if configured
	if config.SSHEnabled && config.SSHHost != nil && config.SSHPort != nil && config.SSHUsername != nil && config.SSHPrivateKey != nil {
		log.Printf("Manager -> TestConnection -> Testing SSH tunnel connection")
		sshPassphrase := ""
		if config.SSHPassphrase != nil {
			sshPassphrase = *config.SSHPassphrase
		}
		if err := TestSSHTunnel(*config.SSHHost, *config.SSHPort, *config.SSHUsername, *config.SSHPrivateKey, sshPassphrase); err != nil {
			return fmt.Errorf("SSH tunnel test failed: %v", err)
		}
		log.Printf("Manager -> TestConnection -> SSH tunnel test passed")
	}

	switch config.Type {
	case constants.DatabaseTypeMySQL, constants.DatabaseTypeStarRocks:
		var dsn string
		port := "3306" // Default port for MySQL / StarRocks (MySQL FE query port)
		if config.Type == constants.DatabaseTypeStarRocks {
			port = "9030" // StarRocks FE MySQL query port
		}

		if config.Port != nil && *config.Port != "" {
			port = *config.Port
		}

		// Base connection parameters
		if config.Password != nil {
			dsn = fmt.Sprintf(
				"%s:%s@tcp(%s:%s)/%s",
				*config.Username, *config.Password, config.Host, port, config.Database,
			)
		} else {
			dsn = fmt.Sprintf(
				"%s@tcp(%s:%s)/%s",
				*config.Username, config.Host, port, config.Database,
			)
		}

		// Add parameters
		dsn += "?parseTime=true"

		// Configure SSL/TLS
		if config.UseSSL {
			// Create a unique TLS config name
			tlsConfigName := fmt.Sprintf("custom-test-%d", time.Now().UnixNano())

			// Fetch certificates from URLs
			certPath, keyPath, rootCertPath, certTempFiles, err := utils.PrepareCertificatesFromURLs(*config.SSLCertURL, *config.SSLKeyURL, *config.SSLRootCertURL)
			if err != nil {
				return err
			}

			// Track temporary files for cleanup
			tempFiles = certTempFiles

			// Create TLS config
			tlsConfig := &tls.Config{
				ServerName: config.Host,
				MinVersion: tls.VersionTLS12,
			}

			// Add client certificates if provided
			if certPath != "" && keyPath != "" {
				cert, err := tls.LoadX509KeyPair(certPath, keyPath)
				if err != nil {
					// Clean up temporary files
					for _, file := range tempFiles {
						os.Remove(file)
					}
					return fmt.Errorf("failed to load client certificates: %v", err)
				}
				tlsConfig.Certificates = []tls.Certificate{cert}
			}

			// Add CA certificate if provided
			if rootCertPath != "" {
				rootCertPool := x509.NewCertPool()
				pem, err := ioutil.ReadFile(rootCertPath)
				if err != nil {
					// Clean up temporary files
					for _, file := range tempFiles {
						os.Remove(file)
					}
					return fmt.Errorf("failed to read CA certificate: %v", err)
				}
				if ok := rootCertPool.AppendCertsFromPEM(pem); !ok {
					// Clean up temporary files
					for _, file := range tempFiles {
						os.Remove(file)
					}
					return fmt.Errorf("failed to append CA certificate")
				}
				tlsConfig.RootCAs = rootCertPool
			}

			// Register TLS config
			mysqldriver.RegisterTLSConfig(tlsConfigName, tlsConfig)

			// Add TLS config to DSN
			dsn += "&tls=" + tlsConfigName
		}

		// Open connection
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			// Clean up temporary files
			for _, file := range tempFiles {
				os.Remove(file)
			}
			return fmt.Errorf("failed to create connection: %v", err)
		}

		// Test connection
		err = db.PingContext(ctx)

		// Close connection
		db.Close()

		// Clean up temporary files
		for _, file := range tempFiles {
			os.Remove(file)
		}

		if err != nil {
			return fmt.Errorf("failed to connect to database: %v", err)
		}

		return nil

	case constants.DatabaseTypeClickhouse:
		var dsn string
		port := "9000" // Default port for ClickHouse

		if config.Port != nil && *config.Port != "" {
			port = *config.Port
		}

		// Base connection parameters
		protocol := "tcp"

		// Configure SSL/TLS
		if config.UseSSL {
			// Fetch certificates from URLs
			_, _, _, certTempFiles, err := utils.PrepareCertificatesFromURLs(*config.SSLCertURL, *config.SSLKeyURL, *config.SSLRootCertURL)
			if err != nil {
				return err
			}

			// Track temporary files for cleanup
			tempFiles = certTempFiles

			// Use secure protocol
			protocol = "https"
		}

		// Build DSN
		if config.Password != nil {
			dsn = fmt.Sprintf("%s://%s:%s@%s:%s/%s",
				protocol, *config.Username, *config.Password, config.Host, port, config.Database)
		} else {
			dsn = fmt.Sprintf("%s://%s@%s:%s/%s",
				protocol, *config.Username, config.Host, port, config.Database)
		}

		// Add parameters
		dsn += "?dial_timeout=10s&read_timeout=20s"

		// Add secure parameter if using SSL
		if config.UseSSL {
			dsn += "&secure=true"
		}

		// Open connection
		db, err := sql.Open("clickhouse", dsn)
		if err != nil {
			// Clean up temporary files
			for _, file := range tempFiles {
				os.Remove(file)
			}
			return fmt.Errorf("failed to create connection: %v", err)
		}

		// Test connection
		err = db.PingContext(ctx)

		// Close connection
		db.Close()

		// Clean up temporary files
		for _, file := range tempFiles {
			os.Remove(file)
		}

		if err != nil {
			return fmt.Errorf("failed to connect to database: %v", err)
		}

		return nil

	case constants.DatabaseTypeMongoDB:
		var port string
		if config.Port != nil && *config.Port != "" {
			port = *config.Port
		} else {
			port = "27017" // Default port for MongoDB
		}
		log.Printf("DBManager -> TestConnection -> Testing MongoDB connection at %s:%s", config.Host, port)

		var uri string

		// Check if we're using SRV records (mongodb+srv://)
		isSRV := strings.Contains(config.Host, ".mongodb.net")
		protocol := "mongodb"
		if isSRV {
			protocol = "mongodb+srv"
		}

		// Validate port value if not using SRV
		if !isSRV && config.Port != nil {
			// Log the port value for debugging
			log.Printf("DBManager -> TestConnection -> Port value before validation: %v", *config.Port)

			// Check if port is empty
			if *config.Port == "" {
				log.Printf("DBManager -> TestConnection -> Port is empty, using default port 27017")
			} else {
				port = *config.Port

				// Only validate port as numeric if it doesn't contain base64 characters
				// (which would indicate it's encrypted)
				if !strings.Contains(port, "+") && !strings.Contains(port, "/") && !strings.Contains(port, "=") {
					// Verify port is numeric
					if _, err := strconv.Atoi(port); err != nil {
						log.Printf("DBManager -> TestConnection -> Non-numeric port value: %v, might be encrypted", port)
						// Don't return error for potentially encrypted ports
					}
				}
			}
		}

		// Base connection parameters with authentication
		if config.Username != nil && *config.Username != "" {
			// URL encode username and password to handle special characters
			encodedUsername := url.QueryEscape(*config.Username)
			encodedPassword := url.QueryEscape(*config.Password)

			if isSRV {
				// For SRV records, don't include port
				uri = fmt.Sprintf("%s://%s:%s@%s/%s",
					protocol, encodedUsername, encodedPassword, config.Host, config.Database)
			} else {
				// Include port for standard connections
				uri = fmt.Sprintf("%s://%s:%s@%s:%s/%s",
					protocol, encodedUsername, encodedPassword, config.Host, port, config.Database)
			}
		} else {
			// Without authentication
			if isSRV {
				// For SRV records, don't include port
				uri = fmt.Sprintf("%s://%s/%s", protocol, config.Host, config.Database)
			} else {
				// Include port for standard connections
				uri = fmt.Sprintf("%s://%s:%s/%s", protocol, config.Host, port, config.Database)
			}
		}

		// Add connection options
		if isSRV {
			uri += "?retryWrites=true&w=majority"
		}

		// Add authSource parameter if specified
		if config.AuthDatabase != nil && *config.AuthDatabase != "" {
			if strings.Contains(uri, "?") {
				uri += "&authSource=" + url.QueryEscape(*config.AuthDatabase)
			} else {
				uri += "?authSource=" + url.QueryEscape(*config.AuthDatabase)
			}
		}

		// Log the final URI (with sensitive parts masked)
		maskedUri := uri
		if config.Password != nil && *config.Password != "" {
			maskedUri = strings.Replace(maskedUri, *config.Password, "********", -1)
		}
		log.Printf("DBManager -> TestConnection -> Connection URI: %s", maskedUri)

		// Configure client options
		clientOptions := options.Client().ApplyURI(uri)

		// Configure SSL/TLS
		if config.UseSSL {
			// Fetch certificates from URLs
			certPath, keyPath, rootCertPath, certTempFiles, err := utils.PrepareCertificatesFromURLs(*config.SSLCertURL, *config.SSLKeyURL, *config.SSLRootCertURL)
			if err != nil {
				return err
			}

			// Track temporary files for cleanup
			tempFiles = certTempFiles

			// Configure TLS
			tlsConfig := &tls.Config{
				InsecureSkipVerify: false, // Default: verify certificates
			}

			// Apply SSL mode if specified
			if config.SSLMode != nil {
				switch *config.SSLMode {
				case "disable":
					// Don't use TLS at all
					// MongoDB driver doesn't have a direct SetTLS(nil) method,
					// so we'll skip setting TLS config at all
					goto skipTLSConfig
				case "require":
					// Require encryption but don't verify server certificates
					tlsConfig.InsecureSkipVerify = true
				case "verify-ca":
					// Verify server certificate but not hostname
					tlsConfig.InsecureSkipVerify = false
					// Clear ServerName to skip hostname verification
					tlsConfig.ServerName = ""
				case "verify-full":
					// Full verification including hostname
					tlsConfig.InsecureSkipVerify = false
					tlsConfig.ServerName = config.Host
				}
			}

			// Add client certificates if provided
			if certPath != "" && keyPath != "" {
				cert, err := tls.LoadX509KeyPair(certPath, keyPath)
				if err != nil {
					// Clean up temporary files
					for _, file := range tempFiles {
						os.Remove(file)
					}
					return fmt.Errorf("failed to load client certificates: %v", err)
				}
				tlsConfig.Certificates = []tls.Certificate{cert}
			}

			// Add root CA if provided
			if rootCertPath != "" {
				rootCA, err := os.ReadFile(rootCertPath)
				if err != nil {
					// Clean up temporary files
					for _, file := range tempFiles {
						os.Remove(file)
					}
					return fmt.Errorf("failed to read root CA: %v", err)
				}

				rootCertPool := x509.NewCertPool()
				if ok := rootCertPool.AppendCertsFromPEM(rootCA); !ok {
					// Clean up temporary files
					for _, file := range tempFiles {
						os.Remove(file)
					}
					return fmt.Errorf("failed to parse root CA certificate")
				}

				tlsConfig.RootCAs = rootCertPool
			}

			clientOptions.SetTLSConfig(tlsConfig)
		}

	skipTLSConfig:
		// Connect to MongoDB with timeout
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		client, err := mongo.Connect(ctx, clientOptions)
		if err != nil {
			// Clean up temporary files
			for _, file := range tempFiles {
				os.Remove(file)
			}
			log.Printf("DBManager -> TestConnection -> Error connecting to MongoDB: %v", err)
			return fmt.Errorf("failed to connect to MongoDB: %v", err)
		}

		// Ping the database to verify connection
		err = client.Ping(ctx, readpref.Primary())

		// Disconnect regardless of ping result
		client.Disconnect(context.WithoutCancel(ctx))

		// Clean up temporary files
		for _, file := range tempFiles {
			os.Remove(file)
		}

		if err != nil {
			log.Printf("DBManager -> TestConnection -> Error pinging MongoDB: %v", err)
			return fmt.Errorf("failed to ping MongoDB: %v", err)
		}

		log.Printf("DBManager -> TestConnection -> Successfully connected to MongoDB")
		return nil

	default:
		return fmt.Errorf("unsupported data source type: %s", config.Type)
	}
}

// FormatSchemaWithExamples formats the schema with example records for LLM
func (m *Manager) FormatSchemaWithExamples(ctx context.Context, chatID string, selectedCollections []string) (string, error) {
	log.Printf("DBManager -> FormatSchemaWithExamples -> Starting for chatID: %s with selected collections: %v", chatID, selectedCollections)

	// Get connection with read lock to ensure thread safety
	m.mu.RLock()
	conn, exists := m.connections[chatID]
	m.mu.RUnlock()

	if !exists {
		log.Printf("DBManager -> FormatSchemaWithExamples -> Connection not found for chatID: %s", chatID)
		return "", fmt.Errorf("connection not found for chat ID: %s", chatID)
	}

	// Get database executor
	db, err := m.GetConnectionContext(ctx, chatID)
	if err != nil {
		log.Printf("DBManager -> FormatSchemaWithExamples -> Error getting executor: %v", err)
		return "", fmt.Errorf("failed to get database executor: %w", err)
	}

	// Use schema manager to format schema with examples and selected collections
	formattedSchema, err := m.schemaManager.FormatSchemaWithExamplesAndCollections(ctx, chatID, db, conn.Config.Type, selectedCollections)
	if err != nil {
		log.Printf("DBManager -> FormatSchemaWithExamples -> Error formatting schema: %v", err)
		return "", fmt.Errorf("failed to format schema with examples: %w", err)
	}

	log.Printf("DBManager -> FormatSchemaWithExamples -> Successfully formatted schema for chatID: %s", chatID)
	return formattedSchema, nil
}

// GetSchemaWithExamples gets the schema with example records
func (m *Manager) GetSchemaWithExamples(ctx context.Context, chatID string, selectedCollections []string) (*SchemaStorage, error) {
	log.Printf("DBManager -> GetSchemaWithExamples -> Starting for chatID: %s with selected collections: %v", chatID, selectedCollections)

	// Get connection with read lock to ensure thread safety
	m.mu.RLock()
	conn, exists := m.connections[chatID]
	m.mu.RUnlock()

	if !exists {
		log.Printf("DBManager -> GetSchemaWithExamples -> Connection not found for chatID: %s", chatID)
		return nil, fmt.Errorf("connection not found for chat ID: %s", chatID)
	}

	// Get database executor
	db, err := m.GetConnectionContext(ctx, chatID)
	if err != nil {
		log.Printf("DBManager -> GetSchemaWithExamples -> Error getting executor: %v", err)
		return nil, fmt.Errorf("failed to get database executor: %w", err)
	}

	// Use schema manager to get schema with examples
	storage, err := m.schemaManager.GetSchemaWithExamples(ctx, chatID, db, conn.Config.Type, selectedCollections)
	if err != nil {
		log.Printf("DBManager -> GetSchemaWithExamples -> Error getting schema: %v", err)
		return nil, fmt.Errorf("failed to get schema with examples: %w", err)
	}

	log.Printf("DBManager -> GetSchemaWithExamples -> Successfully retrieved schema for chatID: %s", chatID)
	return storage, nil
}

// RefreshSchemaWithExamples refreshes the schema and returns it with example records
func (m *Manager) RefreshSchemaWithExamples(ctx context.Context, chatID string, selectedCollections []string) (string, error) {
	log.Printf("DBManager -> RefreshSchemaWithExamples -> Starting for chatID: %s with selected collections: %v", chatID, selectedCollections)

	// Create a new context with a longer timeout specifically for this operation
	schemaCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()

	// Get connection with read lock to ensure thread safety
	m.mu.RLock()
	conn, exists := m.connections[chatID]
	m.mu.RUnlock()

	if !exists {
		log.Printf("DBManager -> RefreshSchemaWithExamples -> Connection not found for chatID: %s", chatID)
		return "", fmt.Errorf("connection not found for chat ID: %s", chatID)
	}

	// Get database executor
	db, err := m.GetConnectionContext(schemaCtx, chatID)
	if err != nil {
		log.Printf("DBManager -> RefreshSchemaWithExamples -> Error getting executor: %v", err)
		return "", fmt.Errorf("failed to get database executor: %w", err)
	}

	// Clear schema cache to force refresh
	m.schemaManager.ClearSchemaCache(chatID)
	log.Printf("DBManager -> RefreshSchemaWithExamples -> Cleared schema cache for chatID: %s", chatID)

	// Check for context cancellation
	if err := schemaCtx.Err(); err != nil {
		log.Printf("DBManager -> RefreshSchemaWithExamples -> Context cancelled: %v", err)
		return "", fmt.Errorf("operation cancelled: %w", err)
	}

	// Force a fresh schema fetch by directly calling GetSchema first
	log.Printf("DBManager -> RefreshSchemaWithExamples -> Forcing fresh schema fetch for chatID: %s", chatID)

	// Convert selectedCollections to the format expected by GetSchema
	var selectedTables []string
	if len(selectedCollections) == 0 || (len(selectedCollections) == 1 && selectedCollections[0] == "ALL") {
		selectedTables = []string{"ALL"}
	} else {
		selectedTables = selectedCollections
	}

	// Fetch fresh schema directly with the longer timeout context
	freshSchema, err := m.schemaManager.GetSchema(schemaCtx, chatID, db, conn.Config.Type, selectedTables)
	if err != nil {
		log.Printf("DBManager -> RefreshSchemaWithExamples -> Error fetching fresh schema: %v", err)
		return "", fmt.Errorf("failed to fetch fresh schema: %w", err)
	}

	// Store the fresh schema
	err = m.schemaManager.storeSchema(schemaCtx, chatID, freshSchema, db, conn.Config.Type)
	if err != nil {
		log.Printf("DBManager -> RefreshSchemaWithExamples -> Error storing fresh schema: %v", err)
		return "", err
	}

	// Check for context cancellation
	if err := schemaCtx.Err(); err != nil {
		log.Printf("DBManager -> RefreshSchemaWithExamples -> Context cancelled after schema fetch: %v", err)
		return "", fmt.Errorf("operation cancelled: %w", err)
	}

	// Format schema with examples and selected collections
	formattedSchema, err := m.schemaManager.FormatSchemaWithExamplesAndCollections(schemaCtx, chatID, db, conn.Config.Type, selectedCollections)
	if err != nil {
		log.Printf("DBManager -> RefreshSchemaWithExamples -> Error formatting schema: %v", err)
		return "", fmt.Errorf("failed to format schema with examples: %v", err)
	}

	log.Printf("DBManager -> RefreshSchemaWithExamples -> Successfully refreshed schema for chatID: %s (schema length: %d)", chatID, len(formattedSchema))
	return formattedSchema, nil
}
