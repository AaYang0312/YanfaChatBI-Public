package dbmanager

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/redis"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"crypto/md5"
)

// Add these constants
const (
	schemaKeyPrefix = "schema:"
	schemaTTL       = 30 * 24 * time.Hour // Keep schemas for 30 days
)

// Update the interfaces
type SchemaFetcher interface {
	GetSchema(ctx context.Context, db DBExecutor, selectedTables []string) (*SchemaInfo, error)
	GetTableChecksum(ctx context.Context, db DBExecutor, table string) (string, error)
	FetchExampleRecords(ctx context.Context, db DBExecutor, table string, limit int) ([]map[string]interface{}, error)
}

// Update SchemaManager struct
type SchemaManager struct {
	mu             sync.RWMutex
	schemaCache    map[string]*SchemaInfo
	storageService *SchemaStorageService
	dbManager      *Manager
	fetcherMap     map[string]func(DBExecutor) SchemaFetcher
	simplifiers    map[string]SchemaSimplifier
}

func NewSchemaManager(redisRepo redis.IRedisRepositories, encryptionKey string, dbManager *Manager) (*SchemaManager, error) {
	storageService, err := NewSchemaStorageService(redisRepo, encryptionKey)
	if err != nil {
		return nil, err
	}

	manager := &SchemaManager{
		schemaCache:    make(map[string]*SchemaInfo),
		storageService: storageService,
		dbManager:      dbManager,
		fetcherMap:     make(map[string]func(DBExecutor) SchemaFetcher),
		simplifiers:    make(map[string]SchemaSimplifier),
	}

	// Register default fetchers
	manager.registerDefaultFetchers()

	// Register default simplifiers
	manager.registerDefaultSimplifiers()

	return manager, nil
}

// Cache-only public APIs need a current, identified connection. A missing
// connection after restart/disconnect cannot identify an old chat-only cache.
func (sm *SchemaManager) currentSchemaAccess(ctx context.Context, chatID string) (schemaCacheAccess, error) {
	if err := ctx.Err(); err != nil {
		return schemaCacheAccess{}, err
	}
	if sm.dbManager == nil {
		return schemaCacheAccess{}, fmt.Errorf("current schema connection unavailable")
	}
	sm.dbManager.mu.RLock()
	conn := sm.dbManager.connections[chatID]
	if conn == nil || conn.Status != StatusConnected || conn.ChatID != chatID || conn.UserID == "" {
		sm.dbManager.mu.RUnlock()
		return schemaCacheAccess{}, fmt.Errorf("current schema connection identity unavailable")
	}
	config, userID := conn.Config, conn.UserID
	sm.dbManager.mu.RUnlock()
	if config.ChatID != "" && config.ChatID != chatID {
		return schemaCacheAccess{}, fmt.Errorf("schema connection identity mismatch")
	}
	if chat := config.SourceChat; chat != nil && (chat.ID.Hex() != chatID || chat.UserID.Hex() != userID) {
		return schemaCacheAccess{}, fmt.Errorf("schema source identity mismatch")
	}
	access := newSchemaCacheAccess(connectionSchemaKey(chatID, config), config.Type, true)
	if !access.valid() {
		return schemaCacheAccess{}, fmt.Errorf("schema database type unavailable")
	}
	if err := sm.dbManager.requireSource(ctx, config); err != nil {
		return schemaCacheAccess{}, err
	}
	return access, nil
}

func (sm *SchemaManager) executorSchemaKey(chatID string, db DBExecutor) string {
	switch db.(type) {
	case *PostgresWrapper, *sourceExecutor:
		if origin, ok := db.(interface{ schemaCacheKey() string }); ok {
			return origin.schemaCacheKey() + ":metadata-v2"
		}
	}
	if origin, ok := db.(interface{ schemaCacheKey() string }); ok {
		return origin.schemaCacheKey()
	}
	if spreadsheet, ok := db.(*spreadsheetSchemaWrapper); ok {
		return connectionSchemaKey(chatID, spreadsheet.conn.Config)
	}
	return chatID
}

func (sm *SchemaManager) SetDBManager(dbManager *Manager) {
	sm.dbManager = dbManager
}

// RegisterFetcher registers a new schema fetcher for a database type
func (sm *SchemaManager) RegisterFetcher(dbType string, constructor func(DBExecutor) SchemaFetcher) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.fetcherMap[dbType] = constructor
}

// getFetcher returns appropriate schema fetcher for the database type
func (sm *SchemaManager) getFetcher(dbType string, db DBExecutor) (SchemaFetcher, error) {
	sm.mu.RLock()
	constructor, exists := sm.fetcherMap[dbType]
	sm.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("no schema fetcher registered for database type: %s", dbType)
	}

	return constructor(db), nil
}

// Update schema fetching methods to use appropriate fetcher
func (sm *SchemaManager) fetchSchema(ctx context.Context, db DBExecutor, dbType string, selectedTables []string) (*SchemaInfo, error) {
	// Check for context cancellation before starting
	if err := ctx.Err(); err != nil {
		log.Printf("fetchSchema -> context cancelled before starting: %v", err)
		return nil, err
	}

	// Get the appropriate fetcher for this database type
	fetcher, err := sm.getFetcher(dbType, db)
	if err != nil {
		return nil, err
	}
	schema, err := fetcher.GetSchema(ctx, db, selectedTables)
	if err == nil && schema != nil {
		schema.legacyDatabaseType = ""
		if isLegacySchemaType(dbType) {
			schema.legacyDatabaseType = dbType
		}
	}
	return schema, err
}

// Update GetSchema to use fetchSchema and getFetcher
func (sm *SchemaManager) GetSchema(ctx context.Context, chatID string, db DBExecutor, dbType string, selectedTables []string) (*SchemaInfo, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetSchema -> context cancelled at start: %v", err)
		return nil, err
	}

	// Create a new context with a longer timeout specifically for this operation
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// Always fetch fresh schema from database for schema checks
	schema, err := sm.fetchSchema(fetchCtx, db, dbType, selectedTables)
	if err != nil {
		if fetchCtx.Err() != nil {
			log.Printf("GetSchema -> context timeout during schema fetch: %v", fetchCtx.Err())
			return nil, fmt.Errorf("timeout while fetching schema: %v", fetchCtx.Err())
		}
		return nil, fmt.Errorf("failed to fetch schema: %w", err)
	}

	// Check for context cancellation again
	if err := ctx.Err(); err != nil {
		log.Printf("GetSchema -> context cancelled after schema fetch: %v", err)
		return nil, err
	}

	// Log all tables found in the database
	tableNames := make([]string, 0, len(schema.Tables))
	for tableName := range schema.Tables {
		// Check for context cancellation
		if err := ctx.Err(); err != nil {
			log.Printf("GetSchema -> context cancelled during table processing: %v", err)
			return nil, err
		}
		tableNames = append(tableNames, tableName)
	}
	log.Printf("SchemaManager -> GetSchema -> Fresh schema contains tables: %v", tableNames)

	return schema, nil
}

