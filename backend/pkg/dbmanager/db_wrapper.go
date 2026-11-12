package dbmanager

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/sourcegate"
)

// DBExecutor interface defines common database operations
type DBExecutor interface {
	Raw(sql string, values ...interface{}) error
	Exec(sql string, values ...interface{}) error
	Query(sql string, dest interface{}, values ...interface{}) error
	QueryRows(sql string, dest *[]map[string]interface{}, values ...interface{}) error
	Close() error
	GetDB() *sql.DB
	GetSchema(ctx context.Context) (*SchemaInfo, error)
	GetTableChecksum(ctx context.Context, table string) (string, error)
}

// sourceExecutor deliberately has no raw pool exit. Its context belongs to one
// admitted server operation; each terminal retains through rows and cleanup.
type sourceExecutor struct {
	ctx     context.Context
	manager *Manager
	config  ConnectionConfig
	db      *gorm.DB
}

func (s *sourceExecutor) schemaCacheKey() string {
	return connectionSchemaKey(s.config.ChatID, s.config)
}

func (s *sourceExecutor) session(ctx context.Context) (*postgresSession, func(), error) {
	if err := s.manager.requireSource(s.ctx, s.config); err != nil {
		return nil, nil, err
	}
	if err := s.manager.requireSource(ctx, s.config); err != nil {
		return nil, nil, err
	}
	chat, connection := s.config.sourceSnapshot()
	bound, release, err := s.manager.sourceAccess.AcquireSource(ctx, chat, connection)
	if err != nil {
		return nil, nil, err
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		release()
		return nil, nil, err
	}
	return &postgresSession{db: sqlDB, config: &s.config, check: func(checkCtx context.Context) error { return s.manager.requireSource(checkCtx, s.config) }, ctx: bound}, release, nil
}

func (s *sourceExecutor) GetDB() *sql.DB                    { return nil }
func (s *sourceExecutor) Close() error                      { return sourcegate.ErrIdentity }
func (s *sourceExecutor) Raw(string, ...interface{}) error  { return sourcegate.ErrIdentity }
func (s *sourceExecutor) Exec(string, ...interface{}) error { return sourcegate.ErrIdentity }
func (s *sourceExecutor) Query(query string, dest interface{}, args ...interface{}) error {
	if !constants.IsReadOnlyQuery(query, s.config.Type) {
		return sourcegate.ErrIdentity
	}
	session, release, err := s.session(s.ctx)
	if err != nil {
		return err
	}
	defer release()
	query, err = postgresOperationQuery(session.ctx, s.config, query)
	if err != nil {
		return err
	}
	return s.db.WithContext(session.ctx).Raw(query, args...).Scan(dest).Error
}
func (s *sourceExecutor) QueryRows(query string, dest *[]map[string]interface{}, args ...interface{}) error {
	return s.Query(query, dest, args...)
}
func (s *sourceExecutor) GetSchema(ctx context.Context) (*SchemaInfo, error) {
	return (&PostgresDriver{}).GetSchema(ctx, s, []string{"ALL"})
}
func (s *sourceExecutor) GetTableChecksum(ctx context.Context, table string) (string, error) {
	return (&PostgresDriver{}).GetTableChecksum(ctx, s, table)
}

// postgresSession stays private to the driver. The owner closes every returned
// rows before releasing the session's retained permit.
type postgresSession struct {
	db     *sql.DB
	ctx    context.Context
	check  func(context.Context) error
	config *ConnectionConfig
}

func openPostgresSession(ctx context.Context, executor DBExecutor) (*postgresSession, func(), error) {
	if bound, ok := executor.(*sourceExecutor); ok {
		return bound.session(ctx)
	}
	db := executor.GetDB()
	if db == nil {
		return nil, nil, fmt.Errorf("SQL connection unavailable")
	}
	return &postgresSession{db: db, ctx: ctx, check: func(ctx context.Context) error { return ctx.Err() }}, func() {}, nil
}

