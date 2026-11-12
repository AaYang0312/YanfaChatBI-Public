package services

import (
	"yanfachatbi/internal/models"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestObjectContextLongUnicodeEnrichmentFitsIndexBudget(t *testing.T) {
	snapshot := indexSnapshot(t, 1)
	object := snapshot.Objects[0]
	rows := []models.TableDescription{{ObjectID: object.Metadata.ObjectID, InstanceVersion: object.Metadata.InstanceVersion, State: models.KnowledgeMapped, Description: strings.Repeat("销售", 10000), FieldDescriptions: []models.FieldDescription{{FieldName: "column_0", State: models.KnowledgeMapped, Description: strings.Repeat("净额", 3000)}}}}
	enrichment := objectIndexEnrichments(rows)
	text := enrichment[object.Metadata.ObjectID]
	if len(text.Description) > 16384 || len(text.Fields["column_0"]) > 4096 || !utf8.ValidString(text.Description) || !utf8.ValidString(text.Fields["column_0"]) {
		t.Fatalf("enrichment byte budget/UTF8 invalid: %d %d", len(text.Description), len(text.Fields["column_0"]))
	}
	if _, err := BuildObjectSchemaChunks(snapshot, "kb", enrichment); err != nil {
		t.Fatal(err)
	}
	if len(rows[0].Description) != 60000 {
		t.Fatal("original manual text mutated")
	}
}