// Fix the CheckSchemaChanges function to properly call CompareSchemas
func (sm *SchemaManager) CheckSchemaChanges(ctx context.Context, chatID string, db DBExecutor, dbType string, selectedTables []string) (*SchemaDiff, bool, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("CheckSchemaChanges -> context cancelled: %v", err)
		return nil, false, err
	}

	_, exists := sm.dbManager.drivers[dbType]
	if !exists {
		return nil, false, fmt.Errorf("no driver found for type: %s", dbType)
	}

	log.Printf("SchemaManager -> CheckSchemaChanges -> Getting current schema for chatID: %s", chatID)
	// Get current schema using driver
	currentSchema, err := sm.GetSchema(ctx, chatID, db, dbType, selectedTables)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get current schema: %w", err)
	}

	// Log current tables for debugging
	currentTables := make([]string, 0, len(currentSchema.Tables))
	for tableName := range currentSchema.Tables {
		currentTables = append(currentTables, tableName)
	}
	log.Printf("SchemaManager -> CheckSchemaChanges -> Current schema has tables: %v", currentTables)

	// Try to get stored schema
	storedSchema, err := sm.getStoredSchema(ctx, chatID)
	if err != nil {
		// This is a first-time schema storage scenario or there was an error retrieving the schema
		if strings.Contains(err.Error(), "first-time schema storage") || strings.Contains(err.Error(), "key does not exist") {
			log.Printf("SchemaManager -> CheckSchemaChanges -> First-time schema storage for chatID: %s", chatID)
		} else {
			log.Printf("SchemaManager -> CheckSchemaChanges -> Error retrieving stored schema: %v", err)
		}

		// First time - store current schema without any comparison
		if err := sm.storeSchema(ctx, chatID, currentSchema, db, dbType); err != nil {
			return nil, false, fmt.Errorf("failed to store schema: %w", err)
		}

		// Return special diff for first time with full schema
		return &SchemaDiff{
			FullSchema:  currentSchema,
			UpdatedAt:   time.Now(),
			IsFirstTime: true,
		}, true, nil
	}

	// Log stored tables for debugging
	storedTables := make([]string, 0, len(storedSchema.FullSchema.Tables))
	for tableName := range storedSchema.FullSchema.Tables {
		storedTables = append(storedTables, tableName)
	}
	log.Printf("SchemaManager -> CheckSchemaChanges -> Stored schema has tables: %v", storedTables)

	// IMPORTANT: Use CompareSchemas (uppercase C) instead of compareSchemas (lowercase c)
	diff, hasChanges := sm.CompareSchemas(storedSchema.FullSchema, currentSchema)

	// Add detailed logging to see what's happening
	log.Printf("SchemaManager -> CheckSchemaChanges -> CompareSchemas returned hasChanges=%v, diff=%+v",
		hasChanges, diff)

	if hasChanges {
		log.Printf("SchemaManager -> CheckSchemaChanges -> Changes detected: added=%d, removed=%d, modified=%d",
			len(diff.AddedTables), len(diff.RemovedTables), len(diff.ModifiedTables))
	} else {
		log.Printf("SchemaManager -> CheckSchemaChanges -> No changes detected in comparison")
	}

	// Store the updated schema AFTER checking for changes
	if err := sm.storeSchema(ctx, chatID, currentSchema, db, dbType); err != nil {
		log.Printf("SchemaManager -> CheckSchemaChanges -> Error storing schema: %v", err)
		// Continue anyway, don't fail the whole operation
	}

	if !hasChanges {
		return nil, false, nil
	}

	return diff, true, nil
}

// Mark the old compareSchemas function as deprecated
// DEPRECATED: Use CompareSchemas instead
func (sm *SchemaManager) compareSchemas(old, new *SchemaInfo) *SchemaDiff {
	log.Printf("WARNING: Using deprecated compareSchemas function. Use CompareSchemas instead")
	diff, _ := sm.CompareSchemas(old, new)
	return diff
}

// Update compareTableSchemas to properly compare all table details
func compareTableSchemas(old, new TableSchema) TableDiff {
	diff := TableDiff{
		AddedColumns:    make([]string, 0),
		RemovedColumns:  make([]string, 0),
		ModifiedColumns: make([]string, 0),
		AddedIndexes:    make([]string, 0),
		RemovedIndexes:  make([]string, 0),
		AddedFKs:        make([]string, 0),
		RemovedFKs:      make([]string, 0),
	}

	// Compare columns
	for colName, newCol := range new.Columns {
		if oldCol, exists := old.Columns[colName]; !exists {
			log.Printf("compareTableSchemas -> Added column detected: %s", colName)
			diff.AddedColumns = append(diff.AddedColumns, colName)
		} else if !compareColumns(oldCol, newCol) {
			log.Printf("compareTableSchemas -> Modified column detected: %s (type: %s->%s, nullable: %v->%v, default: %s->%s)",
				colName, oldCol.Type, newCol.Type, oldCol.IsNullable, newCol.IsNullable,
				oldCol.DefaultValue, newCol.DefaultValue)
			diff.ModifiedColumns = append(diff.ModifiedColumns, colName)
		}
	}

	// Check for removed columns
	for colName := range old.Columns {
		if _, exists := new.Columns[colName]; !exists {
			log.Printf("compareTableSchemas -> Removed column detected: %s", colName)
			diff.RemovedColumns = append(diff.RemovedColumns, colName)
		}
	}

	// Compare indexes
	for idxName, newIdx := range new.Indexes {
		if oldIdx, exists := old.Indexes[idxName]; !exists {
			log.Printf("compareTableSchemas -> Added index detected: %s", idxName)
			diff.AddedIndexes = append(diff.AddedIndexes, idxName)
		} else if !reflect.DeepEqual(oldIdx, newIdx) {
			log.Printf("compareTableSchemas -> Modified index detected: %s", idxName)
			diff.RemovedIndexes = append(diff.RemovedIndexes, idxName)
			diff.AddedIndexes = append(diff.AddedIndexes, idxName)
		}
	}

	for idxName := range old.Indexes {
		if _, exists := new.Indexes[idxName]; !exists {
			log.Printf("compareTableSchemas -> Removed index detected: %s", idxName)
			diff.RemovedIndexes = append(diff.RemovedIndexes, idxName)
		}
	}

	// Compare foreign keys
	for fkName, newFK := range new.ForeignKeys {
		if oldFK, exists := old.ForeignKeys[fkName]; !exists {
			log.Printf("compareTableSchemas -> Added foreign key detected: %s", fkName)
			diff.AddedFKs = append(diff.AddedFKs, fkName)
		} else if !reflect.DeepEqual(oldFK, newFK) {
			log.Printf("compareTableSchemas -> Modified foreign key detected: %s", fkName)
			diff.RemovedFKs = append(diff.RemovedFKs, fkName)
			diff.AddedFKs = append(diff.AddedFKs, fkName)
		}
	}

	for fkName := range old.ForeignKeys {
		if _, exists := new.ForeignKeys[fkName]; !exists {
			log.Printf("compareTableSchemas -> Removed foreign key detected: %s", fkName)
			diff.RemovedFKs = append(diff.RemovedFKs, fkName)
		}
	}

	return diff
}

// Fix the compareColumns function to be more thorough
func compareColumns(oldCol, newCol ColumnInfo) bool {
	// Log detailed comparison
	log.Printf("compareColumns -> Comparing column: name=%s", oldCol.Name)
	log.Printf("compareColumns -> Old column: type=%s, nullable=%v, default=%s, comment=%s",
		oldCol.Type, oldCol.IsNullable, oldCol.DefaultValue, oldCol.Comment)
	log.Printf("compareColumns -> New column: type=%s, nullable=%v, default=%s, comment=%s",
		newCol.Type, newCol.IsNullable, newCol.DefaultValue, newCol.Comment)

	// Compare all relevant column properties
	typeMatch := oldCol.Type == newCol.Type
	nullableMatch := oldCol.IsNullable == newCol.IsNullable
	defaultMatch := oldCol.DefaultValue == newCol.DefaultValue
	commentMatch := oldCol.Comment == newCol.Comment

	log.Printf("compareColumns -> Comparison results: typeMatch=%v, nullableMatch=%v, defaultMatch=%v, commentMatch=%v",
		typeMatch, nullableMatch, defaultMatch, commentMatch)

	return typeMatch && nullableMatch && defaultMatch && commentMatch
}

// Add the missing isEmpty method for TableDiff
func (td TableDiff) isEmpty() bool {
	return !td.MetadataChanged && len(td.AddedConstraints) == 0 && len(td.RemovedConstraints) == 0 && len(td.AddedColumns) == 0 &&
		len(td.RemovedColumns) == 0 &&
		len(td.ModifiedColumns) == 0 &&
		len(td.AddedIndexes) == 0 &&
		len(td.RemovedIndexes) == 0 &&
		len(td.AddedFKs) == 0 &&
		len(td.RemovedFKs) == 0
}

