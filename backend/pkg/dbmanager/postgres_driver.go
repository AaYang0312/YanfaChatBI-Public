package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/sourcegate"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type PostgresDriver struct{}

func NewPostgresDriver() DatabaseDriver {
	return &PostgresDriver{}
}

func (d *PostgresDriver) Connect(config ConnectionConfig) (_ *Connection, connectErr error) {
	ctx := config.operationContext
	if ctx == nil {
		if config.SourceChat != nil && (config.SourceChat.ERPAssociated || config.SourceChat.SourceID != "") {
			return nil, sourcegate.ErrMissing
		}
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sslMode, err := validatePostgresConnection(config)
	if err != nil {
		return nil, err
	}
	var dsn string
	var tempFiles []string
	var sshTunnel *SSHTunnel
	var opened *sql.DB
	defer func() {
		if connectErr != nil {
			if opened != nil {
				opened.Close()
			}
			if sshTunnel != nil {
				closePostgresTunnel(sshTunnel)
			}
			for _, file := range tempFiles {
				os.Remove(file)
			}
		}
	}()

	// Establish SSH tunnel if configured
	if config.SSHEnabled && config.SSHHost != nil && config.SSHPort != nil && config.SSHUsername != nil {
		log.Printf("PostgresDriver -> Connect -> Establishing SSH tunnel for PostgreSQL connection")

		// Determine SSH auth method
		authMethod := SSHAuthMethodPublicKey // Default
		if config.SSHAuthMethod != nil {
			authMethod = ToSSHAuthMethod(*config.SSHAuthMethod)
		}

		var tunnel *SSHTunnel
		var err error

		if authMethod == SSHAuthMethodPassword && config.SSHPassword != nil {
			// Use password-based authentication
			tunnel, err = CreateSSHTunnelWithPassword(*config.SSHHost, *config.SSHPort, *config.SSHUsername, *config.SSHPassword)
		} else {
			// Use public key authentication
			privateKey := ""
			if config.SSHPrivateKey != nil {
				privateKey = *config.SSHPrivateKey
			} else if config.SSHPrivateKeyURL != nil {
				// Load private key from URL
				var err error
				privateKey, err = LoadPrivateKeyFromURL(*config.SSHPrivateKeyURL)
				if err != nil {
					return nil, fmt.Errorf("failed to load SSH private key from URL: %v", err)
				}
			}

			if privateKey == "" {
				return nil, fmt.Errorf("SSH private key is required for public key authentication")
			}

			tunnel, err = CreateSSHTunnel(*config.SSHHost, *config.SSHPort, *config.SSHUsername, privateKey, getValue(config.SSHPassphrase))
		}

		if err != nil {
			return nil, fmt.Errorf("failed to create SSH tunnel: %v", err)
		}

		sshTunnel = tunnel
	}

	// Determine the host to connect to
	connectHost := config.Host

	// Base connection parameters
	baseParams := fmt.Sprintf(
		"host=%s port=%s user=%s dbname=%s",
		quotePostgresConninfo(connectHost),
		quotePostgresConninfo(*config.Port),
		quotePostgresConninfo(*config.Username),
		quotePostgresConninfo(config.Database),
	)

	if config.Password != nil {
		baseParams += " password=" + quotePostgresConninfo(*config.Password)
	}
	if config.ApplicationName != "" {
		baseParams += " application_name=" + quotePostgresConninfo(config.ApplicationName)
	}

	// Every mode, including disable, is explicit; PGSSLMODE cannot override it.
	baseParams += " sslmode=" + quotePostgresConninfo(sslMode)
	if config.UseSSL && sslMode != "disable" {

		certPath, keyPath, rootCertPath, certTempFiles, err := utils.PrepareCertificatesFromURLs(
			getValue(config.SSLCertURL), getValue(config.SSLKeyURL), getValue(config.SSLRootCertURL),
		)
		if err != nil {
			return nil, err
		}

		// Track temporary files for cleanup
		tempFiles = certTempFiles

		// Add certificate paths to connection string
		if certPath != "" {
			baseParams += " sslcert=" + quotePostgresConninfo(certPath)
		}

		if keyPath != "" {
			baseParams += " sslkey=" + quotePostgresConninfo(keyPath)
		}

		if rootCertPath != "" {
			baseParams += " sslrootcert=" + quotePostgresConninfo(rootCertPath)
		}
	}

	dsn = baseParams

	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	if sshTunnel != nil {
		connector.Dialer(postgresTunnelDialer{tunnel: sshTunnel})
	}
	db := sql.OpenDB(connector)
	opened = db
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	// Create GORM DB
	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn: db,
	}), &gorm.Config{DisableAutomaticPing: true})

	if err != nil {
		return nil, fmt.Errorf("failed to create GORM connection: %w", err)
	}

	// Create connection object
	conn := &Connection{
		DB:          gormDB,
		LastUsed:    time.Now(),
		Status:      StatusConnected,
		Config:      config,
		Subscribers: make(map[string]bool),
		SubLock:     sync.RWMutex{},
		TempFiles:   tempFiles,
		SSHTunnel:   sshTunnel,
	}

	return conn, nil
}

