package dbmanager

import (
	"database/sql"
	"fmt"
	"log"
	"yanfachatbi/pkg/schemaobjects"
	"strings"
	"time"
)

// pgSystemSchemas lists PostgreSQL system schemas excluded from schema discovery.
const pgSystemSchemas = "('pg_catalog', 'information_schema', 'pg_toast')"

// pgQualifiedTableName qualifies a table name with its schema unless it lives
// in the default public schema (kept bare for backward compatibility).
func pgQualifiedTableName(schema, table string) string {
	return objectStorageName(schemaobjects.Ref{Schema: schema, Name: table, Kind: schemaobjects.Table})
}

// Quoted identifier parts are parsed without splitting embedded dots or quotes.
func pgSplitQualifiedTable(name string) (schema, table string) {
	schema, table, err := splitObjectName(name)
	if err != nil {
		return "", name
	}
	return schema, table
}
func pgQuoteQualifiedTable(name string) string {
	schema, table := pgSplitQualifiedTable(name)
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	if schema == "" {
		return quote(table)
	}
	return quote(schema) + "." + quote(table)
}

// Helper function to split SQL statements
func splitStatements(query string) []string {
	// Basic statement splitting - can be enhanced for more complex cases
	statements := strings.Split(query, ";")

	// Clean up statements
	var result []string
	for _, stmt := range statements {
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			result = append(result, stmt)
		}
	}
	return result
}

// Update the processRows function signature to return results and error
func processRows(rows *sql.Rows, startTime time.Time) ([]map[string]interface{}, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %v", err)
	}

	results := make([]map[string]interface{}, 0)
	values := make([]interface{}, len(columns))
	scanArgs := make([]interface{}, len(columns))

	for i := range values {
		scanArgs[i] = &values[i]
	}

	for rows.Next() {
		err := rows.Scan(scanArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %v", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			if val == nil {
				row[col] = nil
				continue
			}

			// Handle different types
			switch v := val.(type) {
			case []byte:
				row[col] = string(v)
			default:
				row[col] = v
			}
		}
		results = append(results, row)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	return results, nil
}

// Fix the extractTableName function to properly handle table names
func extractTableName(stmt string) string {
	// Preserve original case for the extraction
	originalStmt := strings.TrimSpace(stmt)
	upperStmt := strings.ToUpper(originalStmt)

	var tableName string

	// Handle different DROP TABLE variations
	if strings.HasPrefix(upperStmt, "DROP TABLE IF EXISTS") {
		tableName = strings.TrimSpace(originalStmt[len("DROP TABLE IF EXISTS"):])
	} else if strings.HasPrefix(upperStmt, "DROP TABLE") {
		tableName = strings.TrimSpace(originalStmt[len("DROP TABLE"):])
	} else {
		return ""
	}

	// Handle schema prefixes, quotes, and trailing characters
	tableName = strings.Split(tableName, " ")[0] // Get first word
	tableName = strings.Trim(tableName, "\"`;")  // Remove quotes and semicolons

	// Handle schema prefixes like "public."
	if strings.Contains(tableName, ".") {
		parts := strings.Split(tableName, ".")
		tableName = parts[len(parts)-1] // Get the last part after the dot
	}

	log.Printf("PostgresDriver -> extractTableName -> Extracted table name '%s' from statement: %s",
		tableName, originalStmt)

	return tableName
}