// Update storeSchema to properly set checksums
func (sm *SchemaManager) storeSchema(ctx context.Context, chatID string, schema *SchemaInfo, db DBExecutor, dbType string) error {
	cacheKey := sm.executorSchemaKey(chatID, db)
	schema.legacyDatabaseType = ""
	if isLegacySchemaType(dbType) {
		schema.legacyDatabaseType = dbType
	}
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("storeSchema -> context cancelled at start: %v", err)
		return err
	}

	// Get table checksums first
	var checksums map[string]string
	var err error
	if schema.MetadataVersion != 0 {
		checksums = schema.objectChecksums()
	} else {
		checksums, err = sm.getTableChecksums(ctx, db, dbType)
	}
	if err != nil {
		return fmt.Errorf("failed to get table checksums: %w", err)
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("storeSchema -> context cancelled after getting checksums: %v", err)
		return err
	}

	// Create LLM-friendly schema with example records
	llmSchema, err := sm.createLLMSchemaWithExamples(ctx, schema, dbType, db)
	if err != nil {
		return err
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("storeSchema -> context cancelled after creating LLM schema: %v", err)
		return err
	}

	// Create storage object
	storage := &SchemaStorage{
		FullSchema:     schema,
		ObjectCacheKey: schema.ObjectCacheKey(cacheKey),
		LLMSchema:      llmSchema,
		TableChecksums: checksums,
		UpdatedAt:      time.Now(),
	}

	// Store in cache
	sm.mu.Lock()
	sm.schemaCache[cacheKey] = schema
	sm.mu.Unlock()

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("storeSchema -> context cancelled before storing in Redis: %v", err)
		return err
	}

	// Store in Redis
	if err := sm.storageService.Store(ctx, cacheKey, storage); err != nil {
		return fmt.Errorf("failed to store schema in Redis: %w", err)
	}

	return nil
}

// Get current table checksums without fetching full schema
func (sm *SchemaManager) getTableChecksums(ctx context.Context, db DBExecutor, dbType string) (map[string]string, error) {
	if isPostgresMetadataType(dbType) {
		objects, err := (&PostgresDriver{}).GetQueryObjects(ctx, db)
		if err != nil {
			return nil, err
		}
		return schemaFromQueryObjects(objects).objectChecksums(), nil
	}
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("getTableChecksums -> context cancelled: %v", err)
		return nil, err
	}

	switch dbType {
	case constants.DatabaseTypePostgreSQL, constants.DatabaseTypeYugabyteDB, constants.DatabaseTypeTimescaleDB:
		// Check for context cancellation
		if err := ctx.Err(); err != nil {
			log.Printf("getTableChecksums -> context cancelled: %v", err)
			return nil, err
		}

		checksums := make(map[string]string)

		// Get schema directly from the database
		schema, err := db.GetSchema(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get schema: %w", err)
		}

		// Calculate checksums for each table
		for tableName, table := range schema.Tables {
			// Check for context cancellation
			if err := ctx.Err(); err != nil {
				log.Printf("getTableChecksums -> context cancelled: %v", err)
				return nil, err
			}

			// Convert table definition to string for checksum
			tableStr := fmt.Sprintf("%s:%v:%v:%v:%v",
				tableName,
				table.Columns,
				table.Indexes,
				table.ForeignKeys,
				table.Constraints,
			)

			// Calculate checksum using crypto/md5
			hasher := md5.New()
			hasher.Write([]byte(tableStr))
			checksum := hex.EncodeToString(hasher.Sum(nil))
			checksums[tableName] = checksum
		}
		return checksums, nil
	case constants.DatabaseTypeMySQL, constants.DatabaseTypeStarRocks:
		// Implement MySQL / StarRocks checksum calculation
		checksums := make(map[string]string)

		// Get schema directly from the database
		schema, err := db.GetSchema(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get schema: %w", err)
		}

		// Calculate checksums for each table
		for tableName, table := range schema.Tables {
			// Check for context cancellation
			if err := ctx.Err(); err != nil {
				log.Printf("getTableChecksums -> context cancelled: %v", err)
				return nil, err
			}

			// Convert table definition to string for checksum
			tableStr := fmt.Sprintf("%s:%v:%v:%v:%v",
				tableName,
				table.Columns,
				table.Indexes,
				table.ForeignKeys,
				table.Constraints,
			)

			// Calculate checksum using crypto/md5
			hasher := md5.New()
			hasher.Write([]byte(tableStr))
			checksum := hex.EncodeToString(hasher.Sum(nil))
			checksums[tableName] = checksum
		}
		return checksums, nil
	case constants.DatabaseTypeClickhouse:
		// Implement ClickHouse checksum calculation
		checksums := make(map[string]string)

		// Get schema directly from the database
		schema, err := db.GetSchema(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get schema: %w", err)
		}

		// Calculate checksums for each table
		for tableName, table := range schema.Tables {
			// Check for context cancellation
			if err := ctx.Err(); err != nil {
				log.Printf("getTableChecksums -> context cancelled: %v", err)
				return nil, err
			}

			// Convert table definition to string for checksum
			tableStr := fmt.Sprintf("%s:%v:%v:%v:%v",
				tableName,
				table.Columns,
				table.Indexes,
				table.ForeignKeys,
				table.Constraints,
			)

			// Calculate checksum using crypto/md5
			hasher := md5.New()
			hasher.Write([]byte(tableStr))
			checksum := hex.EncodeToString(hasher.Sum(nil))
			checksums[tableName] = checksum
		}
		return checksums, nil
	case constants.DatabaseTypeMongoDB:
		// Implement MongoDB checksum calculation
		checksums := make(map[string]string)

		// Get schema directly from the database
		schema, err := db.GetSchema(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get schema: %w", err)
		}

		// Calculate checksums for each collection (table)
		for collectionName, collection := range schema.Tables {
			// Check for context cancellation
			if err := ctx.Err(); err != nil {
				log.Printf("getTableChecksums -> context cancelled: %v", err)
				return nil, err
			}

			// Convert collection definition to string for checksum
			// MongoDB may not have foreign keys or constraints like SQL databases,
			// but we include them in the checksum calculation for consistency
			collectionStr := fmt.Sprintf("%s:%v:%v:%v:%v",
				collectionName,
				collection.Columns,
				collection.Indexes,
				collection.ForeignKeys,
				collection.Constraints,
			)

			// Calculate checksum using crypto/md5
			hasher := md5.New()
			hasher.Write([]byte(collectionStr))
			checksum := hex.EncodeToString(hasher.Sum(nil))
			checksums[collectionName] = checksum
		}
		return checksums, nil
	case "spreadsheet", constants.DatabaseTypeGoogleSheets:
		// Spreadsheet and Google Sheets need special handling to get schema with the fetcher
		checksums := make(map[string]string)

		// Get the spreadsheet fetcher
		fetcher, err := sm.getFetcher("spreadsheet", db)
		if err != nil {
			return nil, fmt.Errorf("failed to get spreadsheet fetcher: %v", err)
		}

		// Get schema using the fetcher
		schema, err := fetcher.GetSchema(ctx, db, []string{"ALL"})
		if err != nil {
			return nil, fmt.Errorf("failed to get schema: %w", err)
		}

		// Calculate checksums for each table
		for tableName, table := range schema.Tables {
			// Check for context cancellation
			if err := ctx.Err(); err != nil {
				log.Printf("getTableChecksums -> context cancelled: %v", err)
				return nil, err
			}

			// Convert table definition to string for checksum
			tableStr := fmt.Sprintf("%s:%v:%v:%v:%v",
				tableName,
				table.Columns,
				table.Indexes,
				table.ForeignKeys,
				table.Constraints,
			)

			// Calculate checksum using crypto/md5
			hasher := md5.New()
			hasher.Write([]byte(tableStr))
			checksum := hex.EncodeToString(hasher.Sum(nil))
			checksums[tableName] = checksum
		}
		return checksums, nil
	}

	return nil, fmt.Errorf("unsupported data source type: %s", dbType)
}

// Update fetchTableList to use driver directly
func (sm *SchemaManager) fetchTableList(ctx context.Context, db DBExecutor) ([]string, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("fetchTableList -> context cancelled: %v", err)
		return nil, err
	}

	schema, err := db.GetSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema: %w", err)
	}

	tables := make([]string, 0, len(schema.Tables))
	for tableName := range schema.Tables {
		tables = append(tables, tableName)
	}
	sort.Strings(tables) // Ensure consistent order
	return tables, nil
}

