package dbmanager

import (
	"fmt"
	"log"
	"sort"
	"strings"
)

func formatSchemaForLLM(schema *SchemaInfo) string {
	if schema == nil {
		return ""
	}
	if schema.MetadataVersion != 0 || !isLegacySchemaType(schema.legacyDatabaseType) {
		return formatQueryObjectSchema(schema)
	}
	log.Printf("FormatSchemaForLLM -> Starting with %d tables", len(schema.Tables))

	var result strings.Builder
	result.WriteString("Current Database Schema:\n\n")

	// Sort tables for consistent output
	tableNames := make([]string, 0, len(schema.Tables))
	for tableName := range schema.Tables {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)

	// Format schema for LLM for tables, columns, indexes, foreign keys, constraints, etc.
	for _, tableName := range tableNames {
		table := schema.Tables[tableName]
		log.Printf("FormatSchemaForLLM -> Formatting table: %s with %d columns",
			tableName, len(table.Columns))

		result.WriteString(fmt.Sprintf("Table: %s\n", tableName))
		if table.Comment != "" {
			result.WriteString(fmt.Sprintf("Description: %s\n", table.Comment))
		}

		// Sort columns for consistent output
		columnNames := make([]string, 0, len(table.Columns))
		for columnName := range table.Columns {
			columnNames = append(columnNames, columnName)
		}
		sort.Strings(columnNames)

		for _, columnName := range columnNames {
			column := table.Columns[columnName]

			nullable := "NOT NULL"
			if column.IsNullable {
				nullable = "NULL"
			}
			result.WriteString(fmt.Sprintf("  - %s (%s) %s",
				columnName,
				column.Type,
				nullable,
			))

			// Check if column is primary key by looking at indexes
			isPrimaryKey := false
			isUnique := false
			for _, idx := range table.Indexes {
				if len(idx.Columns) == 1 && idx.Columns[0] == columnName {
					if strings.Contains(strings.ToLower(idx.Name), "pkey") {
						isPrimaryKey = true
					} else if idx.IsUnique {
						isUnique = true
					}
				}
			}

			if isPrimaryKey {
				result.WriteString(" PRIMARY KEY")
			} else if isUnique {
				result.WriteString(" UNIQUE")
			}

			if column.DefaultValue != "" {
				result.WriteString(fmt.Sprintf(" DEFAULT %s", column.DefaultValue))
			}

			if column.Comment != "" {
				result.WriteString(fmt.Sprintf(" -- %s", column.Comment))
			}

			result.WriteString("\n")
		}

		// Add row count information
		result.WriteString(fmt.Sprintf("Row Count: %d\n", table.RowCount))

		result.WriteString("\n")
	}

	log.Printf("FormatSchemaForLLM -> Completed formatting schema with %d tables", len(tableNames))
	return result.String()
}