func (s *postgresSession) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if err := s.check(s.ctx); err != nil {
		return nil, err
	}
	if s.config != nil {
		var err error
		query, err = postgresOperationQuery(ctx, *s.config, query)
		if err != nil {
			return nil, err
		}
	}
	return s.db.QueryContext(ctx, query, args...)
}
func (s *postgresSession) PingContext(ctx context.Context) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	return s.db.PingContext(ctx)
}

func postgresOperationQuery(ctx context.Context, config ConnectionConfig, query string) (string, error) {
	chat := config.SourceChat
	if chat == nil || (!chat.ERPAssociated && chat.SourceID == "") {
		return query, nil
	}
	identity, err := sourcegate.ContextIdentity(ctx, chat.SourceID)
	if err != nil {
		return "", err
	}
	if identity.OwnerID != chat.UserID.Hex() || identity.ChatID != chat.ID.Hex() || identity.BindingRevision != chat.SourceBindingRevision {
		return "", sourcegate.ErrIdentity
	}
	for _, char := range identity.OperationID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && char != '-' {
			return "", sourcegate.ErrIdentity
		}
	}
	if len(identity.OperationID) != 36 {
		return "", sourcegate.ErrIdentity
	}
	return "/*yanfa_operation:" + identity.OperationID + "*/ " + query, nil
}

func postgresOperationName(ctx context.Context, config ConnectionConfig) (string, error) {
	if _, err := postgresOperationQuery(ctx, config, ""); err != nil {
		return "", err
	}
	identity, err := sourcegate.ContextIdentity(ctx, config.SourceChat.SourceID)
	if err != nil {
		return "", err
	}
	source, err := uuid.Parse(identity.SourceID)
	if err != nil {
		return "", sourcegate.ErrIdentity
	}
	operation, err := uuid.Parse(identity.OperationID)
	if err != nil {
		return "", sourcegate.ErrIdentity
	}
	instance := sha256.Sum256([]byte(config.ApplicationName))
	return "s1:" + base64.RawURLEncoding.EncodeToString(source[:]) + ":" + base64.RawURLEncoding.EncodeToString(instance[:9]) + ":" + base64.RawURLEncoding.EncodeToString(operation[:]), nil
}
func (s *postgresSession) scalar(ctx context.Context, query string, args ...interface{}) (string, error) {
	rows, err := s.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", sql.ErrNoRows
	}
	var value sql.NullString
	if err := rows.Scan(&value); err != nil {
		return "", err
	}
	return value.String, rows.Err()
}

// BaseWrapper provides common functionality for all DB wrappers
type BaseWrapper struct {
	db        *gorm.DB
	manager   *Manager
	chatID    string
	schemaKey string
}

func (w *BaseWrapper) schemaCacheKey() string { return w.schemaKey }

func (w *BaseWrapper) updateUsage() error {
	if err := w.manager.UpdateLastUsed(w.chatID); err != nil {
		log.Printf("Failed to update last used time: %v", err)
		return err
	}
	return nil
}

// PostgresWrapper implements DBExecutor for PostgreSQL
type PostgresWrapper struct {
	BaseWrapper
}

func NewPostgresWrapper(db *gorm.DB, manager *Manager, chatID string) *PostgresWrapper {
	return &PostgresWrapper{
		BaseWrapper: BaseWrapper{
			db:        db,
			manager:   manager,
			chatID:    chatID,
			schemaKey: manager.schemaCacheKey(chatID),
		},
	}
}

// GetDB returns the underlying *sql.DB
func (w *PostgresWrapper) GetDB() *sql.DB {
	sqlDB, err := w.db.DB()
	if err != nil {
		log.Printf("Failed to get SQL DB: %v", err)
		return nil
	}
	return sqlDB
}