// Update schema retrieval methods
func (sm *SchemaManager) getStoredSchema(ctx context.Context, chatID string) (*SchemaStorage, error) {
	access, err := sm.currentSchemaAccess(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return sm.getStoredSchemaAt(ctx, access.Key, access.DatabaseType)
}
func (sm *SchemaManager) getStoredSchemaAt(ctx context.Context, key, dbType string) (*SchemaStorage, error) {
	if sm.storageService == nil {
		return nil, fmt.Errorf("schema storage unavailable")
	}
	return loadSchemaCache(ctx, schemaCacheAccess{Key: key, DatabaseType: dbType, IdentityKnown: true}, sm.storageService.Retrieve)
}

// GetStoredSchemaInfo retrieves the full SchemaInfo from storage for a chat.
// This is used by the vectorization service to build embedding chunks.
func (sm *SchemaManager) GetStoredSchemaInfo(ctx context.Context, chatID string) (*SchemaInfo, error) {
	storage, err := sm.getStoredSchema(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if storage == nil || storage.FullSchema == nil {
		return nil, fmt.Errorf("no schema found for chat %s", chatID)
	}
	return storage.FullSchema, nil
}

// GetStoredSchemaStorage retrieves the full SchemaStorage (including LLMSchema with example records).
// Used by vectorization to enrich schema chunks with example records.
func (sm *SchemaManager) GetStoredSchemaStorage(ctx context.Context, chatID string) (*SchemaStorage, error) {
	return sm.getStoredSchema(ctx, chatID)
}

// Add type-specific schema simplification
type SchemaSimplifier interface {
	SimplifyDataType(dbType string) string
	GetColumnConstraints(col ColumnInfo, table TableSchema) []string
}

// Add database-specific simplifiers
type PostgresSimplifier struct{}

func (s *PostgresSimplifier) SimplifyDataType(dbType string) string {
	switch strings.ToLower(dbType) {
	case "integer", "bigint", "smallint":
		return "number"
	case "character varying", "text", "char", "varchar":
		return "text"
	case "boolean":
		return "boolean"
	case "timestamp without time zone", "timestamp with time zone":
		return "timestamp"
	case "date":
		return "date"
	case "numeric", "decimal", "real", "double precision":
		return "decimal"
	case "jsonb", "json":
		return "json"
	default:
		return dbType
	}
}

func (s *PostgresSimplifier) GetColumnConstraints(col ColumnInfo, table TableSchema) []string {
	constraints := make([]string, 0)

	if !col.IsNullable {
		constraints = append(constraints, "NOT NULL")
	}

	if col.DefaultValue != "" {
		constraints = append(constraints, fmt.Sprintf("DEFAULT %s", col.DefaultValue))
	}

	// Check if column is part of any unique index
	for _, idx := range table.Indexes {
		if idx.IsUnique && len(idx.Columns) == 1 && idx.Columns[0] == col.Name {
			constraints = append(constraints, "UNIQUE")
			break
		}
	}

	// Check if column is a foreign key
	for _, fk := range table.ForeignKeys {
		if fk.ColumnName == col.Name {
			constraints = append(constraints, fmt.Sprintf("REFERENCES %s(%s)", fk.RefTable, fk.RefColumn))
			break
		}
	}

	return constraints
}

func (sm *SchemaManager) isColumnIndexed(colName string, indexes map[string]IndexInfo) bool {
	for _, idx := range indexes {
		for _, col := range idx.Columns {
			if col == colName {
				return true
			}
		}
	}
	return false
}

func (sm *SchemaManager) isColumnUnique(tableName, colName string, schema *SchemaInfo) bool {
	table, exists := schema.Tables[tableName]
	if !exists {
		return false
	}

	for _, idx := range table.Indexes {
		if idx.IsUnique && len(idx.Columns) == 1 && idx.Columns[0] == colName {
			return true
		}
	}
	return false
}

// Ensure both simplifiers implement the interface
var (
	_ SchemaSimplifier = (*PostgresSimplifier)(nil)
)

// FormatSchemaForLLM formats the schema into a LLM-friendly string
func (m *SchemaManager) FormatSchemaForLLM(schema *SchemaInfo) string {
	return formatSchemaForLLM(schema)
}
func (m *SchemaManager) FormatSchemaForLLMWithExamples(storage *SchemaStorage) string {
	return formatSchemaForLLMWithExamples(storage)
}

// HasSchemaChanged to support context cancellation
func (sm *SchemaManager) HasSchemaChanged(ctx context.Context, chatID string, db DBExecutor) (bool, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("HasSchemaChanged -> context cancelled: %v", err)
		return false, err
	}

	log.Printf("HasSchemaChanged -> chatID: %s", chatID)

	conn, exists := sm.dbManager.connections[chatID]
	if !exists {
		return true, fmt.Errorf("connection not found")
	}

	if isPostgresMetadataType(conn.Config.Type) {
		return sm.postgresSchemaChanged(ctx, chatID, db)
	}
	// Get current checksums first
	currentChecksums, err := sm.getTableChecksums(ctx, db, conn.Config.Type)
	if err != nil {
		log.Printf("HasSchemaChanged -> error getting current checksums: %v", err)
		return true, nil
	}

	// Check for context cancellation after expensive operation
	if err := ctx.Err(); err != nil {
		log.Printf("HasSchemaChanged -> context cancelled: %v", err)
		return false, err
	}

	log.Printf("HasSchemaChanged -> currentChecksums: %+v", currentChecksums)

	// Check in-memory cache
	sm.mu.RLock()
	cachedSchema, exists := sm.schemaCache[sm.executorSchemaKey(chatID, db)]
	sm.mu.RUnlock()

	if exists && cachedSchema != nil {
		log.Printf("HasSchemaChanged -> using cached schema")

		// Compare table counts
		if len(cachedSchema.Tables) != len(currentChecksums) {
			log.Printf("HasSchemaChanged -> table count mismatch: cached=%d, current=%d",
				len(cachedSchema.Tables), len(currentChecksums))
			return true, nil
		}

		// Compare each table's checksum
		for tableName, currentChecksum := range currentChecksums {
			// Check for context cancellation during iteration
			if err := ctx.Err(); err != nil {
				log.Printf("HasSchemaChanged -> context cancelled: %v", err)
				return false, err
			}

			table, ok := cachedSchema.Tables[tableName]
			if !ok {
				log.Printf("HasSchemaChanged -> table %s not found in cache", tableName)
				return true, nil
			}

			log.Printf("HasSchemaChanged -> comparing table %s: cached=%s, current=%s",
				tableName, table.Checksum, currentChecksum)

			if table.Checksum != currentChecksum {
				log.Printf("HasSchemaChanged -> checksum mismatch for table %s", tableName)
				return true, nil
			}
		}
		return false, nil
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("HasSchemaChanged -> context cancelled: %v", err)
		return false, err
	}

	// Check Redis if not in memory
	storage, err := sm.storageService.Retrieve(ctx, sm.executorSchemaKey(chatID, db))
	if err != nil || storage == nil {
		log.Printf("HasSchemaChanged -> no valid storage found: %v", err)
		return true, nil
	}

	return !reflect.DeepEqual(storage.TableChecksums, currentChecksums), nil
}

// Add TriggerType enum
type TriggerType string

const (
	TriggerTypeManual TriggerType = "manual" // For DDL operations
	TriggerTypeAuto   TriggerType = "auto"   // For interval checks
)

// Helper function to get the latest schema
func (sm *SchemaManager) GetLatestSchema(ctx context.Context, chatID string) (*SchemaInfo, error) {
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetLatestSchema -> context cancelled: %v", err)
		return nil, err
	}

	// Get current connection
	db, err := sm.dbManager.GetConnectionContext(ctx, chatID)
	if err != nil {
		log.Printf("GetLatestSchema -> error getting connection: %v", err)
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}

	// Get connection config
	sm.dbManager.mu.RLock()
	conn, exists := sm.dbManager.connections[chatID]
	sm.dbManager.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("connection not found for chatID: %s", chatID)
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetLatestSchema -> context cancelled: %v", err)
		return nil, err
	}

	// For manual triggers (DDL), directly fetch and store new schema
	log.Printf("SchemaManager -> RefreshSchema -> Manual trigger, fetching new schema")
	schema, err := db.GetSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current schema: %w", err)
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetLatestSchema -> context cancelled: %v", err)
		return nil, err
	}

	log.Printf("SchemaManager -> RefreshSchema -> Storing fresh schema")
	// Store the fresh schema immediately
	if err := sm.storeSchema(ctx, chatID, schema, db, conn.Config.Type); err != nil {
		return nil, fmt.Errorf("failed to store schema: %w", err)
	}

	return schema, nil
}