func formatSchemaForLLMWithExamples(storage *SchemaStorage) string {
	if storage == nil || storage.FullSchema == nil {
		return ""
	}
	if storage.FullSchema.MetadataVersion != 0 || !isLegacySchemaType(storage.FullSchema.legacyDatabaseType) {
		return formatQueryObjectSchema(storage.FullSchema)
	}
	if storage.LLMSchema == nil {
		return formatSchemaForLLM(storage.FullSchema)
	}
	log.Printf("FormatSchemaForLLMWithExamples -> Starting with %d tables", len(storage.LLMSchema.Tables))

	var result strings.Builder
	result.WriteString("Current Database Schema:\n\n")

	// Sort tables for consistent output
	tableNames := make([]string, 0, len(storage.LLMSchema.Tables))
	for tableName := range storage.LLMSchema.Tables {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)
	log.Printf("FormatSchemaForLLMWithExamples -> Sorted %d table names", len(tableNames))

	// Format schema for LLM for tables, columns, indexes, foreign keys, constraints, etc.
	for _, tableName := range tableNames {
		table := storage.LLMSchema.Tables[tableName]
		log.Printf("FormatSchemaForLLMWithExamples -> Formatting table: %s with %d columns and %d example records",
			tableName, len(table.Columns), len(table.ExampleRecords))

		result.WriteString(fmt.Sprintf("Table: %s\n", tableName))
		if table.Description != "" {
			result.WriteString(fmt.Sprintf("Description: %s\n", table.Description))
		}

		// Sort columns for consistent output
		sort.Slice(table.Columns, func(i, j int) bool {
			return table.Columns[i].Name < table.Columns[j].Name
		})

		// Check if we have any columns
		if len(table.Columns) == 0 {
			log.Printf("FormatSchemaForLLMWithExamples -> Warning: No columns found for table %s", tableName)
		}

		for _, column := range table.Columns {
			nullable := "NOT NULL"
			if column.IsNullable {
				nullable = "NULL"
			}
			result.WriteString(fmt.Sprintf("  - %s (%s) %s",
				column.Name,
				column.Type,
				nullable,
			))

			if column.Name == table.PrimaryKey {
				result.WriteString(" PRIMARY KEY")
			} else if column.IsIndexed {
				result.WriteString(" INDEXED")
			}

			if column.Description != "" {
				result.WriteString(fmt.Sprintf(" -- %s", column.Description))
			}

			result.WriteString("\n")
		}

		// Add index information
		if fullTable, ok := storage.FullSchema.Tables[tableName]; ok && len(fullTable.Indexes) > 0 {
			result.WriteString("\nIndexes:\n")

			// Sort indexes for consistent output
			indexNames := make([]string, 0, len(fullTable.Indexes))
			for indexName := range fullTable.Indexes {
				indexNames = append(indexNames, indexName)
			}
			sort.Strings(indexNames)
			log.Printf("FormatSchemaForLLMWithExamples -> Formatting %d indexes for table %s", len(indexNames), tableName)

			for _, indexName := range indexNames {
				index := fullTable.Indexes[indexName]
				uniqueStr := ""
				if index.IsUnique {
					uniqueStr = "UNIQUE "
				}
				result.WriteString(fmt.Sprintf("  - %s: %sINDEX on (%s)\n",
					indexName,
					uniqueStr,
					strings.Join(index.Columns, ", ")))
			}
		}

		// Add constraint information
		if fullTable, ok := storage.FullSchema.Tables[tableName]; ok && len(fullTable.Constraints) > 0 {
			result.WriteString("\nConstraints:\n")

			// Sort constraints for consistent output
			constraintNames := make([]string, 0, len(fullTable.Constraints))
			for constraintName := range fullTable.Constraints {
				constraintNames = append(constraintNames, constraintName)
			}
			sort.Strings(constraintNames)
			log.Printf("FormatSchemaForLLMWithExamples -> Formatting %d constraints for table %s", len(constraintNames), tableName)

			for _, constraintName := range constraintNames {
				constraint := fullTable.Constraints[constraintName]
				result.WriteString(fmt.Sprintf("  - %s (%s): %s\n",
					constraintName,
					constraint.Type,
					constraint.Definition))
			}
		}

		// Add foreign key information
		if fullTable, ok := storage.FullSchema.Tables[tableName]; ok && len(fullTable.ForeignKeys) > 0 {
			result.WriteString("\nForeign Keys:\n")

			// Sort foreign keys for consistent output
			fkNames := make([]string, 0, len(fullTable.ForeignKeys))
			for fkName := range fullTable.ForeignKeys {
				fkNames = append(fkNames, fkName)
			}
			sort.Strings(fkNames)
			log.Printf("FormatSchemaForLLMWithExamples -> Formatting %d foreign keys for table %s", len(fkNames), tableName)

			for _, fkName := range fkNames {
				fk := fullTable.ForeignKeys[fkName]
				result.WriteString(fmt.Sprintf("  - %s: %s references %s(%s)",
					fkName,
					fk.ColumnName,
					fk.RefTable,
					fk.RefColumn))

				if fk.OnDelete != "NO ACTION" {
					result.WriteString(fmt.Sprintf(" ON DELETE %s", fk.OnDelete))
				}

				if fk.OnUpdate != "NO ACTION" {
					result.WriteString(fmt.Sprintf(" ON UPDATE %s", fk.OnUpdate))
				}

				result.WriteString("\n")
			}
		}

		// Add row count information
		result.WriteString(fmt.Sprintf("\nRow Count: %d\n", table.RowCount))

		// Add example records if available
		if len(table.ExampleRecords) > 0 {
			log.Printf("FormatSchemaForLLMWithExamples -> Formatting %d example records for table %s",
				len(table.ExampleRecords), tableName)

			result.WriteString("\nExample Records:\n")

			// Get column names for header
			columnNames := make([]string, len(table.Columns))
			for i, col := range table.Columns {
				columnNames[i] = col.Name
			}

			// Debug column names
			log.Printf("FormatSchemaForLLMWithExamples -> Column names for table %s: %v", tableName, columnNames)

			// Format as a simple table
			for i, record := range table.ExampleRecords {
				log.Printf("FormatSchemaForLLMWithExamples -> Formatting record %d: %+v", i, record)

				result.WriteString(fmt.Sprintf("Record %d:\n", i+1))

				// If we have no columns defined but have records, use the record keys as column names
				if len(columnNames) == 0 {
					for key, val := range record {
						// Format the value based on its type
						var valStr string
						if val == nil {
							valStr = "NULL"
						} else {
							switch v := val.(type) {
							case string:
								valStr = fmt.Sprintf("\"%s\"", v)
							default:
								valStr = fmt.Sprintf("%v", v)
							}
						}
						result.WriteString(fmt.Sprintf("  %s: %s\n", key, valStr))
					}
				} else {
					// Use the defined column names
					for _, colName := range columnNames {
						if val, ok := record[colName]; ok {
							// Format the value based on its type
							var valStr string
							if val == nil {
								valStr = "NULL"
							} else {
								switch v := val.(type) {
								case string:
									valStr = fmt.Sprintf("\"%s\"", v)
								default:
									valStr = fmt.Sprintf("%v", v)
								}
							}
							result.WriteString(fmt.Sprintf("  %s: %s\n", colName, valStr))
						}
					}
				}
				result.WriteString("\n")
			}
		} else {
			log.Printf("FormatSchemaForLLMWithExamples -> No example records for table %s", tableName)
		}

		result.WriteString("\n")
	}

	// Add views information
	if len(storage.FullSchema.Views) > 0 {
		result.WriteString("Views:\n")

		// Sort views for consistent output
		viewNames := make([]string, 0, len(storage.FullSchema.Views))
		for viewName := range storage.FullSchema.Views {
			viewNames = append(viewNames, viewName)
		}
		sort.Strings(viewNames)
		log.Printf("FormatSchemaForLLMWithExamples -> Formatting %d views", len(viewNames))

		for _, viewName := range viewNames {
			view := storage.FullSchema.Views[viewName]
			result.WriteString(fmt.Sprintf("  - %s: %s\n", viewName, view.Definition))
		}
		result.WriteString("\n")
	}

	// Add sequences information
	if len(storage.FullSchema.Sequences) > 0 {
		result.WriteString("Sequences:\n")

		// Sort sequences for consistent output
		sequenceNames := make([]string, 0, len(storage.FullSchema.Sequences))
		for sequenceName := range storage.FullSchema.Sequences {
			sequenceNames = append(sequenceNames, sequenceName)
		}
		sort.Strings(sequenceNames)
		log.Printf("FormatSchemaForLLMWithExamples -> Formatting %d sequences", len(sequenceNames))

		for _, sequenceName := range sequenceNames {
			sequence := storage.FullSchema.Sequences[sequenceName]
			result.WriteString(fmt.Sprintf("  - %s: starts at %d, increments by %d",
				sequenceName,
				sequence.StartValue,
				sequence.Increment))

			if sequence.IsCycled {
				result.WriteString(", cycles")
			}

			result.WriteString("\n")
		}
		result.WriteString("\n")
	}

	// Add enums information
	if len(storage.FullSchema.Enums) > 0 {
		result.WriteString("Enums:\n")

		// Sort enums for consistent output
		enumNames := make([]string, 0, len(storage.FullSchema.Enums))
		for enumName := range storage.FullSchema.Enums {
			enumNames = append(enumNames, enumName)
		}
		sort.Strings(enumNames)
		log.Printf("FormatSchemaForLLMWithExamples -> Formatting %d enums", len(enumNames))

		for _, enumName := range enumNames {
			enum := storage.FullSchema.Enums[enumName]
			result.WriteString(fmt.Sprintf("  - %s: %s\n",
				enumName,
				strings.Join(enum.Values, ", ")))
		}
		result.WriteString("\n")
	}

	log.Printf("FormatSchemaForLLMWithExamples -> Completed formatting schema with %d tables", len(tableNames))
	return result.String()
}