// GetSchema fetches the current database schema
func (w *PostgresWrapper) GetSchema(ctx context.Context) (*SchemaInfo, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("PostgresWrapper -> GetSchema -> Context cancelled: %v", err)
		return nil, err
	}

	driver, exists := w.manager.drivers["postgresql"]
	if !exists {
		// Check if yugabytedb driver exists
		driver, exists = w.manager.drivers["yugabytedb"]
		if !exists {
			// Check if timescaledb driver exists
			driver, exists = w.manager.drivers["timescaledb"]
			if !exists {
				return nil, fmt.Errorf("driver not found")
			}
		}
	}

	if fetcher, ok := driver.(SchemaFetcher); ok {
		// Get selected collections from the chat service if available
		var selectedTables []string
		if w.manager.streamHandler != nil {
			// Try to get selected collections from the chat service
			selectedCollections, err := w.manager.streamHandler.GetSelectedCollections(w.chatID)
			if err == nil && selectedCollections != "ALL" && selectedCollections != "" {
				selectedTables = strings.Split(selectedCollections, ",")
				log.Printf("PostgresWrapper -> GetSchema -> Using selected collections for chat %s: %v", w.chatID, selectedTables)
			} else {
				// Default to ALL if there's an error or no specific collections
				selectedTables = []string{"ALL"}
				log.Printf("PostgresWrapper -> GetSchema -> Using ALL tables for chat %s", w.chatID)
			}
		} else {
			// Default to ALL if stream handler is not available
			selectedTables = []string{"ALL"}
		}

		// Pass the selected tables to get the schema
		schema, err := fetcher.GetSchema(ctx, w, selectedTables)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("Schema fetch cancelled by context")
				return nil, err
			}
			return nil, err
		}
		return schema, nil
	}
	return nil, fmt.Errorf("driver does not support schema fetching")
}

// GetTableChecksum calculates checksum for a single table
func (w *PostgresWrapper) GetTableChecksum(ctx context.Context, table string) (string, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("PostgresWrapper -> GetTableChecksum -> Context cancelled: %v", err)
		return "", err
	}

	if err := w.updateUsage(); err != nil {
		return "", fmt.Errorf("failed to update usage: %v", err)
	}

	driver, exists := w.manager.drivers["postgresql"]
	if !exists {
		// Check if yugabytedb driver exists
		driver, exists = w.manager.drivers["yugabytedb"]
		if !exists {
			// Check if timescaledb driver exists
			driver, exists = w.manager.drivers["timescaledb"]
			if !exists {
				return "", fmt.Errorf("driver not found")
			}
		}
	}

	if fetcher, ok := driver.(SchemaFetcher); ok {
		return fetcher.GetTableChecksum(ctx, w, table)
	}
	return "", fmt.Errorf("driver does not support checksum calculation")
}

// Raw executes a raw SQL query
func (w *PostgresWrapper) Raw(sql string, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Error
}

// Exec executes a SQL statement
func (w *PostgresWrapper) Exec(sql string, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Exec(sql, values...).Error
}

// Query executes a SQL query and scans the result into dest
func (w *PostgresWrapper) Query(sql string, dest interface{}, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Scan(dest).Error
}

// QueryRows executes a SQL query and scans the result into dest
func (w *PostgresWrapper) QueryRows(sql string, dest *[]map[string]interface{}, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Scan(dest).Error
}