// Create LLM-friendly schema
func (sm *SchemaManager) createLLMSchema(schema *SchemaInfo, dbType string) *LLMSchemaInfo {
	if schema != nil && schema.MetadataVersion != 0 {
		return createObjectLLMSchema(schema)
	}
	llmSchema := &LLMSchemaInfo{
		Tables:        make(map[string]LLMTableInfo),
		Relationships: make([]SchemaRelationship, 0),
	}

	// Get the appropriate simplifier for this database type
	simplifier := sm.getSimplifier(dbType)

	// Process tables
	for tableName, table := range schema.Tables {
		llmTable := LLMTableInfo{
			Name:           tableName,
			Description:    table.Comment,
			Columns:        make([]LLMColumnInfo, 0, len(table.Columns)),
			RowCount:       table.RowCount,
			ExampleRecords: nil,
		}

		// Process columns using the appropriate simplifier
		for _, col := range table.Columns {
			simplifiedType := simplifier.SimplifyDataType(col.Type)

			llmCol := LLMColumnInfo{
				Name:        col.Name,
				Type:        simplifiedType,
				Description: col.Comment,
				IsNullable:  col.IsNullable,
				IsIndexed:   sm.isColumnIndexed(col.Name, table.Indexes),
			}
			llmTable.Columns = append(llmTable.Columns, llmCol)
		}

		// Find primary key
		for _, constraint := range table.Constraints {
			if constraint.Type == "PRIMARY KEY" && len(constraint.Columns) > 0 {
				llmTable.PrimaryKey = strings.Join(constraint.Columns, ", ")
				break
			}
		}

		// Add ClickHouse-specific information if applicable
		if dbType == constants.DatabaseTypeClickhouse && table.Comment != "" {
			// Parse the comment for ClickHouse-specific information
			comment := table.Comment

			// Extract engine information
			if strings.Contains(strings.ToLower(comment), "engine=") {
				engineStart := strings.Index(strings.ToLower(comment), "engine=") + 7
				engineEnd := len(comment)
				if spaceIdx := strings.Index(comment[engineStart:], " "); spaceIdx != -1 {
					engineEnd = engineStart + spaceIdx
				}
				engineInfo := comment[engineStart:engineEnd]
				if engineInfo != "" {
					llmTable.Description += fmt.Sprintf(" [Engine: %s]", engineInfo)
				}
			}

			// Extract partition key information
			if strings.Contains(strings.ToLower(comment), "partition by") {
				partitionStart := strings.Index(strings.ToLower(comment), "partition by") + 12
				partitionEnd := len(comment)
				for _, keyword := range []string{"order by", "primary key", "sample by", "settings"} {
					if keywordIdx := strings.Index(strings.ToLower(comment[partitionStart:]), keyword); keywordIdx != -1 {
						if partitionStart+keywordIdx < partitionEnd {
							partitionEnd = partitionStart + keywordIdx
						}
					}
				}
				partitionKey := strings.TrimSpace(comment[partitionStart:partitionEnd])
				if partitionKey != "" {
					llmTable.Description += fmt.Sprintf(" [Partition Key: %s]", partitionKey)
				}
			}

			// Extract order by key information
			if strings.Contains(strings.ToLower(comment), "order by") {
				orderStart := strings.Index(strings.ToLower(comment), "order by") + 8
				orderEnd := len(comment)
				for _, keyword := range []string{"partition by", "primary key", "sample by", "settings"} {
					if keywordIdx := strings.Index(strings.ToLower(comment[orderStart:]), keyword); keywordIdx != -1 {
						if orderStart+keywordIdx < orderEnd {
							orderEnd = orderStart + keywordIdx
						}
					}
				}
				orderByKey := strings.TrimSpace(comment[orderStart:orderEnd])
				if orderByKey != "" {
					llmTable.Description += fmt.Sprintf(" [Order By: %s]", orderByKey)
				}
			}
		}

		llmSchema.Tables[tableName] = llmTable
	}

	// Extract relationships
	llmSchema.Relationships = sm.extractRelationships(schema)

	return llmSchema
}

func (sm *SchemaManager) createLLMSchemaWithExamples(ctx context.Context, schema *SchemaInfo, dbType string, db DBExecutor) (*LLMSchemaInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if schema != nil && schema.MetadataVersion != 0 {
		return createObjectLLMSchema(schema), nil
	}
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("createLLMSchemaWithExamples -> context cancelled at start: %v", err)
		return nil, err
	}

	log.Printf("createLLMSchemaWithExamples -> Starting for dbType: %s with %d tables", dbType, len(schema.Tables))

	llmSchema := &LLMSchemaInfo{
		Tables:        make(map[string]LLMTableInfo),
		Relationships: make([]SchemaRelationship, 0),
	}

	// Get the appropriate simplifier for this database type
	simplifier := sm.getSimplifier(dbType)

	// Get fetcher for the database type
	fetcher, err := sm.getFetcher(dbType, db)
	if err != nil {
		return nil, err
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("createLLMSchemaWithExamples -> context cancelled after getting fetcher: %v", err)
		return nil, err
	}

	// Process tables
	for tableName, table := range schema.Tables {
		// Check for context cancellation periodically
		if err := ctx.Err(); err != nil {
			log.Printf("createLLMSchemaWithExamples -> context cancelled during table processing: %v", err)
			return nil, err
		}

		log.Printf("createLLMSchemaWithExamples -> Processing table: %s with %d columns", tableName, len(table.Columns))

		llmTable := LLMTableInfo{
			Name:           tableName,
			Description:    table.Comment,
			Columns:        make([]LLMColumnInfo, 0, len(table.Columns)),
			RowCount:       table.RowCount,
			ExampleRecords: nil,
		}

		// Process columns using the appropriate simplifier
		for colName, col := range table.Columns {
			log.Printf("createLLMSchemaWithExamples -> Processing column: %s of type %s", colName, col.Type)
			simplifiedType := simplifier.SimplifyDataType(col.Type)

			llmCol := LLMColumnInfo{
				Name:        col.Name,
				Type:        simplifiedType,
				Description: col.Comment,
				IsNullable:  col.IsNullable,
				IsIndexed:   sm.isColumnIndexed(col.Name, table.Indexes),
			}
			llmTable.Columns = append(llmTable.Columns, llmCol)
			log.Printf("createLLMSchemaWithExamples -> Added column: %s of simplified type %s", col.Name, simplifiedType)
		}

		// Find primary key
		for constraintName, constraint := range table.Constraints {
			if constraint.Type == "PRIMARY KEY" && len(constraint.Columns) > 0 {
				llmTable.PrimaryKey = strings.Join(constraint.Columns, ", ")
				log.Printf("createLLMSchemaWithExamples -> Found primary key constraint %s with columns: %s",
					constraintName, llmTable.PrimaryKey)
				break
			}
		}

		// Fetch example records if fetcher is available
		if fetcher != nil {
			log.Printf("createLLMSchemaWithExamples -> Fetching example records for table: %s", tableName)
			examples, err := fetcher.FetchExampleRecords(ctx, db, tableName, 3) // Fetch up to 3 example records
			if err != nil {
				log.Printf("createLLMSchemaWithExamples -> Failed to fetch example records for table %s: %v", tableName, err)
				return nil, err
			} else {
				log.Printf("createLLMSchemaWithExamples -> Successfully fetched %d example records for table %s", len(examples), tableName)
				llmTable.ExampleRecords = examples

				// Debug the example records
				for i, record := range examples {
					log.Printf("createLLMSchemaWithExamples -> Example record %d for table %s: %+v", i, tableName, record)
				}
			}
		} else {
			log.Printf("createLLMSchemaWithExamples -> No fetcher available, skipping example records for table: %s", tableName)
		}

		llmSchema.Tables[tableName] = llmTable
		log.Printf("createLLMSchemaWithExamples -> Added table %s to LLM schema with %d columns and %d example records",
			tableName, len(llmTable.Columns), len(llmTable.ExampleRecords))
	}

	// Extract relationships
	llmSchema.Relationships = sm.extractRelationships(schema)
	log.Printf("createLLMSchemaWithExamples -> Extracted %d relationships", len(llmSchema.Relationships))

	return llmSchema, nil
}

// Extract relationships from foreign keys
func (sm *SchemaManager) extractRelationships(schema *SchemaInfo) []SchemaRelationship {
	relationships := make([]SchemaRelationship, 0)
	processedPairs := make(map[string]bool)

	for tableName, table := range schema.Tables {
		for _, fk := range table.ForeignKeys {
			// Create unique pair key to avoid duplicates
			pairKey := fmt.Sprintf("%s:%s", tableName, fk.RefTable)
			if processedPairs[pairKey] {
				continue
			}

			rel := SchemaRelationship{
				FromTable: tableName,
				ToTable:   fk.RefTable,
				Type:      sm.determineRelationType(schema, tableName, fk),
			}
			relationships = append(relationships, rel)
			processedPairs[pairKey] = true
		}
	}

	return relationships
}