var errPostgresConninfoNUL = errors.New("invalid PostgreSQL connection parameter: NUL byte")

func validatePostgresConnection(config ConnectionConfig) (string, error) {
	// Startup parameters are NUL-terminated even when conninfo is correctly quoted.
	for _, value := range [...]string{config.Host, config.Database, config.ApplicationName} {
		if strings.IndexByte(value, 0) >= 0 {
			return "", errPostgresConninfoNUL
		}
	}
	for _, value := range [...]*string{config.Port, config.Username, config.Password} {
		if value != nil && strings.IndexByte(*value, 0) >= 0 {
			return "", errPostgresConninfoNUL
		}
	}
	mode := "disable"
	if config.UseSSL {
		mode = "require"
	}
	if config.SSLMode != nil {
		switch *config.SSLMode {
		case "disable", "require", "verify-ca", "verify-full":
			if config.UseSSL {
				mode = *config.SSLMode
			}
		default:
			return "", fmt.Errorf("invalid PostgreSQL SSL mode")
		}
	}
	return mode, nil
}

// lib/pq conninfo uses backslash escapes, not SQL string literal escapes.
func quotePostgresConninfo(value string) string {
	var quoted strings.Builder
	quoted.Grow(len(value) + strings.Count(value, "\\") + strings.Count(value, "'") + 2)
	quoted.WriteByte('\'')
	for i := range len(value) {
		if value[i] == '\\' || value[i] == '\'' {
			quoted.WriteByte('\\')
		}
		quoted.WriteByte(value[i])
	}
	quoted.WriteByte('\'')
	return quoted.String()
}

func (d *PostgresDriver) Disconnect(conn *Connection) error {
	sqlDB, closeErr := conn.DB.DB()
	if closeErr == nil {
		closeErr = sqlDB.Close()
	}
	var tunnelErr error
	if tunnel, ok := conn.SSHTunnel.(*SSHTunnel); ok {
		tunnelErr = closePostgresTunnel(tunnel)
	}
	for _, file := range conn.TempFiles {
		os.Remove(file)
	}
	return errors.Join(closeErr, tunnelErr)
}

func closePostgresTunnel(tunnel *SSHTunnel) error {
	if tunnel == nil {
		return nil
	}
	err := tunnel.Close()
	if tunnel.ServerConn != nil {
		tunnel.ServerConn.Wait()
	}
	return err
}

type postgresTunnelDialer struct{ tunnel *SSHTunnel }