// Close closes the database connection
func (w *PostgresWrapper) Close() error {
	sqlDB, err := w.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// MySQLWrapper implements DBExecutor for MySQL
type MySQLWrapper struct {
	BaseWrapper
}

func NewMySQLWrapper(db *gorm.DB, manager *Manager, chatID string) *MySQLWrapper {
	return &MySQLWrapper{
		BaseWrapper: BaseWrapper{
			db:        db,
			manager:   manager,
			chatID:    chatID,
			schemaKey: manager.schemaCacheKey(chatID),
		},
	}
}

// GetDB returns the underlying *sql.DB
func (w *MySQLWrapper) GetDB() *sql.DB {
	sqlDB, err := w.db.DB()
	if err != nil {
		log.Printf("Failed to get SQL DB: %v", err)
		return nil
	}
	return sqlDB
}

// GetSchema fetches the current database schema
func (w *MySQLWrapper) GetSchema(ctx context.Context) (*SchemaInfo, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("MySQLWrapper -> GetSchema -> Context cancelled: %v", err)
		return nil, err
	}

	// Check if MySQL driver exists
	_, exists := w.manager.drivers["mysql"]
	if !exists {
		return nil, fmt.Errorf("MySQL driver not found")
	}

	// Get the schema fetcher factory for MySQL
	fetcherFactory, exists := w.manager.fetchers["mysql"]
	if !exists {
		return nil, fmt.Errorf("MySQL schema fetcher not found")
	}

	// Create a schema fetcher for this connection
	fetcher := fetcherFactory(w)

	// Get selected collections from the chat service if available
	var selectedTables []string
	if w.manager.streamHandler != nil {
		// Try to get selected collections from the chat service
		selectedCollections, err := w.manager.streamHandler.GetSelectedCollections(w.chatID)
		if err == nil && selectedCollections != "ALL" && selectedCollections != "" {
			selectedTables = strings.Split(selectedCollections, ",")
			log.Printf("MySQLWrapper -> GetSchema -> Using selected collections for chat %s: %v", w.chatID, selectedTables)
		} else {
			// Default to ALL if there's an error or no specific collections
			selectedTables = []string{"ALL"}
			log.Printf("MySQLWrapper -> GetSchema -> Using ALL tables for chat %s", w.chatID)
		}
	} else {
		// Default to ALL if stream handler is not available
		selectedTables = []string{"ALL"}
	}

	// Pass the selected tables to get the schema
	schema, err := fetcher.GetSchema(ctx, w, selectedTables)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("Schema fetch cancelled by context")
			return nil, err
		}
		return nil, err
	}
	return schema, nil
}

// GetTableChecksum calculates checksum for a single table
func (w *MySQLWrapper) GetTableChecksum(ctx context.Context, table string) (string, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("MySQLWrapper -> GetTableChecksum -> Context cancelled: %v", err)
		return "", err
	}

	if err := w.updateUsage(); err != nil {
		return "", fmt.Errorf("failed to update usage: %v", err)
	}

	// Get the schema fetcher factory for MySQL
	fetcherFactory, exists := w.manager.fetchers["mysql"]
	if !exists {
		return "", fmt.Errorf("MySQL schema fetcher not found")
	}

	// Create a schema fetcher for this connection
	fetcher := fetcherFactory(w)

	return fetcher.GetTableChecksum(ctx, w, table)
}

// Raw executes a raw SQL query
func (w *MySQLWrapper) Raw(sql string, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Error
}

// Exec executes a SQL statement
func (w *MySQLWrapper) Exec(sql string, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Exec(sql, values...).Error
}

// Query executes a SQL query and scans the result into dest
func (w *MySQLWrapper) Query(sql string, dest interface{}, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	log.Printf("MySQLWrapper -> Query -> Executing: %s with values: %v", sql, values)
	result := w.db.Raw(sql, values...).Scan(dest)
	if result.Error != nil {
		log.Printf("MySQLWrapper -> Query -> Error: %v", result.Error)
	} else {
		log.Printf("MySQLWrapper -> Query -> Success: %d rows affected", result.RowsAffected)
	}
	return result.Error
}

// QueryRows executes a SQL query and scans the result into dest
func (w *MySQLWrapper) QueryRows(sql string, dest *[]map[string]interface{}, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Scan(dest).Error
}