// Determine relationship type (one-to-one, one-to-many, etc.)
func (sm *SchemaManager) determineRelationType(schema *SchemaInfo, fromTable string, fk ForeignKey) string {
	// Check if the foreign key column is unique
	if sm.isColumnUnique(fromTable, fk.ColumnName, schema) {
		return "one_to_one"
	}
	return "one_to_many"
}

// Add method to register default fetchers
func (sm *SchemaManager) registerDefaultFetchers() {
	// Register PostgreSQL schema fetcher
	sm.RegisterFetcher("postgresql", func(db DBExecutor) SchemaFetcher {
		return &PostgresDriver{}
	})

	// Register YugabyteDB schema fetcher (uses PostgreSQL fetcher)
	sm.RegisterFetcher("yugabytedb", func(db DBExecutor) SchemaFetcher {
		return &PostgresDriver{}
	})

	// Register TimescaleDB schema fetcher (TimescaleDB is a PostgreSQL extension)
	sm.RegisterFetcher("timescaledb", func(db DBExecutor) SchemaFetcher {
		return &PostgresDriver{}
	})

	// Register MySQL schema fetcher
	sm.RegisterFetcher("mysql", func(db DBExecutor) SchemaFetcher {
		return NewMySQLSchemaFetcher(db)
	})

	// Register StarRocks schema fetcher (StarRocks uses MySQL wire protocol)
	sm.RegisterFetcher("starrocks", func(db DBExecutor) SchemaFetcher {
		return NewMySQLSchemaFetcher(db)
	})

	// Register ClickHouse schema fetcher
	sm.RegisterFetcher("clickhouse", func(db DBExecutor) SchemaFetcher {
		return NewClickHouseSchemaFetcher(db)
	})

	// Register MongoDB schema fetcher
	sm.RegisterFetcher("mongodb", func(db DBExecutor) SchemaFetcher {
		return NewMongoDBSchemaFetcher(db)
	})

	// Register Spreadsheet schema fetcher (uses custom SpreadsheetDriver fetcher)
	sm.RegisterFetcher("spreadsheet", func(db DBExecutor) SchemaFetcher {
		return &SpreadsheetDriver{
			postgresDriver: NewPostgresDriver(),
		}
	})

	// Register Google Sheets schema fetcher (uses PostgreSQL driver)
	sm.RegisterFetcher(constants.DatabaseTypeGoogleSheets, func(db DBExecutor) SchemaFetcher {
		return &SpreadsheetDriver{
			postgresDriver: NewPostgresDriver(),
		}
	})
}

// Update the CompareSchemasDetailed function to be more precise
func (sm *SchemaManager) CompareSchemasDetailed(oldSchema, newSchema *SchemaInfo) (*SchemaDiff, bool) {
	if (oldSchema != nil && oldSchema.MetadataVersion != 0) || (newSchema != nil && newSchema.MetadataVersion != 0) {
		return compareObjectSchemas(oldSchema, newSchema)
	}
	diff := &SchemaDiff{
		AddedTables:    make([]string, 0),
		RemovedTables:  make([]string, 0),
		ModifiedTables: make(map[string]TableDiff),
		UpdatedAt:      time.Now(),
	}

	hasChanges := false

	// Check for added/removed tables
	for tableName := range newSchema.Tables {
		if _, exists := oldSchema.Tables[tableName]; !exists {
			diff.AddedTables = append(diff.AddedTables, tableName)
			hasChanges = true
		}
	}

	for tableName := range oldSchema.Tables {
		if _, exists := newSchema.Tables[tableName]; !exists {
			diff.RemovedTables = append(diff.RemovedTables, tableName)
			hasChanges = true
		}
	}

	// Check for modified tables
	for tableName, newTable := range newSchema.Tables {
		oldTable, exists := oldSchema.Tables[tableName]
		if !exists {
			continue // Already handled as added table
		}

		// Skip if table checksums match exactly
		if oldTable.Checksum == newTable.Checksum {
			continue
		}

		tableDiff := TableDiff{
			AddedColumns:    make([]string, 0),
			RemovedColumns:  make([]string, 0),
			ModifiedColumns: make([]string, 0),
			AddedIndexes:    make([]string, 0),
			RemovedIndexes:  make([]string, 0),
			AddedFKs:        make([]string, 0),
			RemovedFKs:      make([]string, 0),
		}

		// Check columns
		for colName := range newTable.Columns {
			if _, exists := oldTable.Columns[colName]; !exists {
				tableDiff.AddedColumns = append(tableDiff.AddedColumns, colName)
				hasChanges = true
			}
		}

		for colName, oldCol := range oldTable.Columns {
			newCol, exists := newTable.Columns[colName]
			if !exists {
				tableDiff.RemovedColumns = append(tableDiff.RemovedColumns, colName)
				hasChanges = true
				continue
			}

			// Check if column definition changed
			if !reflect.DeepEqual(oldCol, newCol) {
				tableDiff.ModifiedColumns = append(tableDiff.ModifiedColumns, colName)
				hasChanges = true
			}
		}

		// Check indexes
		for idxName := range newTable.Indexes {
			if _, exists := oldTable.Indexes[idxName]; !exists {
				tableDiff.AddedIndexes = append(tableDiff.AddedIndexes, idxName)
				hasChanges = true
			}
		}

		for idxName, oldIdx := range oldTable.Indexes {
			newIdx, exists := newTable.Indexes[idxName]
			if !exists {
				tableDiff.RemovedIndexes = append(tableDiff.RemovedIndexes, idxName)
				hasChanges = true
				continue
			}

			// Check if index definition changed
			if !reflect.DeepEqual(oldIdx, newIdx) {
				// Consider this a removed and added index
				tableDiff.RemovedIndexes = append(tableDiff.RemovedIndexes, idxName)
				tableDiff.AddedIndexes = append(tableDiff.AddedIndexes, idxName)
				hasChanges = true
			}
		}

		// Check foreign keys
		for fkName := range newTable.ForeignKeys {
			if _, exists := oldTable.ForeignKeys[fkName]; !exists {
				tableDiff.AddedFKs = append(tableDiff.AddedFKs, fkName)
				hasChanges = true
			}
		}

		for fkName, oldFK := range oldTable.ForeignKeys {
			newFK, exists := newTable.ForeignKeys[fkName]
			if !exists {
				tableDiff.RemovedFKs = append(tableDiff.RemovedFKs, fkName)
				hasChanges = true
				continue
			}

			// Check if FK definition changed
			if !reflect.DeepEqual(oldFK, newFK) {
				// Consider this a removed and added FK
				tableDiff.RemovedFKs = append(tableDiff.RemovedFKs, fkName)
				tableDiff.AddedFKs = append(tableDiff.AddedFKs, fkName)
				hasChanges = true
			}
		}

		// Only add table diff if there are actual changes
		if len(tableDiff.AddedColumns) > 0 || len(tableDiff.RemovedColumns) > 0 ||
			len(tableDiff.ModifiedColumns) > 0 || len(tableDiff.AddedIndexes) > 0 ||
			len(tableDiff.RemovedIndexes) > 0 || len(tableDiff.AddedFKs) > 0 ||
			len(tableDiff.RemovedFKs) > 0 {
			diff.ModifiedTables[tableName] = tableDiff
		}
	}

	// If no changes were detected, return nil diff
	if !hasChanges {
		return nil, false
	}

	return diff, true
}

