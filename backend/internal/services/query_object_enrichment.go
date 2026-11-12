package services

import (
	"yanfachatbi/internal/models"
	"unicode/utf8"
)

func objectEnrichmentText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func objectIndexEnrichments(rows []models.TableDescription) map[string]ObjectIndexEnrichment {
	result := map[string]ObjectIndexEnrichment{}
	for _, row := range rows {
		if row.State != models.KnowledgeMapped || row.InstanceVersion == "" {
			continue
		}
		entry := ObjectIndexEnrichment{Description: objectEnrichmentText(row.Description, 16384), Fields: map[string]string{}}
		for _, field := range row.FieldDescriptions {
			if field.State == models.KnowledgeMapped {
				entry.Fields[field.FieldName] = objectEnrichmentText(field.Description, 4096)
			}
		}
		result[row.ObjectID] = entry
	}
	return result
}