// Close closes the database connection
func (w *MySQLWrapper) Close() error {
	sqlDB, err := w.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ClickHouseWrapper implements DBExecutor for ClickHouse
type ClickHouseWrapper struct {
	BaseWrapper
}

func NewClickHouseWrapper(db *gorm.DB, manager *Manager, chatID string) *ClickHouseWrapper {
	return &ClickHouseWrapper{
		BaseWrapper: BaseWrapper{
			db:        db,
			manager:   manager,
			chatID:    chatID,
			schemaKey: manager.schemaCacheKey(chatID),
		},
	}
}

// GetDB returns the underlying *sql.DB
func (w *ClickHouseWrapper) GetDB() *sql.DB {
	sqlDB, err := w.db.DB()
	if err != nil {
		log.Printf("Failed to get SQL DB: %v", err)
		return nil
	}
	return sqlDB
}

// GetSchema fetches the current database schema
func (w *ClickHouseWrapper) GetSchema(ctx context.Context) (*SchemaInfo, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("ClickHouseWrapper -> GetSchema -> Context cancelled: %v", err)
		return nil, err
	}

	// Check if ClickHouse driver exists
	_, exists := w.manager.drivers["clickhouse"]
	if !exists {
		return nil, fmt.Errorf("ClickHouse driver not found")
	}

	// Get the schema fetcher factory for ClickHouse
	fetcherFactory, exists := w.manager.fetchers["clickhouse"]
	if !exists {
		return nil, fmt.Errorf("ClickHouse schema fetcher not found")
	}

	// Create a schema fetcher for this connection
	fetcher := fetcherFactory(w)

	// Get selected collections from the chat service if available
	var selectedTables []string
	if w.manager.streamHandler != nil {
		// Try to get selected collections from the chat service
		selectedCollections, err := w.manager.streamHandler.GetSelectedCollections(w.chatID)
		if err == nil && selectedCollections != "ALL" && selectedCollections != "" {
			selectedTables = strings.Split(selectedCollections, ",")
			log.Printf("ClickHouseWrapper -> GetSchema -> Using selected collections for chat %s: %v", w.chatID, selectedTables)
		} else {
			// Default to ALL if there's an error or no specific collections
			selectedTables = []string{"ALL"}
			log.Printf("ClickHouseWrapper -> GetSchema -> Using ALL tables for chat %s", w.chatID)
		}
	} else {
		// Default to ALL if stream handler is not available
		selectedTables = []string{"ALL"}
	}

	// Pass the selected tables to get the schema
	schema, err := fetcher.GetSchema(ctx, w, selectedTables)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("Schema fetch cancelled by context")
			return nil, err
		}
		return nil, err
	}
	return schema, nil
}

// GetTableChecksum calculates checksum for a single table
func (w *ClickHouseWrapper) GetTableChecksum(ctx context.Context, table string) (string, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("ClickHouseWrapper -> GetTableChecksum -> Context cancelled: %v", err)
		return "", err
	}

	if err := w.updateUsage(); err != nil {
		return "", fmt.Errorf("failed to update usage: %v", err)
	}

	// Get the schema fetcher factory for ClickHouse
	fetcherFactory, exists := w.manager.fetchers["clickhouse"]
	if !exists {
		return "", fmt.Errorf("ClickHouse schema fetcher not found")
	}

	// Create a schema fetcher for this connection
	fetcher := fetcherFactory(w)

	return fetcher.GetTableChecksum(ctx, w, table)
}

// Raw executes a raw SQL query
func (w *ClickHouseWrapper) Raw(sql string, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Error
}

// Exec executes a SQL statement
func (w *ClickHouseWrapper) Exec(sql string, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Exec(sql, values...).Error
}

// Query executes a SQL query and scans the result into dest
func (w *ClickHouseWrapper) Query(sql string, dest interface{}, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Scan(dest).Error
}

// QueryRows executes a SQL query and scans the result into dest
func (w *ClickHouseWrapper) QueryRows(sql string, dest *[]map[string]interface{}, values ...interface{}) error {
	if err := w.updateUsage(); err != nil {
		return fmt.Errorf("failed to update usage: %v", err)
	}
	return w.db.Raw(sql, values...).Scan(dest).Error
}

// Close closes the database connection
func (w *ClickHouseWrapper) Close() error {
	sqlDB, err := w.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