// Fix the CompareSchemas function to always do detailed comparison
func (sm *SchemaManager) CompareSchemas(oldSchema, newSchema *SchemaInfo) (*SchemaDiff, bool) {
	if (oldSchema != nil && oldSchema.MetadataVersion != 0) || (newSchema != nil && newSchema.MetadataVersion != 0) {
		return compareObjectSchemas(oldSchema, newSchema)
	}
	// Skip the checksum comparison and always do detailed comparison
	log.Printf("SchemaManager -> CompareSchemas -> Performing detailed comparison regardless of checksums")

	diff := &SchemaDiff{
		AddedTables:    make([]string, 0),
		RemovedTables:  make([]string, 0),
		ModifiedTables: make(map[string]TableDiff),
		UpdatedAt:      time.Now(),
	}

	hasChanges := false

	// Log table counts for debugging
	log.Printf("SchemaManager -> CompareSchemas -> Old schema has %d tables, new schema has %d tables",
		len(oldSchema.Tables), len(newSchema.Tables))

	// Log table names for better debugging
	oldTableNames := make([]string, 0, len(oldSchema.Tables))
	for tableName := range oldSchema.Tables {
		oldTableNames = append(oldTableNames, tableName)
	}

	newTableNames := make([]string, 0, len(newSchema.Tables))
	for tableName := range newSchema.Tables {
		newTableNames = append(newTableNames, tableName)
	}

	log.Printf("SchemaManager -> CompareSchemas -> Old tables: %v", oldTableNames)
	log.Printf("SchemaManager -> CompareSchemas -> New tables: %v", newTableNames)

	// Check for added tables
	for tableName := range newSchema.Tables {
		_, exists := oldSchema.Tables[tableName]
		log.Printf("SchemaManager -> CompareSchemas -> Checking if table %s exists in old schema: %v", tableName, exists)

		if !exists {
			log.Printf("SchemaManager -> CompareSchemas -> Added table detected: %s", tableName)
			diff.AddedTables = append(diff.AddedTables, tableName)
			hasChanges = true
		}
	}

	// Check for removed tables
	for tableName := range oldSchema.Tables {
		_, exists := newSchema.Tables[tableName]
		log.Printf("SchemaManager -> CompareSchemas -> Checking if table %s exists in new schema: %v", tableName, exists)

		if !exists {
			log.Printf("SchemaManager -> CompareSchemas -> Removed table detected: %s", tableName)
			diff.RemovedTables = append(diff.RemovedTables, tableName)
			hasChanges = true
		}
	}

	// Check for modified tables - CRITICAL FIX
	for tableName, newTable := range newSchema.Tables {
		oldTable, exists := oldSchema.Tables[tableName]
		if !exists {
			continue // Already handled as added table
		}

		// Always perform detailed comparison using compareTableSchemas
		tableDiff := compareTableSchemas(oldTable, newTable)

		// Log the detailed comparison results
		log.Printf("SchemaManager -> CompareSchemas -> Table %s detailed comparison: added cols=%d, removed cols=%d, modified cols=%d, added indexes=%d, removed indexes=%d",
			tableName, len(tableDiff.AddedColumns), len(tableDiff.RemovedColumns),
			len(tableDiff.ModifiedColumns), len(tableDiff.AddedIndexes), len(tableDiff.RemovedIndexes))

		// Only add table diff if there are actual changes
		if !tableDiff.isEmpty() {
			diff.ModifiedTables[tableName] = tableDiff
			hasChanges = true
			log.Printf("SchemaManager -> CompareSchemas -> Table %s has changes", tableName)
		} else {
			log.Printf("SchemaManager -> CompareSchemas -> Table %s has no changes", tableName)
		}
	}

	// If no changes were detected, return false
	if !hasChanges {
		log.Printf("SchemaManager -> CompareSchemas -> No actual changes detected")
		return nil, false
	}

	log.Printf("SchemaManager -> CompareSchemas -> Changes detected: added tables=%d, removed tables=%d, modified tables=%d",
		len(diff.AddedTables), len(diff.RemovedTables), len(diff.ModifiedTables))
	return diff, true
}

// Improve the compareTableDetails method to detect all changes
func (sm *SchemaManager) compareTableDetails(oldTable, newTable *TableSchema) TableDiff {
	tableDiff := TableDiff{
		AddedColumns:    make([]string, 0),
		RemovedColumns:  make([]string, 0),
		ModifiedColumns: make([]string, 0),
		AddedIndexes:    make([]string, 0),
		RemovedIndexes:  make([]string, 0),
		AddedFKs:        make([]string, 0),
		RemovedFKs:      make([]string, 0),
	}

	// Check columns
	for colName, newCol := range newTable.Columns {
		if oldCol, exists := oldTable.Columns[colName]; !exists {
			log.Printf("SchemaManager -> compareTableDetails -> Added column detected: %s", colName)
			tableDiff.AddedColumns = append(tableDiff.AddedColumns, colName)
		} else {
			// Compare column details
			if newCol.Type != oldCol.Type ||
				newCol.IsNullable != oldCol.IsNullable ||
				newCol.DefaultValue != oldCol.DefaultValue {
				log.Printf("SchemaManager -> compareTableDetails -> Modified column detected: %s (type: %s->%s, nullable: %v->%v, default: %s->%s)",
					colName, oldCol.Type, newCol.Type, oldCol.IsNullable, newCol.IsNullable,
					oldCol.DefaultValue, newCol.DefaultValue)
				tableDiff.ModifiedColumns = append(tableDiff.ModifiedColumns, colName)
			}
		}
	}

	for colName := range oldTable.Columns {
		if _, exists := newTable.Columns[colName]; !exists {
			log.Printf("SchemaManager -> compareTableDetails -> Removed column detected: %s", colName)
			tableDiff.RemovedColumns = append(tableDiff.RemovedColumns, colName)
		}
	}

	// Check indexes
	for idxName, newIdx := range newTable.Indexes {
		if oldIdx, exists := oldTable.Indexes[idxName]; !exists {
			log.Printf("SchemaManager -> compareTableDetails -> Added index detected: %s", idxName)
			tableDiff.AddedIndexes = append(tableDiff.AddedIndexes, idxName)
		} else {
			// Compare index details
			if !reflect.DeepEqual(oldIdx, newIdx) {
				log.Printf("SchemaManager -> compareTableDetails -> Modified index detected: %s", idxName)
				tableDiff.RemovedIndexes = append(tableDiff.RemovedIndexes, idxName)
				tableDiff.AddedIndexes = append(tableDiff.AddedIndexes, idxName)
			}
		}
	}

	for idxName := range oldTable.Indexes {
		if _, exists := newTable.Indexes[idxName]; !exists {
			log.Printf("SchemaManager -> compareTableDetails -> Removed index detected: %s", idxName)
			tableDiff.RemovedIndexes = append(tableDiff.RemovedIndexes, idxName)
		}
	}

	// Check foreign keys
	for fkName, newFK := range newTable.ForeignKeys {
		if oldFK, exists := oldTable.ForeignKeys[fkName]; !exists {
			log.Printf("SchemaManager -> compareTableDetails -> Added foreign key detected: %s", fkName)
			tableDiff.AddedFKs = append(tableDiff.AddedFKs, fkName)
		} else {
			// Compare FK details
			if !reflect.DeepEqual(oldFK, newFK) {
				log.Printf("SchemaManager -> compareTableDetails -> Modified foreign key detected: %s", fkName)
				tableDiff.RemovedFKs = append(tableDiff.RemovedFKs, fkName)
				tableDiff.AddedFKs = append(tableDiff.AddedFKs, fkName)
			}
		}
	}

	for fkName := range oldTable.ForeignKeys {
		if _, exists := newTable.ForeignKeys[fkName]; !exists {
			log.Printf("SchemaManager -> compareTableDetails -> Removed foreign key detected: %s", fkName)
			tableDiff.RemovedFKs = append(tableDiff.RemovedFKs, fkName)
		}
	}

	return tableDiff
}

// Add a method to clear schema cache
func (sm *SchemaManager) ClearSchemaCache(chatID string) {
	sm.mu.Lock()
	for key := range sm.schemaCache {
		if key == chatID || strings.HasPrefix(key, chatID+":") {
			delete(sm.schemaCache, key)
		}
	}
	sm.mu.Unlock()
	log.Printf("SchemaManager -> ClearSchemaCache -> Cleared schema cache for chatID: %s", chatID)
}

// InvalidateSchema preserves the chat-only API for callers with an identified
// in-memory connection. Status is irrelevant to deletion, but identity and source
// authorization are still required. Rebinding after restart uses the explicit
// previous-config API below instead of guessing from persisted cache contents.
func (sm *SchemaManager) InvalidateSchema(ctx context.Context, chatID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sm.dbManager == nil || chatID == "" {
		return fmt.Errorf("schema connection identity unavailable")
	}
	sm.dbManager.mu.RLock()
	conn := sm.dbManager.connections[chatID]
	if conn == nil || conn.ChatID != chatID || conn.UserID == "" {
		sm.dbManager.mu.RUnlock()
		return fmt.Errorf("schema connection identity unavailable")
	}
	config, userID := conn.Config, conn.UserID
	sm.dbManager.mu.RUnlock()
	if config.ChatID != "" && config.ChatID != chatID {
		return fmt.Errorf("schema connection identity mismatch")
	}
	if chat := config.SourceChat; chat != nil && (chat.ID.Hex() != chatID || chat.UserID.Hex() != userID) {
		return fmt.Errorf("schema source identity mismatch")
	}
	if err := sm.dbManager.requireSource(ctx, config); err != nil {
		return err
	}
	return sm.invalidateSchemaIdentity(ctx, connectionSchemaKey(chatID, config))
}