func (d postgresTunnelDialer) Dial(network, address string) (net.Conn, error) {
	return d.tunnel.ServerConn.Dial(network, address)
}
func (d postgresTunnelDialer) DialTimeout(network, address string, _ time.Duration) (net.Conn, error) {
	return d.Dial(network, address)
}
func (d postgresTunnelDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Legacy SSH channel open is synchronous. Retain until it really returns,
	// then close a late channel before reporting cancellation.
	connection, err := d.Dial(network, address)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func (d *PostgresDriver) Ping(conn *Connection) error {
	sqlDB, err := conn.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

func (d *PostgresDriver) IsAlive(conn *Connection) bool {
	sqlDB, err := conn.DB.DB()
	if err != nil {
		return false
	}
	return sqlDB.Ping() == nil
}

// Modify ExecuteQuery to check for schema changes
func (d *PostgresDriver) ExecuteQuery(ctx context.Context, conn *Connection, query string, queryType string, findCount bool) *QueryExecutionResult {
	startTime := time.Now()
	log.Printf("PostgreSQL/YugabyteDB Driver -> ExecuteQuery -> Query: %v", query)
	sqlDB, err := conn.DB.DB()
	if err != nil {
		log.Printf("PostgreSQL/YugabyteDB Driver -> ExecuteQuery -> Failed to get SQL connection: %v", err)
		return &QueryExecutionResult{
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
			Error: &dtos.QueryError{
				Code:    "FAILED_TO_GET_SQL_CONNECTION",
				Message: "Failed to get SQL connection",
				Details: err.Error(),
			},
		}
	}

	// Split multiple statements
	statements := splitStatements(query)
	var lastResult *sql.Rows
	var lastError error

	log.Printf("PostgreSQL/YugabyteDB Driver -> ExecuteQuery -> Statements: %v", statements)
	// Execute each statement
	for _, stmt := range statements {
		if stmt = strings.TrimSpace(stmt); stmt == "" {
			continue
		}

		lastResult, lastError = sqlDB.QueryContext(ctx, stmt)
		if lastError != nil {
			log.Printf("PostgreSQL/YugabyteDB Driver -> ExecuteQuery -> Query execution failed: %v", lastError)
			return &QueryExecutionResult{
				ExecutionTime: int(time.Since(startTime).Milliseconds()),
				Error: &dtos.QueryError{
					Code:    "QUERY_EXECUTION_FAILED",
					Message: "Query execution failed",
					Details: lastError.Error(),
				},
			}
		}
		if lastResult != nil {
			defer lastResult.Close()
		}
	}

	// Process results from the last statement if it returned rows
	var result *QueryExecutionResult
	if lastResult != nil {
		results, err := processRows(lastResult, startTime)
		if err != nil {
			return &QueryExecutionResult{
				ExecutionTime: int(time.Since(startTime).Milliseconds()),
				Error: &dtos.QueryError{
					Code:    "RESULT_PROCESSING_FAILED",
					Message: err.Error(),
					Details: "Failed to process query results",
				},
			}
		}
		result = &QueryExecutionResult{
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
			Result: map[string]interface{}{
				"results": results,
			},
		}
	} else {
		result = &QueryExecutionResult{
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
			Result: map[string]interface{}{
				"message": "Query executed successfully",
			},
		}
	}

	return result
}

func (d *PostgresDriver) BeginTx(ctx context.Context, conn *Connection) Transaction {
	log.Printf("PostgreSQL/YugabyteDB Driver -> BeginTx -> Starting transaction")

	if conn == nil || conn.DB == nil {
		log.Printf("PostgreSQL/YugabyteDB Driver -> BeginTx: Connection or DB is nil")
		return nil
	}
	if _, err := postgresOperationQuery(ctx, conn.Config, ""); err != nil {
		return nil
	}

	sqlDB, err := conn.DB.DB()
	if err != nil {
		log.Printf("PostgreSQL/YugabyteDB Driver -> BeginTx -> Failed to get SQL connection: %v", err)
		return nil
	}
	if conn.Config.SourceChat != nil && conn.Config.SourceChat.ERPAssociated {
		physical, err := sqlDB.Conn(ctx)
		if err != nil {
			return nil
		}
		tx, err := physical.BeginTx(ctx, nil)
		if err != nil {
			physical.Close()
			return nil
		}
		name, err := postgresOperationName(ctx, conn.Config)
		if err == nil {
			_, err = tx.ExecContext(ctx, "SELECT set_config('application_name', $1, true)", name)
		}
		if err != nil {
			tx.Rollback()
			physical.Close()
			return nil
		}
		return &PostgresTransaction{tx: tx, conn: conn, sqlConn: physical}
	}

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("PostgreSQL/YugabyteDB Driver -> BeginTx -> Failed to begin transaction: %v", err)
		return nil
	}

	// Pass connection to transaction
	return &PostgresTransaction{
		tx:   tx,
		conn: conn,
	}
}

// Improve the GetSchema method to properly detect all tables
func (d *PostgresDriver) GetSchema(ctx context.Context, db DBExecutor, selectedTables []string) (*SchemaInfo, error) {
	objects, err := d.GetQueryObjects(ctx, db)
	if err != nil {
		return nil, err
	}
	return legacyQueryObjectSchema(objects, selectedTables)
}

// Fix the GetTableChecksum method to be more stable and consistent
func (d *PostgresDriver) GetTableChecksum(ctx context.Context, db DBExecutor, table string) (string, error) {
	schema, err := d.GetSchema(ctx, db, []string{table})
	if err != nil {
		return "", err
	}
	for _, t := range schema.Tables {
		return t.Checksum, nil
	}
	return "", fmt.Errorf("object unavailable")
}

// Add FetchExampleRecords method to PostgresDriver
func (d *PostgresDriver) FetchExampleRecords(ctx context.Context, db DBExecutor, table string, limit int) ([]map[string]interface{}, error) {
	// Ensure limit is reasonable
	if limit <= 0 {
		limit = 3 // Default to 3 records
	} else if limit > 10 {
		limit = 10 // Cap at 10 records to avoid large data transfers
	}

	// Build a simple query to fetch example records
	schemaName, bareTable := pgSplitQualifiedTable(table)
	quoted := `"` + strings.ReplaceAll(bareTable, `"`, `""`) + `"`
	if schemaName != "" {
		quoted = `"` + strings.ReplaceAll(schemaName, `"`, `""`) + `".` + quoted
	}
	query := fmt.Sprintf("SELECT * FROM %s LIMIT %d", quoted, limit)

	var records []map[string]interface{}
	var err error
	if bound, ok := db.(*sourceExecutor); ok {
		session, release, sessionErr := bound.session(ctx)
		if sessionErr != nil {
			return nil, sessionErr
		}
		defer release()
		err = bound.db.WithContext(session.ctx).Raw(query).Scan(&records).Error
	} else {
		err = db.QueryRows(query, &records)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch example records for table %s: %v", table, err)
	}
	// If no records found, return empty slice
	if len(records) == 0 {
		return []map[string]interface{}{}, nil
	}

	return records, nil
}

// Helper function to get value from pointer or empty string
func getValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