// InvalidateSchemaForConnection uses the binding operation's previous server-owned
// snapshot and works even when no connection remains after process restart.
func (sm *SchemaManager) InvalidateSchemaForConnection(ctx context.Context, chatID string, previous ConnectionConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	chat := previous.SourceChat
	if chat == nil || chat.ID.IsZero() || chat.UserID.IsZero() || chat.ID.Hex() != chatID || (previous.ChatID != "" && previous.ChatID != chatID) {
		return fmt.Errorf("previous schema source identity unavailable or mismatched")
	}
	if sm.dbManager == nil {
		return fmt.Errorf("schema source authorization unavailable")
	}
	if err := sm.dbManager.requireSource(ctx, previous); err != nil {
		return err
	}
	return sm.invalidateSchemaIdentity(ctx, connectionSchemaKey(chatID, previous))
}

// Called only after the public entrypoints have identified and authorized a
// server-owned connection snapshot. Never treat this base as a chat prefix.
func (sm *SchemaManager) invalidateSchemaIdentity(ctx context.Context, base string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sm.storageService == nil || sm.storageService.redisRepo == nil {
		return fmt.Errorf("schema storage unavailable")
	}
	// These are the two persisted formats: legacy and query-object metadata v2.
	// Delete exact keys only: other source/binding/connection revisions survive.
	keys := []string{base, base + ":metadata-v2"}
	sm.mu.Lock()
	for _, key := range keys {
		delete(sm.schemaCache, key)
	}
	sm.mu.Unlock()
	var failures []error
	for _, key := range keys {
		if err := sm.storageService.redisRepo.Del(schemaKeyPrefix+key, ctx); err != nil {
			failures = append(failures, fmt.Errorf("invalidate schema %s: %w", key, err))
		}
	}
	// Attempt both versions and preserve failures so the binding operation can
	// retry idempotently; an unavailable Redis must never look like success.
	return errors.Join(failures...)
}

// Readiness is scoped by the current connection before touching memory/Redis.
// It is never inferred from an old LLM-only representation.
func (sm *SchemaManager) IsSchemaReady(ctx context.Context, chatID string) bool {
	access, err := sm.currentSchemaAccess(ctx, chatID)
	if err != nil {
		return false
	}
	sm.mu.RLock()
	cached := sm.schemaCache[access.Key]
	sm.mu.RUnlock()
	if cached != nil {
		if _, err := validateSchemaCache(&SchemaStorage{FullSchema: cached, ObjectCacheKey: cached.ObjectCacheKey(access.Key)}, access); err == nil {
			return true
		}
	}
	stored, err := sm.getStoredSchemaAt(ctx, access.Key, access.DatabaseType)
	if err != nil {
		return false
	}
	sm.mu.Lock()
	sm.schemaCache[access.Key] = stored.FullSchema
	sm.mu.Unlock()
	return true
}

// GetSchemaWithExamples gets the schema with example records
func (sm *SchemaManager) GetSchemaWithExamples(ctx context.Context, chatID string, db DBExecutor, dbType string, selectedTables []string) (*SchemaStorage, error) {
	if isPostgresMetadataType(dbType) {
		schema, err := sm.fetchSchema(ctx, db, dbType, selectedTables)
		if err != nil {
			return nil, err
		}
		if err = sm.storeSchema(ctx, chatID, schema, db, dbType); err != nil {
			return nil, err
		}
		return &SchemaStorage{FullSchema: schema, LLMSchema: createObjectLLMSchema(schema), TableChecksums: schema.objectChecksums(), ObjectCacheKey: schema.ObjectCacheKey(sm.executorSchemaKey(chatID, db)), UpdatedAt: schema.UpdatedAt}, nil
	}
	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetSchemaWithExamples -> context cancelled: %v", err)
		return nil, err
	}
	if bound, ok := db.(*sourceExecutor); ok {
		if err := bound.manager.requireSource(ctx, bound.config); err != nil {
			return nil, err
		}
	}

	// Try to get from storage first
	storage, err := sm.getStoredSchemaAt(ctx, sm.executorSchemaKey(chatID, db), dbType)
	if err != nil {
		// If this is a first-time schema storage scenario, we'll continue to fetch the schema
		if strings.Contains(err.Error(), "first-time schema storage") {
			log.Printf("GetSchemaWithExamples -> First-time schema storage for chatID: %s, will fetch schema", chatID)
		} else {
			log.Printf("GetSchemaWithExamples -> Error retrieving stored schema: %v, will fetch schema", err)
		}
		// Continue to fetch the schema
	} else if storage != nil && storage.LLMSchema != nil {
		// Check for context cancellation
		if err := ctx.Err(); err != nil {
			log.Printf("GetSchemaWithExamples -> context cancelled after getting stored schema: %v", err)
			return nil, err
		}

		// Check if we have example records
		hasExamples := false
		for _, table := range storage.LLMSchema.Tables {
			if len(table.ExampleRecords) > 0 {
				hasExamples = true
				break
			}
		}

		if hasExamples {
			return storage, nil
		}
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetSchemaWithExamples -> context cancelled before fetching schema: %v", err)
		return nil, err
	}

	// If not found or no examples, fetch fresh schema and store with examples
	schema, err := sm.fetchSchema(ctx, db, dbType, selectedTables)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schema: %w", err)
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetSchemaWithExamples -> context cancelled after fetching schema: %v", err)
		return nil, err
	}

	// Store schema with examples
	if err := sm.storeSchema(ctx, chatID, schema, db, dbType); err != nil {
		return nil, fmt.Errorf("failed to store schema: %w", err)
	}

	// Check for context cancellation
	if err := ctx.Err(); err != nil {
		log.Printf("GetSchemaWithExamples -> context cancelled after storing schema: %v", err)
		return nil, err
	}

	// Get the stored schema with examples
	storage, err = sm.getStoredSchemaAt(ctx, sm.executorSchemaKey(chatID, db), dbType)
	if err != nil {
		// Check if this is a first-time schema storage scenario
		if strings.Contains(err.Error(), "first-time schema storage") {
			log.Printf("GetSchemaWithExamples -> First-time schema storage for chatID: %s", chatID)
			return nil, fmt.Errorf("first-time schema storage, please try again: %v", err)
		}
		return nil, fmt.Errorf("failed to get stored schema: %w", err)
	}

	return storage, nil
}

// FormatSchemaWithExamplesAndCollections formats the schema with example records for LLM with selected collections
func (sm *SchemaManager) FormatSchemaWithExamplesAndCollections(ctx context.Context, chatID string, db DBExecutor, dbType string, selectedCollections []string) (string, error) {
	// Get schema with examples
	storage, err := sm.GetSchemaWithExamples(ctx, chatID, db, dbType, selectedCollections)
	if err != nil {
		return "", fmt.Errorf("failed to get schema with examples: %w", err)
	}

	// Format the schema for LLM
	return sm.FormatSchemaForLLMWithExamples(storage), nil
}

// Add a method to register simplifiers
func (sm *SchemaManager) RegisterSimplifier(dbType string, simplifier SchemaSimplifier) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.simplifiers[dbType] = simplifier
}

// Add a method to get the appropriate simplifier
func (sm *SchemaManager) getSimplifier(dbType string) SchemaSimplifier {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	simplifier, exists := sm.simplifiers[dbType]
	if !exists {
		// Default to PostgreSQL simplifier if no specific one is found
		return &PostgresSimplifier{}
	}
	return simplifier
}

// Add a method to register default simplifiers
func (sm *SchemaManager) registerDefaultSimplifiers() {
	// Register PostgreSQL simplifier
	sm.RegisterSimplifier("postgresql", &PostgresSimplifier{})

	// Register YugabyteDB simplifier (uses PostgreSQL simplifier)
	sm.RegisterSimplifier("yugabytedb", &PostgresSimplifier{})

	// Register MySQL simplifier
	sm.RegisterSimplifier("mysql", &MySQLSimplifier{})

	// Register ClickHouse simplifier
	sm.RegisterSimplifier("clickhouse", &ClickHouseSimplifier{})

	// Register MongoDB simplifier
	sm.RegisterSimplifier("mongodb", &MongoDBSimplifier{})
}
